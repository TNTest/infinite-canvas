package router_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/router"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestAPIKeyReveal(t *testing.T) {
	const marker = "API_KEY_REVEAL_CHILD"
	if os.Getenv(marker) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAPIKeyReveal$", "-test.timeout=40s", "-test.v")
		command.Env = append(os.Environ(), marker+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated API Key reveal flow: %v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	previousConfig, previousWriter := config.Cfg, gin.DefaultWriter
	master := strings.Repeat("1", 64)
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: filepath.Join(t.TempDir(), "reveal.db"), JWTSecret: "reveal-login-fixture", APIKeyEncryptionKey: master}
	gin.SetMode(gin.TestMode)
	gin.DefaultWriter = io.Discard
	t.Cleanup(func() { config.Cfg = previousConfig; gin.DefaultWriter = previousWriter })
	db, err := repository.DB()
	if err != nil {
		t.Fatal("initialize reveal fixture database")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("open reveal fixture connection")
	}
	pool.SetMaxOpenConns(1)
	defer pool.Close()
	password := "fixture-only-password"
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal("hash fixture password")
	}
	users := []model.User{
		{ID: "reveal-owner", Username: "reveal-owner", Password: string(passwordHash), Role: model.UserRoleUser, Status: model.UserStatusActive},
		{ID: "reveal-other", Username: "reveal-other", Password: string(passwordHash), Role: model.UserRoleUser, Status: model.UserStatusActive},
		{ID: "reveal-admin", Username: "reveal-admin", Password: string(passwordHash), Role: model.UserRoleAdmin, Status: model.UserStatusActive},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal("seed reveal fixture users")
	}
	engine := router.New()
	call := func(t *testing.T, method, path, token, body string) (*httptest.ResponseRecorder, apiKeyResponse) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		var response apiKeyResponse
		if json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("%s %s returned invalid JSON", method, path)
		}
		if strings.HasPrefix(path, "/api/v1/api-keys") && w.Code == http.StatusOK && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("API Key management response permits caching")
		}
		return w, response
	}
	login := func(t *testing.T, username string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": username, "password": password})
		w, response := call(t, http.MethodPost, "/api/auth/login", "", string(body))
		var session model.AuthSession
		if w.Code != http.StatusOK || response.Code != 0 || json.Unmarshal(response.Data, &session) != nil || session.Token == "" {
			t.Fatal("fixture login failed")
		}
		return session.Token
	}
	noCredentialFields := func(t *testing.T, raw json.RawMessage) {
		t.Helper()
		var metadata map[string]json.RawMessage
		if json.Unmarshal(raw, &metadata) != nil {
			t.Fatal("invalid Key metadata")
		}
		for field := range metadata {
			lower := strings.ToLower(field)
			if lower == "key" || strings.Contains(lower, "hash") || strings.Contains(lower, "cipher") || strings.Contains(lower, "encrypted") || strings.Contains(lower, "secret") {
				t.Fatal("metadata exposed credential material")
			}
		}
	}
	create := func(t *testing.T, token, name string, expiresAt *time.Time) (model.UserAPIKey, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"name": name, "expiresAt": expiresAt, "scopes": []string{"read", "generate", "write", "delete"}, "rateLimit": 6000})
		w, response := call(t, http.MethodPost, "/api/v1/api-keys", token, string(body))
		var data struct {
			APIKey model.UserAPIKey `json:"apiKey"`
			Key    string           `json:"key"`
		}
		if w.Code != http.StatusOK || response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || data.APIKey.ID == "" || data.Key == "" || !data.APIKey.CanCopy {
			t.Fatal("new Key is not copyable")
		}
		if w.Header().Get("Pragma") != "no-cache" {
			t.Fatal("Key creation response permits legacy caching")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(response.Data, &fields) != nil {
			t.Fatal("invalid creation response")
		}
		noCredentialFields(t, fields["apiKey"])
		return data.APIKey, data.Key
	}
	reveal := func(t *testing.T, token, id string, status int, expected string) *httptest.ResponseRecorder {
		t.Helper()
		w, response := call(t, http.MethodPost, "/api/v1/api-keys/"+id+"/reveal", token, "{}")
		if w.Code != status {
			t.Fatalf("reveal HTTP %d, want %d", w.Code, status)
		}
		if status != http.StatusForbidden && (w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache") {
			t.Fatal("reveal response permits caching")
		}
		if status == http.StatusOK {
			var data struct {
				Key string `json:"key"`
			}
			if response.Code != 0 || json.Unmarshal(response.Data, &data) != nil || data.Key != expected {
				t.Fatal("copied Key differs from its original value")
			}
		} else if response.Code == 0 || string(response.Data) != "null" {
			t.Fatal("failed reveal included response data")
		}
		return w
	}
	countKeys := func(t *testing.T) int64 {
		t.Helper()
		var count int64
		if db.Model(&model.UserAPIKey{}).Count(&count).Error != nil {
			t.Fatal("count fixture keys")
		}
		return count
	}
	ownerJWT, otherJWT, adminJWT := login(t, users[0].Username), login(t, users[1].Username), login(t, users[2].Username)
	ownerKey, original := create(t, ownerJWT, "persistent copy", nil)
	otherKey, _ := create(t, otherJWT, "other owner", nil)
	adminKey, adminOriginal := create(t, adminJWT, "admin owner", nil)
	var stored model.UserAPIKey
	if db.First(&stored, "id = ?", ownerKey.ID).Error != nil || stored.EncryptedKey == "" || strings.Contains(stored.EncryptedKey, original) {
		t.Fatal("copyable Key lacks encrypted persistence")
	}

	t.Run("persisted value survives router recreation and fresh login", func(t *testing.T) {
		engine = router.New()
		ownerJWT = login(t, users[0].Username)
		reveal(t, ownerJWT, ownerKey.ID, http.StatusOK, original)
		w, response := call(t, http.MethodGet, "/api/v1/api-keys", ownerJWT, "")
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if w.Code != http.StatusOK || response.Code != 0 || json.Unmarshal(response.Data, &list) != nil || len(list.Items) != 1 {
			t.Fatal("owner Key list failed")
		}
		for _, item := range list.Items {
			noCredentialFields(t, item)
			var metadata model.UserAPIKey
			if json.Unmarshal(item, &metadata) != nil || !metadata.CanCopy {
				t.Fatal("persisted Key list lost copy availability")
			}
		}
		for _, forbidden := range []string{original, stored.KeyHash, stored.EncryptedKey} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatal("list exposed credential contents")
			}
		}
	})

	t.Run("successful copies are audited before disclosure without consuming quota", func(t *testing.T) {
		var before model.UserAPIKey
		if db.First(&before, "id = ?", ownerKey.ID).Error != nil {
			t.Fatal("load Key usage before copy")
		}
		reveal(t, ownerJWT, ownerKey.ID, http.StatusOK, original)
		logs, err := repository.ListUserAPIKeyAuditLogs(users[0].ID, ownerKey.ID, 100)
		if err != nil || len(logs) == 0 {
			t.Fatal("successful copy omitted its audit")
		}
		found := false
		for _, log := range logs {
			if log.UserID == users[0].ID && log.KeyID == ownerKey.ID && log.Method == http.MethodPost && log.Path == "/api/v1/api-keys/:id/reveal" && log.Status == http.StatusOK && log.DurationMs >= 0 && log.CreatedAt != "" {
				found = true
			}
		}
		if !found {
			t.Fatal("successful copy audit lacks owner, route, status or timing")
		}
		auditJSON, err := json.Marshal(logs)
		if err != nil {
			t.Fatal("serialize copy audit")
		}
		for _, forbidden := range []string{original, stored.KeyHash, stored.EncryptedKey, master} {
			if strings.Contains(string(auditJSON), forbidden) {
				t.Fatal("copy audit retained credential material")
			}
		}
		const callback = "fixture-block-reveal-audit"
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "user_api_key_audit_logs" {
				tx.AddError(errors.New("fixture audit unavailable"))
			}
		}); err != nil {
			t.Fatal("install audit failure fixture")
		}
		callbackActive := true
		defer func() {
			if callbackActive {
				_ = db.Callback().Create().Remove(callback)
			}
		}()
		failed := reveal(t, ownerJWT, ownerKey.ID, http.StatusServiceUnavailable, "")
		if strings.Contains(failed.Body.String(), original) || strings.Contains(failed.Body.String(), stored.EncryptedKey) {
			t.Fatal("failed audit still disclosed the Key")
		}
		if err := db.Callback().Create().Remove(callback); err != nil {
			t.Fatal("remove audit failure fixture")
		}
		callbackActive = false
		reveal(t, ownerJWT, ownerKey.ID, http.StatusOK, original)
		var after model.UserAPIKey
		if db.First(&after, "id = ?", ownerKey.ID).Error != nil || after.RequestCount != before.RequestCount {
			t.Fatal("login-session copy consumed Key request quota")
		}
	})

	t.Run("only owner login sessions can reveal", func(t *testing.T) {
		foreign := reveal(t, otherJWT, ownerKey.ID, http.StatusNotFound, "")
		missing := reveal(t, ownerJWT, "missing-key", http.StatusNotFound, "")
		other := reveal(t, ownerJWT, otherKey.ID, http.StatusNotFound, "")
		adminForeign := reveal(t, adminJWT, ownerKey.ID, http.StatusNotFound, "")
		if foreign.Body.String() != missing.Body.String() || other.Body.String() != missing.Body.String() || adminForeign.Body.String() != missing.Body.String() {
			t.Fatal("foreign Key lookup revealed whether it exists")
		}
		reveal(t, adminJWT, adminKey.ID, http.StatusOK, adminOriginal)
		reveal(t, original, ownerKey.ID, http.StatusForbidden, "")
		reveal(t, adminOriginal, ownerKey.ID, http.StatusForbidden, "")
	})

	t.Run("expired keys remain copyable without extending expiration", func(t *testing.T) {
		future := time.Now().Add(time.Hour).UTC()
		timedKey, timedOriginal := create(t, ownerJWT, "expired copy", &future)
		past := time.Now().Add(-time.Hour).UTC()
		if db.Model(&model.UserAPIKey{}).Where("id = ?", timedKey.ID).Update("expires_at", past).Error != nil {
			t.Fatal("expire fixture Key")
		}
		reveal(t, ownerJWT, timedKey.ID, http.StatusOK, timedOriginal)
		var expired model.UserAPIKey
		if db.First(&expired, "id = ?", timedKey.ID).Error != nil || expired.ExpiresAt == nil || !expired.ExpiresAt.Equal(past) {
			t.Fatal("copying changed Key expiration")
		}
		w, _ := call(t, http.MethodGet, "/api/v1/canvas/projects", timedOriginal, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatal("copying reactivated the expired Key")
		}
	})

	t.Run("revoked key cannot be copied", func(t *testing.T) {
		key, _ := create(t, ownerJWT, "revoked copy", nil)
		w, response := call(t, http.MethodDelete, "/api/v1/api-keys/"+key.ID, ownerJWT, "")
		if w.Code != http.StatusOK || response.Code != 0 {
			t.Fatal("revoke fixture Key")
		}
		reveal(t, ownerJWT, key.ID, http.StatusNotFound, "")
	})

	legacyOriginal := "ic_" + strings.Repeat("x", 43)
	legacyHash := sha256.Sum256([]byte(legacyOriginal))
	legacy := model.UserAPIKey{ID: "legacy-hash-only", UserID: users[0].ID, Name: "legacy", Prefix: legacyOriginal[:11], KeyHash: hex.EncodeToString(legacyHash[:]), Scopes: []string{"read"}, RateLimit: 6000}
	if db.Create(&legacy).Error != nil {
		t.Fatal("seed hash-only fixture Key")
	}
	t.Run("hash only keys stay usable but cannot be copied", func(t *testing.T) {
		_, response := call(t, http.MethodGet, "/api/v1/api-keys", ownerJWT, "")
		var list struct {
			Items []model.UserAPIKey `json:"items"`
		}
		if response.Code != 0 || json.Unmarshal(response.Data, &list) != nil {
			t.Fatal("list legacy Key")
		}
		found := false
		for _, item := range list.Items {
			if item.ID == legacy.ID {
				found = true
				if item.CanCopy {
					t.Fatal("hash-only Key incorrectly marked copyable")
				}
			}
		}
		if !found {
			t.Fatal("hash-only Key missing from list")
		}
		reveal(t, ownerJWT, legacy.ID, http.StatusConflict, "")
		w, _ := call(t, http.MethodGet, "/api/v1/canvas/projects", legacyOriginal, "")
		if w.Code != http.StatusOK {
			t.Fatal("hash-only Key authentication stopped working")
		}
	})

	t.Run("master configuration fails closed without blocking hash authentication", func(t *testing.T) {
		defer func() { config.Cfg.APIKeyEncryptionKey = master }()
		for _, invalid := range []string{"", "invalid-master-format"} {
			config.Cfg.APIKeyEncryptionKey = invalid
			before := countKeys(t)
			w, response := call(t, http.MethodPost, "/api/v1/api-keys", ownerJWT, `{"name":"must not persist"}`)
			if w.Code != http.StatusServiceUnavailable || response.Code == 0 || string(response.Data) != "null" || countKeys(t) != before {
				t.Fatal("invalid master configuration created a credential or failed open")
			}
			reveal(t, ownerJWT, ownerKey.ID, http.StatusServiceUnavailable, "")
			for _, key := range []string{original, legacyOriginal} {
				w, _ = call(t, http.MethodGet, "/api/v1/canvas/projects", key, "")
				if w.Code != http.StatusOK {
					t.Fatal("invalid encryption master broke independent hash authentication")
				}
			}
		}
		config.Cfg.APIKeyEncryptionKey = strings.Repeat("2", 64)
		w := reveal(t, ownerJWT, ownerKey.ID, http.StatusServiceUnavailable, "")
		for _, forbidden := range []string{original, stored.KeyHash, stored.EncryptedKey, master, config.Cfg.APIKeyEncryptionKey} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Fatal("failed decryption leaked credential material")
			}
		}
	})

	t.Run("corrupt ciphertext fails safely", func(t *testing.T) {
		if db.Model(&model.UserAPIKey{}).Where("id = ?", ownerKey.ID).Update("encrypted_key", "corrupt-ciphertext").Error != nil {
			t.Fatal("corrupt fixture ciphertext")
		}
		w := reveal(t, ownerJWT, ownerKey.ID, http.StatusServiceUnavailable, "")
		if strings.Contains(w.Body.String(), "corrupt-ciphertext") || strings.Contains(w.Body.String(), original) {
			t.Fatal("corrupt ciphertext reveal leaked data")
		}
	})
}
