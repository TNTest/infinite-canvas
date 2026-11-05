package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

const userModelChannelHeader = "X-User-Model-Channel-ID"

func selectAIRequestChannel(user model.AuthUser, modelName string, channelID string, userChannelID string, publicOnly bool) (model.ModelChannel, string, error) {
	userChannelID = strings.TrimSpace(userChannelID)
	if userChannelID != "" {
		channel, err := service.SelectUserLocalModelChannelForModel(user.ID, modelName, userChannelID)
		return channel, userChannelID, err
	}
	if !service.UserCanUseRemoteModelChannel(user) {
		return model.ModelChannel{}, "", fmt.Errorf("当前账号未开放云端渠道")
	}
	channel, err := service.SelectModelChannelForModel(modelName, channelID, publicOnly)
	return channel, "", err
}

func failAIChannelSelect(w http.ResponseWriter, err error, fallback string) {
	message := strings.TrimSpace(err.Error())
	switch message {
	case "当前账号未开放云端渠道", "请先登录", "缺少模型名称", "缺少模型渠道", "本地渠道不存在", "本地渠道配置不完整", "本地渠道不支持该模型", "指定模型渠道不可用", "模型未开放":
		Fail(w, message)
	default:
		Fail(w, fallback)
	}
}

func AIImagesGenerations(w http.ResponseWriter, r *http.Request) {
	proxyAIRequest(w, r, "/images/generations")
}

func AIImagesEdits(w http.ResponseWriter, r *http.Request) {
	proxyAIRequest(w, r, "/images/edits")
}

func AIChatCompletions(w http.ResponseWriter, r *http.Request) {
	proxyAIRequest(w, r, "/chat/completions")
}

func AIResponses(w http.ResponseWriter, r *http.Request) {
	proxyAIRequest(w, r, "/responses")
}

func AIVideos(w http.ResponseWriter, r *http.Request) {
	proxyAIVideoTaskRequest(w, r)
}

func AIVideo(w http.ResponseWriter, r *http.Request, id string) {
	if serveAIVideoTask(w, r, id) {
		return
	}
	if isClientVideoTaskID(id) {
		OK(w, map[string]any{"id": id, "task_id": id, "object": "video", "status": "queued", "progress": 0})
		return
	}
	proxyAIGetRequest(w, r, "/videos/"+id)
}

func AIVideoContent(w http.ResponseWriter, r *http.Request, id string) {
	for _, adapter := range builtinAIProtocols {
		if adapter.videoContent != nil && adapter.videoContent(w, r, id) {
			return
		}
	}
	proxyAIGetRequest(w, r, "/videos/"+id+"/content")
}

func AIAudioSpeech(w http.ResponseWriter, r *http.Request) {
	proxyAIRequest(w, r, "/audio/speech")
}

func AITTSVoices(w http.ResponseWriter, r *http.Request) {
	modelName := strings.TrimSpace(r.URL.Query().Get("model"))
	if modelName == "" {
		Fail(w, "缺少模型名称")
		return
	}
	proxyAIGetRequest(w, r, "/tts/voices?model="+url.QueryEscape(modelName))
}

func proxyAIGetRequest(w http.ResponseWriter, r *http.Request, path string) {
	startedAt := time.Now()
	user, ok := service.UserFromContext(r.Context())
	if !ok {
		Fail(w, "未登录或权限不足")
		return
	}
	modelName := r.URL.Query().Get("model")
	if strings.TrimSpace(modelName) == "" {
		modelName = "Agnes-Video-V2.0"
	}
	channel, _, err := selectAIRequestChannel(user, modelName, r.Header.Get("X-Model-Channel-ID"), r.Header.Get(userModelChannelHeader), false)
	if err != nil {
		log.Printf("AI proxy select channel failed: model=%s err=%v", modelName, err)
		failAIChannelSelect(w, err, "AI 接口请求失败")
		return
	}
	upstreamPath := resolveAIProxyPath(channel, modelName, path)
	method := http.MethodGet
	var request *http.Request
	if service.IsMiniMaxChannel(channel) && service.IsMiniMaxTTSModelName(modelName) && strings.HasPrefix(path, "/tts/voices") {
		method = http.MethodPost
		upstreamPath = "/v1/get_voice"
		request, err = http.NewRequest(method, resolveAIProxyURL(channel, modelName, upstreamPath), strings.NewReader(`{"voice_type":"system"}`))
	} else {
		request, err = http.NewRequest(method, resolveAIProxyURL(channel, modelName, upstreamPath), nil)
	}
	if err != nil {
		Fail(w, "AI 接口请求失败")
		return
	}
	service.SetModelChannelAuthHeader(request, channel)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	copyAIResponse(w, request, channel, aiLogContext{StartedAt: startedAt, Endpoint: path, Method: method, Model: modelName, Channel: channel, UserID: user.ID, UserDisplayName: firstNonEmpty(user.DisplayName, user.Username), RequestBody: summarizeQueryParams(r.URL.Query())}, nil)
}

func proxyAIRequest(w http.ResponseWriter, r *http.Request, path string) {
	startedAt := time.Now()
	body, contentType, modelName, err := readAIRequest(r)
	if err != nil {
		log.Printf("AI proxy request read failed: %v", err)
		Fail(w, "AI 接口请求失败")
		return
	}
	user, ok := service.UserFromContext(r.Context())
	if !ok {
		Fail(w, "未登录或权限不足")
		return
	}
	channel, userChannelID, err := selectAIRequestChannel(user, modelName, r.Header.Get("X-Model-Channel-ID"), r.Header.Get(userModelChannelHeader), true)
	if err != nil {
		log.Printf("AI proxy select channel failed: model=%s err=%v", modelName, err)
		failAIChannelSelect(w, err, "AI 接口请求失败")
		return
	}
	credits := 0.0
	if userChannelID == "" {
		credits, err = service.ModelCost(modelName)
		if err != nil {
			log.Printf("AI proxy read model cost failed: model=%s err=%v", modelName, err)
			Fail(w, "AI 接口请求失败")
			return
		}
		credits *= float64(readAIRequestCount(body, contentType, false))
	}
	if serveParameterTranslation(w, r, body, path, channel, user, credits, startedAt) {
		return
	}
	upstreamPath := resolveAIProxyPath(channel, modelName, path)
	prepared, _, err := prepareAIProtocolRequest(aiProtocolRequest{
		mode: aiProtocolProxyRequest, body: body, contentType: contentType, modelName: modelName,
		channel: channel, endpoint: path, path: upstreamPath,
	})
	if err != nil {
		log.Printf("AI proxy normalize %s request failed: model=%s err=%v", prepared.failureLabel, modelName, err)
		message := "AI 接口请求失败"
		if prepared.failureLabel == "MiMo TTS" || prepared.failureLabel == "MiniMax TTS" || prepared.failureLabel == "AutoDL" {
			message = err.Error()
		}
		Fail(w, message)
		return
	}
	ctx := context.Background()
	if r.Header.Get("X-Canvas-Node-Request") == "true" {
		ctx = r.Context()
	}
	if ctx.Err() != nil {
		return
	}
	body, contentType, upstreamPath = prepared.body, prepared.contentType, prepared.path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, service.BuildModelChannelURL(channel, upstreamPath), bytes.NewReader(body))
	if err != nil {
		log.Printf("AI proxy build request failed: url=%s err=%v", service.BuildModelChannelURL(channel, upstreamPath), err)
		Fail(w, "AI 接口请求失败")
		return
	}
	service.SetModelChannelAuthHeader(request, channel)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if credits > 0 {
		if err := service.ConsumeUserCredits(user.ID, modelName, credits, upstreamPath); err != nil {
			FailError(w, err)
			return
		}
	}
	copyAIResponse(w, request, channel, aiLogContext{
		StartedAt:       startedAt,
		Endpoint:        path,
		Method:          http.MethodPost,
		Model:           modelName,
		Channel:         channel,
		UserID:          user.ID,
		UserDisplayName: firstNonEmpty(user.DisplayName, user.Username),
		Credits:         credits,
		RequestBody:     summarizeAIRequest(body, contentType),
	}, func() {
		if credits > 0 && ctx.Err() == nil {
			if err := service.RefundUserCredits(user.ID, modelName, credits, upstreamPath); err != nil {
				log.Printf("AI proxy refund credits failed: user=%s model=%s credits=%g err=%v", user.ID, modelName, credits, err)
			}
		}
	})
}

func geminiStreamRequested(body []byte) bool {
	var payload struct {
		Stream *bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream != nil && *payload.Stream
}

type aiLogContext struct {
	StartedAt       time.Time
	Endpoint        string
	Method          string
	Model           string
	Channel         model.ModelChannel
	UserID          string
	UserDisplayName string
	Credits         float64
	RequestBody     string
	OutcomeKnown    bool
}

func copyAIResponse(w http.ResponseWriter, request *http.Request, channel model.ModelChannel, logContext aiLogContext, onFailure func()) {
	if onFailure != nil {
		onFailure = sync.OnceFunc(onFailure)
	}
	response, err := service.HTTPClientForChannel(channel).Do(request)
	if err != nil {
		message := redactAIChannelSecret(err.Error(), channel.APIKey)
		log.Printf("AI proxy request failed: %s", message)
		if onFailure != nil {
			onFailure()
		}
		saveAIProxyLog(logContext, 0, "", message)
		status := http.StatusBadGateway
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
			status = http.StatusGatewayTimeout
		}
		FailWithStatus(w, status, "AI 接口请求失败")
		return
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
		payload = []byte(redactAIChannelSecret(string(payload), channel.APIKey))
		log.Printf("AI upstream error: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(payload)))
		if onFailure != nil {
			onFailure()
		}
		saveAIProxyLog(logContext, response.StatusCode, string(payload), strings.TrimSpace(string(payload)))
		message := readUpstreamAIErrorMessage(payload, response.StatusCode)
		if service.IsTokenDanceChannel(channel) {
			message = firstNonEmpty(service.TokenDanceRecoveryMessage(response.Header.Get("TokenDance-Recovery-Action")), message)
		}
		status := response.StatusCode
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status >= 500 && status != http.StatusGatewayTimeout {
			status = http.StatusBadGateway
		}
		if status == http.StatusTooManyRequests {
			if retryAfter := response.Header.Get("Retry-After"); validAIRetryAfter(retryAfter) {
				w.Header().Set("Retry-After", retryAfter)
			}
		}
		FailWithStatus(w, status, message)
		return
	}

	if copyAIProtocolResponse(w, response, request, channel, logContext, onFailure) {
		return
	}

	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	var responseBody string
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		responseBody, err = copyAIEventStream(w, response.Body, channel.APIKey, 64*1024*1024)
		if err != nil && !errors.Is(err, errAIClientWrite) && !errors.Is(err, errAIResponseFailure) {
			// Once streaming begins the HTTP status is committed. Signal the failure
			// as an SSE event rather than appending a second JSON response.
			_ = sse.Encode(w, sse.Event{Event: "error", Data: map[string]any{"error": map[string]string{"message": "AI 响应传输中断", "type": "stream_error"}}})
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	} else {
		responseBody, err = copyAIResponseBody(w, response.Body, !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "video/"))
		if err == nil && strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "json") && aiResponsePayloadFailed([]byte(responseBody)) {
			err = errAIResponseFailure
		}
	}
	errorMessage := ""
	if err != nil {
		errorMessage = redactAIChannelSecret(err.Error(), channel.APIKey)
		log.Printf("AI proxy response failed: %s", errorMessage)
		// A failed client delivery does not prove generation failed. Refunding
		// on disconnect would let callers consume an upstream result for free.
		if onFailure != nil && !errors.Is(err, errAIClientWrite) {
			onFailure()
		}
	}
	saveAIProxyLog(logContext, response.StatusCode, redactAIChannelSecret(responseBody, channel.APIKey), errorMessage)
}

var (
	errAIClientWrite     = errors.New("client response write failed")
	errAIResponseFailure = errors.New("upstream AI response reported failure")
)

func redactAIChannelSecret(value string, secret string) string {
	if secret != "" {
		return strings.ReplaceAll(value, secret, "[redacted]")
	}
	return value
}

func validAIRetryAfter(value string) bool {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return true
	}
	_, err := http.ParseTime(value)
	return err == nil
}

func copyAIResponseBody(w http.ResponseWriter, body io.Reader, capture bool) (string, error) {
	buffer := make([]byte, 32*1024)
	var logBuffer strings.Builder
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if err := writeAIResponseChunk(w, buffer[:n], &logBuffer, capture); err != nil {
				if readErr != nil && !errors.Is(readErr, io.EOF) {
					err = errors.Join(err, readErr)
				}
				return logBuffer.String(), err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return logBuffer.String(), nil
			}
			return logBuffer.String(), readErr
		}
	}
}

func writeAIResponseChunk(w http.ResponseWriter, data []byte, capture *strings.Builder, keep bool) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return errors.Join(errAIClientWrite, err)
	}
	if keep && capture.Len() < 64*1024 {
		_, _ = capture.Write(data[:min(n, 64*1024-capture.Len())])
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func copyAIEventStream(w http.ResponseWriter, body io.Reader, secret string, maxFrameBytes int) (string, error) {
	reader := bufio.NewReader(body)
	var frame bytes.Buffer
	var capture strings.Builder
	writeFrame := func() error {
		// The existing SSE decoder handles fields, comments and multiline data.
		// Normalize CRLF only for inspection; successful frames stay byte-for-byte.
		events, err := sse.Decode(strings.NewReader(strings.ReplaceAll(frame.String(), "\r\n", "\n")))
		if err != nil {
			return err
		}
		failed := false
		for _, event := range events {
			data, _ := event.Data.(string)
			if event.Event == "error" || event.Event == "response.failed" || aiResponsePayloadFailed([]byte(data)) {
				failed = true
			}
		}
		data := frame.Bytes()
		if failed {
			data = []byte(redactAIChannelSecret(string(data), secret))
		}
		if err := writeAIResponseChunk(w, data, &capture, true); err != nil {
			return err
		}
		frame.Reset()
		if failed {
			return errAIResponseFailure
		}
		return nil
	}
	continued := false
	for {
		line, err := reader.ReadSlice('\n')
		if frame.Len()+len(line) > maxFrameBytes {
			return capture.String(), errors.New("upstream SSE event exceeds size limit")
		}
		_, _ = frame.Write(line)
		if !continued && (bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n"))) {
			if frameErr := writeFrame(); frameErr != nil {
				return capture.String(), frameErr
			}
		}
		continued = errors.Is(err, bufio.ErrBufferFull)
		if continued {
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return capture.String(), err
			}
			if frame.Len() > 0 {
				if frameErr := writeFrame(); frameErr != nil {
					return capture.String(), frameErr
				}
			}
			return capture.String(), nil
		}
	}
}

func aiResponsePayloadFailed(data []byte) bool {
	var payload struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return false
	}
	return len(payload.Error) > 0 && string(payload.Error) != "null" || payload.Type == "error" || payload.Type == "response.failed" || payload.Response.Status == "failed"
}

func saveAIProxyLog(context aiLogContext, status int, responseBody string, errorMessage string) {
	if context.StartedAt.IsZero() {
		context.StartedAt = time.Now()
	}
	if service.IsGeminiChannel(context.Channel) {
		if context.Endpoint == "/images/generations" || context.Endpoint == "/images/edits" || context.Endpoint == "/audio/speech" {
			responseBody = "[redacted Gemini media response]"
		} else if strings.TrimSpace(responseBody) != "" {
			responseBody = summarizeAIRequest([]byte(responseBody), "application/json")
		}
	}
	service.SaveAICallLog(service.AICallLogInput{
		UserID:          context.UserID,
		UserDisplayName: context.UserDisplayName,
		Endpoint:        context.Endpoint,
		Method:          context.Method,
		Model:           context.Model,
		ChannelID:       context.Channel.ID,
		ChannelName:     context.Channel.Name,
		Status:          status,
		DurationMs:      time.Since(context.StartedAt).Milliseconds(),
		Credits:         context.Credits,
		RequestBody:     context.RequestBody,
		ResponseBody:    responseBody,
		Error:           errorMessage,
		OutcomeKnown:    context.OutcomeKnown,
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func summarizeAIRequest(body []byte, contentType string) string {
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return summarizeMultipartAIRequest(body, contentType)
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err == nil {
		redactLargeImages(&payload)
		if encoded, err := json.MarshalIndent(payload, "", "  "); err == nil {
			return string(encoded)
		}
	}
	return string(body)
}

func summarizeQueryParams(values map[string][]string) string {
	if len(values) == 0 {
		return ""
	}
	encoded, _ := json.MarshalIndent(values, "", "  ")
	return string(encoded)
}

func summarizeMultipartAIRequest(body []byte, contentType string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "multipart/form-data"
	}
	form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(32 << 20)
	if err != nil {
		return "multipart/form-data"
	}
	defer form.RemoveAll()
	summary := map[string]any{"fields": form.Value}
	files := []map[string]any{}
	for field, headers := range form.File {
		for _, header := range headers {
			files = append(files, map[string]any{"field": field, "filename": header.Filename, "size": header.Size, "contentType": header.Header.Get("Content-Type")})
		}
	}
	summary["files"] = files
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	return string(encoded)
}

func readUpstreamAIErrorMessage(body []byte, statusCode int) string {
	var payload struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Msg     string `json:"msg"`
		Message string `json:"message"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil {
		if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
			return payload.Error.Message
		}
		if strings.TrimSpace(payload.Msg) != "" {
			return payload.Msg
		}
		if strings.TrimSpace(payload.Message) != "" {
			return payload.Message
		}
	}
	if statusCode > 0 {
		return fmt.Sprintf("AI 接口请求失败：%d", statusCode)
	}
	return "AI 接口请求失败"
}

func redactLargeImages(value *any) {
	switch typed := (*value).(type) {
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok && (strings.HasPrefix(text, "data:image/") || strings.HasPrefix(text, "data:video/") || strings.HasPrefix(text, "data:audio/") || len(text) > 2048 && looksLikeBase64(text)) {
				typed[key] = fmt.Sprintf("[redacted media/string len=%d]", len(text))
				continue
			}
			redactLargeImages(&item)
			typed[key] = item
		}
	case []any:
		for index, item := range typed {
			redactLargeImages(&item)
			typed[index] = item
		}
	}
}

func looksLikeBase64(value string) bool {
	for _, char := range value[:min(len(value), 200)] {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '+' || char == '/' || char == '=') {
			return false
		}
	}
	return true
}

func readAIRequest(r *http.Request) ([]byte, string, string, error) {
	contentType := r.Header.Get("Content-Type")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, "", "", err
	}
	modelName := ""
	if strings.HasPrefix(contentType, "multipart/form-data") {
		modelName = readMultipartModel(body, contentType)
	} else {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		modelName = payload.Model
	}
	if strings.TrimSpace(modelName) == "" {
		return nil, "", "", errMissingModel
	}
	return body, contentType, modelName, nil
}

func readMultipartModel(body []byte, contentType string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	form, err := reader.ReadForm(32 << 20)
	if err != nil {
		return ""
	}
	defer form.RemoveAll()
	if values := form.Value["model"]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func readAIRequestCount(body []byte, contentType string, video bool) int {
	count := 1
	var videoPayload map[string]any
	if strings.HasPrefix(contentType, "multipart/form-data") {
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			return count
		}
		form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(32 << 20)
		if err != nil {
			return count
		}
		defer form.RemoveAll()
		if video {
			count = 0
			for _, field := range []string{"seconds", "duration"} {
				if values := form.Value[field]; len(values) > 0 {
					if _, err := fmt.Sscan(values[0], &count); err == nil && count != 0 {
						break
					}
				}
			}
		} else if values := form.Value["n"]; len(values) > 0 {
			_, _ = fmt.Sscan(values[0], &count)
		}
	} else if video {
		count = 0
		if json.Unmarshal(body, &videoPayload) == nil {
			for _, field := range []string{"seconds", "duration", "parameters.durationSeconds"} {
				if _, err := fmt.Sscan(readStringPath(videoPayload, field), &count); err == nil && count != 0 {
					break
				}
			}
		}
	} else {
		var payload struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(body, &payload)
		count = payload.N
	}
	if video && count == -1 {
		return 15
	}
	if video && count < 1 {
		frames := readIntPath(videoPayload, "num_frames")
		frameRate := readIntPath(videoPayload, "frame_rate")
		if frames > 1 && frameRate > 0 {
			count = (frames - 1 + frameRate - 1) / frameRate
		}
	}
	if count < 1 {
		return 1
	}
	return count
}

func readAIRequestResolution(body []byte, contentType string) string {
	if strings.HasPrefix(contentType, "multipart/form-data") {
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			return ""
		}
		form, err := multipart.NewReader(bytes.NewReader(body), params["boundary"]).ReadForm(32 << 20)
		if err != nil {
			return ""
		}
		defer form.RemoveAll()
		for _, field := range []string{"resolution_name", "resolution"} {
			if values := form.Value[field]; len(values) > 0 {
				return values[0]
			}
		}
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return firstNonEmpty(
		readStringPath(payload, "resolution_name"),
		readStringPath(payload, "resolution"),
		readStringPath(payload, "parameters.resolution"),
		readStringPath(payload, "_parameterTranslation.variables.resolution"),
	)
}

func resolveAIProxyURL(channel model.ModelChannel, modelName string, path string) string {
	for _, adapter := range builtinAIProtocols {
		if adapter.url != nil {
			if resolved, ok := adapter.url(channel, modelName, path); ok {
				return resolved
			}
		}
	}
	return service.BuildModelChannelURL(channel, path)
}

func agnesVideoQueryID(modelName string, path string) (string, bool) {
	if !isAgnesVideoModel(modelName) || !strings.HasPrefix(path, "/videos/") || strings.HasSuffix(path, "/content") {
		return "", false
	}
	id := strings.TrimPrefix(path, "/videos/")
	if strings.HasPrefix(id, "video_") {
		return id, true
	}
	return "", false
}

func resolveAIProxyPath(channel model.ModelChannel, modelName string, path string) string {
	for _, adapter := range builtinAIProtocols {
		if adapter.path != nil {
			if resolved, ok := adapter.path(channel, modelName, path); ok {
				return resolved
			}
		}
	}
	return path
}

func isCogVideoX3Model(modelName string) bool {
	return strings.EqualFold(strings.TrimSpace(modelName), "cogvideox-3")
}

func isAgnesVideoModel(modelName string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(modelName)), "agnes-video")
}

var errMissingModel = &aiError{"缺少模型名称"}

type aiError struct {
	message string
}

func (err *aiError) Error() string {
	return err.message
}
