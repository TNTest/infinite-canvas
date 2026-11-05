package router_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tigerowo/infinite-canvas/middleware"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"gorm.io/gorm"
)

func exerciseAPIKeyLimitsAndAudit(t *testing.T, engine *gin.Engine, ownerJWT, otherJWT, ownerID string, db *gorm.DB) {
	t.Helper()
	call := func(target *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		target.ServeHTTP(w, r)
		return w
	}
	create := func(body string) createdAPIKey {
		t.Helper()
		w := call(engine, http.MethodPost, "/api/v1/api-keys", ownerJWT, body)
		var envelope struct {
			Code int           `json:"code"`
			Data createdAPIKey `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != http.StatusOK || envelope.Code != 0 || envelope.Data.Key == "" {
			t.Fatalf("create limits fixture: HTTP %d %s", w.Code, w.Body)
		}
		return envelope.Data
	}
	limited := create(`{"name":"request-quota","scopes":["read"],"rateLimit":1000,"requestLimit":1}`)
	// A scope denial is still an authenticated attempt and consumes the one
	// available request. Quota rejections themselves must not keep incrementing.
	w := call(engine, http.MethodGet, "/api/v1/user-config", limited.Key, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("session-only endpoint not denied: HTTP %d", w.Code)
	}
	for range 2 {
		w = call(engine, http.MethodGet, "/api/v1/canvas/projects", limited.Key, "")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "额度") {
			t.Fatalf("quota did not reject next request: HTTP %d %s", w.Code, w.Body)
		}
	}
	var stored model.UserAPIKey
	if err := db.First(&stored, "id = ?", limited.APIKey.ID).Error; err != nil || stored.RequestCount != 1 {
		t.Fatalf("quota rejected attempts incremented count: %d, %v", stored.RequestCount, err)
	}

	rpm := create(`{"name":"request-rate","scopes":["read"],"rateLimit":1}`)
	w = call(engine, http.MethodGet, "/api/v1/canvas/projects?secret-query=excluded", rpm.Key, "")
	if w.Code != http.StatusOK {
		t.Fatalf("first RPM request failed: HTTP %d %s", w.Code, w.Body)
	}
	w = call(engine, http.MethodGet, "/api/v1/canvas/projects", rpm.Key, "")
	retryAfter, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != http.StatusTooManyRequests || err != nil || retryAfter < 1 || retryAfter > 60 {
		t.Fatalf("RPM response lacks bounded Retry-After: HTTP %d headers %v", w.Code, w.Header())
	}
	logs, err := repository.ListUserAPIKeyAuditLogs(ownerID, rpm.APIKey.ID, 100)
	if err != nil || len(logs) != 2 {
		t.Fatalf("allowed and rate-denied requests not both audited: %d, %v", len(logs), err)
	}
	statuses := map[int]bool{}
	for _, entry := range logs {
		statuses[entry.Status] = true
		if entry.Path != "/api/v1/canvas/projects" || entry.Method != http.MethodGet || entry.KeyID != rpm.APIKey.ID || entry.UserID != ownerID {
			t.Fatalf("audit contains the wrong route/owner/key: %+v", entry)
		}
	}
	if !statuses[http.StatusOK] || !statuses[http.StatusTooManyRequests] {
		t.Fatalf("audit statuses = %v", statuses)
	}
	w = call(engine, http.MethodGet, "/api/v1/api-keys/"+rpm.APIKey.ID+"/logs", ownerJWT, "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), rpm.Key) || strings.Contains(w.Body.String(), "secret-query") {
		t.Fatalf("session audit response failed or leaked request material: HTTP %d %s", w.Code, w.Body)
	}
	w = call(engine, http.MethodGet, "/api/v1/api-keys/"+rpm.APIKey.ID+"/logs", otherJWT, "")
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), `"keyId"`) {
		t.Fatal("another user could read the key's audit log")
	}

	panicKey := create(`{"name":"panic-audit","scopes":["read"],"rateLimit":1000}`)
	panicRouter := gin.New()
	panicRouter.Use(gin.RecoveryWithWriter(io.Discard))
	panicRouter.GET("/api/v1/canvas/projects", middleware.UserAuth, func(c *gin.Context) { panic("fixture panic") })
	w = call(panicRouter, http.MethodGet, "/api/v1/canvas/projects", panicKey.Key, "")
	logs, err = repository.ListUserAPIKeyAuditLogs(ownerID, panicKey.APIKey.ID, 100)
	if w.Code != http.StatusInternalServerError || err != nil || len(logs) != 1 || logs[0].Status != http.StatusInternalServerError {
		t.Fatalf("panic was swallowed or audited as HTTP 200: HTTP %d logs %+v error %v", w.Code, logs, err)
	}

	// Audit failure must be observable to the server without replacing an
	// already completed business response or pretending the event was stored.
	t.Run("audit persistence failure is explicitly logged", func(t *testing.T) {
		var output bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&output)
		defer log.SetOutput(previous)
		const callback = "api-key-test-audit-failure"
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "user_api_key_audit_logs" {
				tx.AddError(errors.New("fixture audit storage failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer db.Callback().Create().Remove(callback)
		w := call(engine, http.MethodGet, "/api/v1/canvas/projects", panicKey.Key, "")
		if w.Code != http.StatusOK || !strings.Contains(output.String(), "API key audit write failed") || strings.Contains(output.String(), panicKey.Key) {
			t.Fatalf("audit storage failure obscured business result or server log: HTTP %d, %s", w.Code, &output)
		}
	})
}
