package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

type aiFailureTransport func(*http.Request) (*http.Response, error)

func (f aiFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type aiTimeoutError struct{}

func (aiTimeoutError) Error() string   { return "upstream timeout" }
func (aiTimeoutError) Timeout() bool   { return true }
func (aiTimeoutError) Temporary() bool { return true }

type aiBrokenReader struct {
	data string
	err  error
}

func (r *aiBrokenReader) Read(p []byte) (int, error) {
	if r.data != "" {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

type aiBrokenWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *aiBrokenWriter) Write(p []byte) (int, error) {
	w.writes++
	return 0, errors.New("client disconnected")
}

type aiDataErrorReader struct{}

func (*aiDataErrorReader) Read(p []byte) (int, error) {
	return copy(p, `{"choices":[]}`), io.ErrUnexpectedEOF
}

type aiShortWriter struct{ *httptest.ResponseRecorder }

func (w *aiShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestAIProxyFailureHandling(t *testing.T) {
	const secret = "provider-secret-never-return"
	normalSSE := ": heartbeat\r\n\r\nid: 1\r\nevent: message\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"error is just text\"}}]}\r\n\r\ndata: [DONE]\r\n\r\n"
	cases := []struct {
		name                          string
		transportError                error
		status                        int
		contentType, body, retryAfter string
		reader                        io.ReadCloser
		clientFailure                 bool
		shortWrite                    bool
		wantStatus                    int
		wantFailure                   bool
		wantRefund                    bool
		wantBody                      string
		wantStreamError               bool
		wantRetryAfter                string
		wantLogContains               string
	}{
		{name: "connection failure", transportError: errors.New("connection refused"), wantStatus: 502, wantFailure: true, wantRefund: true},
		{name: "timeout", transportError: aiTimeoutError{}, wantStatus: 504, wantFailure: true, wantRefund: true},
		{name: "context deadline", transportError: context.DeadlineExceeded, wantStatus: 504, wantFailure: true, wantRefund: true},
		{name: "upstream authentication", status: 401, body: `{"error":{"message":"bad ` + secret + `"}}`, wantStatus: 502, wantFailure: true, wantRefund: true},
		{name: "upstream forbidden", status: 403, body: `{"error":{"message":"forbidden"}}`, wantStatus: 502, wantFailure: true, wantRefund: true},
		{name: "upstream rate limit", status: 429, body: `{"error":{"message":"slow down"}}`, retryAfter: "17", wantRetryAfter: "17", wantStatus: 429, wantFailure: true, wantRefund: true},
		{name: "invalid retry header", status: 429, body: `{"error":{"message":"slow down"}}`, retryAfter: "not-a-date", wantStatus: 429, wantFailure: true, wantRefund: true},
		{name: "upstream server error", status: 500, body: `{"message":"unavailable"}`, wantStatus: 502, wantFailure: true, wantRefund: true},
		{name: "upstream timeout status", status: 504, body: `{"message":"timeout"}`, wantStatus: 504, wantFailure: true, wantRefund: true},
		{name: "normal JSON", status: 200, contentType: "application/json", body: `{"choices":[]}`, wantStatus: 200, wantBody: `{"choices":[]}`},
		{name: "JSON business error", status: 200, contentType: "application/json", body: `{"error":{"message":"generation failed"}}`, wantStatus: 200, wantFailure: true, wantRefund: true},
		{name: "normal SSE", status: 200, contentType: "text/event-stream; charset=utf-8", body: normalSSE, wantStatus: 200, wantBody: normalSSE},
		{name: "SSE explicit error", status: 200, contentType: "text/event-stream", body: "event: error\ndata: {\"message\":\"bad " + secret + "\"}\n\n", wantStatus: 200, wantFailure: true, wantRefund: true},
		{name: "SSE error JSON", status: 200, contentType: "text/event-stream", body: "data: {\"error\":{\"message\":\"failed\"}}\n\n", wantStatus: 200, wantFailure: true, wantRefund: true},
		{name: "SSE failed response", status: 200, contentType: "text/event-stream", body: "event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n", wantStatus: 200, wantFailure: true, wantRefund: true},
		{name: "SSE read interrupted", status: 200, contentType: "text/event-stream", reader: io.NopCloser(&aiBrokenReader{data: "data: {\"choices\":[]}\n\npartial", err: io.ErrUnexpectedEOF}), wantStatus: 200, wantFailure: true, wantRefund: true, wantStreamError: true},
		{name: "JSON read interrupted", status: 200, contentType: "application/json", reader: io.NopCloser(&aiBrokenReader{data: "{\"data\":", err: io.ErrUnexpectedEOF}), wantStatus: 200, wantFailure: true, wantRefund: true},
		{name: "client stream write failed", status: 200, contentType: "text/event-stream", body: normalSSE, clientFailure: true, wantStatus: 200, wantFailure: true},
		{name: "completed JSON delivery failed", status: 200, contentType: "application/json", body: `{"choices":[]}`, clientFailure: true, wantStatus: 200, wantFailure: true},
		{name: "completed JSON short write", status: 200, contentType: "application/json", body: `{"choices":[]}`, shortWrite: true, wantStatus: 200, wantFailure: true},
		{name: "client write wins over upstream read error", status: 200, contentType: "application/json", reader: io.NopCloser(&aiDataErrorReader{}), clientFailure: true, wantStatus: 200, wantFailure: true, wantLogContains: "unexpected EOF"},
		{name: "client write wins over SSE error frame", status: 200, contentType: "text/event-stream", body: "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n", clientFailure: true, wantStatus: 200, wantFailure: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			previousConfig := config.Cfg
			previousTransport := http.DefaultTransport
			config.Cfg = config.Config{AILogDir: t.TempDir()}
			t.Cleanup(func() { config.Cfg = previousConfig; http.DefaultTransport = previousTransport })
			http.DefaultTransport = aiFailureTransport(func(r *http.Request) (*http.Response, error) {
				if tt.transportError != nil {
					return nil, tt.transportError
				}
				body := tt.reader
				if body == nil {
					body = io.NopCloser(strings.NewReader(tt.body))
				}
				headers := http.Header{}
				headers.Set("Content-Type", tt.contentType)
				if tt.retryAfter != "" {
					headers.Set("Retry-After", tt.retryAfter)
				}
				return &http.Response{StatusCode: tt.status, Header: headers, Body: body, Request: r}, nil
			})
			request, err := http.NewRequest("POST", "http://upstream.invalid/v1/chat/completions", nil)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			var writer http.ResponseWriter = recorder
			var broken *aiBrokenWriter
			if tt.clientFailure {
				broken = &aiBrokenWriter{ResponseRecorder: recorder}
				writer = broken
			} else if tt.shortWrite {
				writer = &aiShortWriter{ResponseRecorder: recorder}
			}
			failures := 0
			copyAIResponse(writer, request, model.ModelChannel{APIKey: secret}, aiLogContext{Endpoint: "/chat/completions", Model: "fixture"}, func() { failures++ })
			wantFailures := 0
			if tt.wantRefund {
				wantFailures = 1
			}
			if failures != wantFailures {
				t.Fatalf("failure callbacks=%d want=%d", failures, wantFailures)
			}
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if tt.wantBody != "" && recorder.Body.String() != tt.wantBody {
				t.Fatalf("response changed: %q want=%q", recorder.Body.String(), tt.wantBody)
			}
			if recorder.Header().Get("Retry-After") != tt.wantRetryAfter {
				t.Fatalf("Retry-After=%q want=%q", recorder.Header().Get("Retry-After"), tt.wantRetryAfter)
			}
			if strings.Contains(recorder.Body.String(), secret) {
				t.Fatal("response exposed channel secret")
			}
			if tt.wantStreamError {
				if !strings.Contains(recorder.Body.String(), "event:error") && !strings.Contains(recorder.Body.String(), "event: error") {
					t.Fatal("missing SSE error signal")
				}
				if strings.Contains(recorder.Body.String(), "partial") {
					t.Fatal("partial SSE frame leaked before error signal")
				}
			}
			if broken != nil && broken.writes != 1 {
				t.Fatalf("client write retried after disconnect: %d", broken.writes)
			}
			logs, err := service.ListAICallLogs(model.Query{PageSize: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Items) != 1 {
				t.Fatalf("log count=%d", len(logs.Items))
			}
			if (logs.Items[0].Error != "") != tt.wantFailure {
				t.Fatalf("log error=%q expected failure=%v", logs.Items[0].Error, tt.wantFailure)
			}
			if tt.wantLogContains != "" && !strings.Contains(logs.Items[0].Error, tt.wantLogContains) {
				t.Fatalf("missing secondary transport error from log: %q", logs.Items[0].Error)
			}
			encoded, _ := json.Marshal(logs.Items[0])
			if strings.Contains(string(encoded), secret) {
				t.Fatal("log exposed channel secret")
			}
		})
	}
}

func TestAIResponseCopyErrors(t *testing.T) {
	t.Run("short write", func(t *testing.T) {
		_, err := copyAIResponseBody(&aiShortWriter{httptest.NewRecorder()}, strings.NewReader("hello"), true)
		if !errors.Is(err, errAIClientWrite) || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short write was swallowed: %v", err)
		}
	})
	t.Run("capture bounded without truncating delivery", func(t *testing.T) {
		body := strings.Repeat("x", 128*1024)
		recorder := httptest.NewRecorder()
		captured, err := copyAIResponseBody(recorder, strings.NewReader(body), true)
		if err != nil || len(captured) != 64*1024 || recorder.Body.String() != body {
			t.Fatalf("captured=%d delivered=%d error=%v", len(captured), recorder.Body.Len(), err)
		}
	})
}

func TestAIEventStreamSizeLimit(t *testing.T) {
	t.Run("large image event stays intact", func(t *testing.T) {
		frame := "data: {\"image\":\"" + strings.Repeat("A", 128*1024) + "\"}\n\ndata: [DONE]\n\n"
		writer := httptest.NewRecorder()
		capture, err := copyAIEventStream(writer, strings.NewReader(frame), "", len(frame))
		if err != nil || writer.Body.String() != frame || len(capture) != 64*1024 {
			t.Fatalf("large frame: delivered=%d captured=%d err=%v", writer.Body.Len(), len(capture), err)
		}
	})
	t.Run("oversized frame stops without partial delivery", func(t *testing.T) {
		writer := httptest.NewRecorder()
		_, err := copyAIEventStream(writer, strings.NewReader("data: "+strings.Repeat("A", 2048)+"\n\n"), "", 1024)
		if err == nil || !strings.Contains(err.Error(), "size limit") || writer.Body.Len() != 0 {
			t.Fatalf("oversized frame: delivered=%d err=%v", writer.Body.Len(), err)
		}
	})
}
