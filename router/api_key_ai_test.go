package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/router"
	"github.com/tigerowo/infinite-canvas/service"
)

type apiKeyAITransport func(*http.Request) (*http.Response, error)

func (f apiKeyAITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type apiKeyAIInterruptedBody struct{ sent bool }

func (r *apiKeyAIInterruptedBody) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, "data: {\"choices\":[]}\n\n"), nil
}
func (*apiKeyAIInterruptedBody) Close() error { return nil }

type apiKeyAIFailingWriter struct {
	*httptest.ResponseRecorder
	short  bool
	writes int
}

func (w *apiKeyAIFailingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("client disconnected")
}

func TestAPIKeyAIEndToEnd(t *testing.T) {
	// The repository owns a process-wide singleton; isolate this fixture from
	// other router tests while exercising the real Gin/middleware/service chain.
	const marker = "API_KEY_AI_FLOW_CHILD"
	if os.Getenv(marker) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAPIKeyAIEndToEnd$", "-test.timeout=40s", "-test.v")
		command.Env = append(os.Environ(), marker+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated API Key AI flow: %v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	previousConfig, previousTransport := config.Cfg, http.DefaultTransport
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: ":memory:", JWTSecret: "local-key-ai-fixture-secret", AILogDir: t.TempDir(), APIKeyEncryptionKey: strings.Repeat("1", 64)}
	t.Cleanup(func() { config.Cfg = previousConfig; http.DefaultTransport = previousTransport })
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	db, err := repository.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	defer connection.Close()
	user := model.User{ID: "key-ai-user", Username: "key-ai-user", Role: model.UserRoleUser, Status: model.UserStatusActive, Credits: 100}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	allowed := true
	channel := model.ModelChannel{ID: "key-ai-channel", Name: "fixture", Protocol: "openai", BaseURL: "https://upstream.invalid", APIKey: "provider-fixture-secret", Models: []string{"key-model"}, Enabled: true, Weight: 1}
	_, err = repository.SaveSettings(model.Settings{Public: model.PublicSetting{ModelChannel: model.PublicModelChannelSetting{AvailableModels: []string{"key-model"}, ModelCosts: []model.ModelCost{{Model: "key-model", Credits: 1}}, AllowUserRemoteChannel: &allowed}}, Private: model.PrivateSetting{Channels: []model.ModelChannel{channel}}}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	loginToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, service.TokenClaims{UserID: user.ID, Role: user.Role, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(config.Cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	engine := router.New()
	call := func(path, token, body string, delivery ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		writer := httptest.NewRecorder()
		if len(delivery) > 0 && delivery[0] != "" {
			broken := &apiKeyAIFailingWriter{ResponseRecorder: writer, short: delivery[0] == "short"}
			engine.ServeHTTP(broken, request)
			if broken.writes != 1 {
				t.Fatalf("retried client delivery after failure: %d", broken.writes)
			}
		} else {
			engine.ServeHTTP(writer, request)
		}
		return writer
	}
	created := call("/api/v1/api-keys", loginToken, `{"name":"AI integration","rateLimit":6000}`)
	var result struct {
		Code int `json:"code"`
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Code != 0 || result.Data.Key == "" {
		t.Fatalf("session key creation failed: %s %v", created.Body.String(), err)
	}
	apiKey := result.Data.Key
	normalSSE := "data: {\"choices\":[{\"delta\":{\"content\":\"error is ordinary text\"}}]}\n\ndata: [DONE]\n\n"
	tests := []struct {
		name, path, contentType, payload string
		status                           int
		transportError                   error
		interrupted                      bool
		deliveryFailure                  string
		wantStatus                       int
		wantCredits                      float64
		wantExact                        string
	}{
		{name: "image generation", path: "/images/generations", contentType: "application/json", payload: `{"data":[{"url":"https://media.invalid/image"}]}`, status: 200, wantStatus: 200, wantCredits: 99, wantExact: `{"data":[{"url":"https://media.invalid/image"}]}`},
		{name: "chat completion", path: "/chat/completions", contentType: "application/json", payload: `{"choices":[{"message":{"content":"hello"}}]}`, status: 200, wantStatus: 200, wantCredits: 99, wantExact: `{"choices":[{"message":{"content":"hello"}}]}`},
		{name: "audio generation", path: "/audio/speech", contentType: "audio/mpeg", payload: "fixture-audio", status: 200, wantStatus: 200, wantCredits: 99, wantExact: "fixture-audio"},
		{name: "successful stream", path: "/chat/completions", contentType: "text/event-stream", payload: normalSSE, status: 200, wantStatus: 200, wantCredits: 99, wantExact: normalSSE},
		{name: "client disconnect keeps generation charge", path: "/chat/completions", contentType: "application/json", payload: `{"choices":[{"message":{"content":"generated"}}]}`, status: 200, deliveryFailure: "disconnect", wantStatus: 200, wantCredits: 99},
		{name: "client short write keeps generation charge", path: "/chat/completions", contentType: "application/json", payload: `{"choices":[{"message":{"content":"generated"}}]}`, status: 200, deliveryFailure: "short", wantStatus: 200, wantCredits: 99},
		{name: "SSE client disconnect keeps generation charge", path: "/chat/completions", contentType: "text/event-stream", payload: normalSSE, status: 200, deliveryFailure: "disconnect", wantStatus: 200, wantCredits: 99},
		{name: "upstream unauthorized refund", path: "/chat/completions", contentType: "application/json", payload: `{"error":{"message":"upstream rejected"}}`, status: 401, wantStatus: 502, wantCredits: 100},
		{name: "upstream rate limit refund", path: "/images/generations", contentType: "application/json", payload: `{"error":{"message":"slow down"}}`, status: 429, wantStatus: 429, wantCredits: 100},
		{name: "upstream server failure refund", path: "/audio/speech", contentType: "application/json", payload: `{"error":{"message":"unavailable"}}`, status: 500, wantStatus: 502, wantCredits: 100},
		{name: "upstream timeout refund", path: "/chat/completions", transportError: context.DeadlineExceeded, wantStatus: 504, wantCredits: 100},
		{name: "connection failure refund", path: "/chat/completions", transportError: errors.New("connection refused"), wantStatus: 502, wantCredits: 100},
		{name: "SSE error frame refund", path: "/chat/completions", contentType: "text/event-stream", payload: "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n", status: 200, wantStatus: 200, wantCredits: 100},
		{name: "SSE interruption refund", path: "/chat/completions", contentType: "text/event-stream", interrupted: true, status: 200, wantStatus: 200, wantCredits: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("credits", 100).Error; err != nil {
				t.Fatal(err)
			}
			calls := 0
			http.DefaultTransport = apiKeyAITransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Header.Get("Authorization") != "Bearer "+channel.APIKey || strings.Contains(request.Header.Get("Authorization"), apiKey) {
					t.Fatal("application Key was forwarded instead of provider credential")
				}
				if request.URL.String() != channel.BaseURL+"/v1"+tt.path {
					t.Fatalf("upstream target=%s", request.URL)
				}
				if tt.transportError != nil {
					return nil, tt.transportError
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tt.payload))
				if tt.interrupted {
					body = &apiKeyAIInterruptedBody{}
				}
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Content-Type": {tt.contentType}, "Retry-After": {"3"}}, Body: body, Request: request}, nil
			})
			response := call("/api/v1"+tt.path, apiKey, `{"model":"key-model","input":"hello","prompt":"scene","messages":[]}`, tt.deliveryFailure)
			if calls != 1 || response.Code != tt.wantStatus {
				t.Fatalf("upstream calls=%d status=%d want=%d body=%s", calls, response.Code, tt.wantStatus, response.Body.String())
			}
			if tt.wantExact != "" && response.Body.String() != tt.wantExact {
				t.Fatalf("successful response altered: %q", response.Body.String())
			}
			if strings.Contains(response.Body.String(), channel.APIKey) || strings.Contains(response.Body.String(), apiKey) {
				t.Fatal("credential leaked in response")
			}
			current, found, err := repository.GetUserByID(user.ID)
			if err != nil || !found || current.Credits != tt.wantCredits {
				t.Fatalf("balance=%v want=%v error=%v", current.Credits, tt.wantCredits, err)
			}
			if tt.wantStatus >= 400 {
				var body map[string]any
				if json.Unmarshal(response.Body.Bytes(), &body) != nil || body["code"] != float64(1) {
					t.Fatalf("missing error envelope: %s", response.Body.String())
				}
			}
			if tt.status == 429 && response.Header().Get("Retry-After") != "3" {
				t.Fatal("upstream retry hint lost")
			}
			if tt.interrupted && !strings.Contains(response.Body.String(), "stream_error") {
				t.Fatal("stream interruption not signalled")
			}
		})
	}
}
