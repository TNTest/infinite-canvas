package service

import "testing"

func TestAICallSSEFailureExtraction(t *testing.T) {
	cases := []struct {
		name, body string
		failed     bool
	}{
		{name: "ordinary delta mentions error", body: "data: {\"choices\":[{\"delta\":{\"content\":\"the error is explained\"}}]}\n\ndata: [DONE]\n\n"},
		{name: "JSON message content mentions error", body: `{"choices":[{"message":{"role":"assistant","content":"the error is explained"}}]}`},
		{name: "ordinary delta JSON text contains error field", body: "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"error\\\":\\\"sample\\\"}\"}}]}\n\n"},
		{name: "explicit event", body: "event: error\ndata: {\"message\":\"upstream rejected\"}\n\n", failed: true},
		{name: "CRLF error payload", body: "data: {\"error\":{\"message\":\"upstream rejected\"}}\r\n\r\n", failed: true},
		{name: "failed responses event", body: "event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n", failed: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			failed := false
			reason := extractAICallFailureReason(tt.body, &failed)
			if (reason != "") != tt.failed {
				t.Fatalf("reason=%q expected failure=%v", reason, tt.failed)
			}
			if failed != tt.failed {
				t.Fatalf("failed=%v expected=%v", failed, tt.failed)
			}
		})
	}
}
