package router_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

type apiKeyResponse struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

type createdAPIKey struct {
	APIKey struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Prefix     string   `json:"prefix"`
		ExpiresAt  *string  `json:"expiresAt"`
		LastUsedAt *string  `json:"lastUsedAt"`
		Scopes     []string `json:"scopes"`
	} `json:"apiKey"`
	Key string `json:"key"`
}

func TestAPIKeyRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := config.Cfg
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: filepath.Join(t.TempDir(), "api-keys.db"), JWTSecret: "api-key-route-test-secret", APIKeyEncryptionKey: strings.Repeat("1", 64)}
	t.Cleanup(func() { config.Cfg = previous })
	db, err := repository.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = connection.Close() })

	users := []model.User{
		{ID: "key-owner", Username: "key-owner", Role: model.UserRoleUser, Status: model.UserStatusActive},
		{ID: "key-other", Username: "key-other", Role: model.UserRoleUser, Status: model.UserStatusActive},
		{ID: "key-admin", Username: "key-admin", Role: model.UserRoleAdmin, Status: model.UserStatusActive},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	engine := router.New()
	request := func(method, path, token, body string) (int, apiKeyResponse) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		var response apiKeyResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s %s returned invalid JSON: %v", method, path, err)
		}
		if path == "/api/v1/api-keys" && response.Code == 0 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("API key creation/list response must prohibit caching")
		}
		return w.Code, response
	}
	jwtFor := func(user model.User) string {
		t.Helper()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, service.TokenClaims{
			UserID: user.ID,
			Role:   user.Role,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}).SignedString([]byte(config.Cfg.JWTSecret))
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	create := func(token, name string, expiresAt *string, scopes ...string) createdAPIKey {
		t.Helper()
		payload := map[string]any{"name": name, "expiresAt": expiresAt, "rateLimit": 1000}
		if len(scopes) > 0 {
			payload["scopes"] = scopes
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		status, response := request(http.MethodPost, "/api/v1/api-keys", token, string(body))
		if status != http.StatusOK || response.Code != 0 {
			t.Fatalf("create key: HTTP %d, code %d", status, response.Code)
		}
		var created createdAPIKey
		if err := json.Unmarshal(response.Data, &created); err != nil {
			t.Fatal(err)
		}
		if created.APIKey.ID == "" || !strings.HasPrefix(created.Key, "ic_") || len(created.Key) < 40 || created.APIKey.Name != name || created.APIKey.Prefix == "" || created.APIKey.Prefix == created.Key {
			t.Fatal("creation response lacks key metadata or a high-entropy secret")
		}
		return created
	}
	ownerJWT, otherJWT, adminJWT := jwtFor(users[0]), jwtFor(users[1]), jwtFor(users[2])

	t.Run("authentication and expiration validation", func(t *testing.T) {
		for _, token := range []string{"", "ic_invalid"} {
			status, _ := request(http.MethodGet, "/api/v1/api-keys", token, "")
			if status != http.StatusUnauthorized {
				t.Fatalf("unauthenticated list: HTTP %d", status)
			}
		}
		for _, body := range []string{`{"name":"bad","expiresAt":"not-a-date"}`, `{"name":"past","expiresAt":"2000-01-01T00:00:00Z"}`, `{"name":`} {
			_, response := request(http.MethodPost, "/api/v1/api-keys", ownerJWT, body)
			if response.Code == 0 {
				t.Fatal("invalid key creation input was accepted")
			}
		}
	})
	ownerKey := create(ownerJWT, "Permanent automation", nil, "read", "generate", "write", "delete")
	if ownerKey.APIKey.ExpiresAt != nil || ownerKey.APIKey.LastUsedAt != nil {
		t.Fatal("new permanent key must have null expiration and last-used time")
	}
	var stored map[string]any
	if err := db.Table("user_api_keys").Where("id = ?", ownerKey.APIKey.ID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(ownerKey.Key))
	if stored["key_hash"] != hex.EncodeToString(hash[:]) {
		t.Fatal("database must store the SHA-256 digest of the key")
	}
	storedJSON, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(storedJSON), ownerKey.Key) {
		t.Fatal("database stored the plaintext key")
	}
	expiration := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	timedKey := create(ownerJWT, "Timed automation", &expiration, "read", "generate", "write", "delete")
	if timedKey.APIKey.ExpiresAt == nil || *timedKey.APIKey.ExpiresAt != expiration {
		t.Fatal("expiration was not preserved")
	}
	otherKey := create(otherJWT, "Other account", nil)
	adminKey := create(adminJWT, "Administrator automation", nil)

	t.Run("identity ownership and secret visibility", func(t *testing.T) {
		status, me := request(http.MethodGet, "/api/auth/me", ownerKey.Key, "")
		var user model.AuthUser
		if err := json.Unmarshal(me.Data, &user); err != nil {
			t.Fatal(err)
		}
		if status != http.StatusOK || me.Code != 0 || user.ID != users[0].ID || user.Role != model.UserRoleUser {
			t.Fatal("API key did not resolve to its current owner")
		}
		_, saved := request(http.MethodPost, "/api/v1/canvas/projects", ownerKey.Key, `{"data":{"id":"owner-project","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`)
		if saved.Code != 0 {
			t.Fatal("key could not write to an existing user API")
		}
		_, ownProjects := request(http.MethodGet, "/api/v1/canvas/projects", ownerKey.Key, "")
		_, otherProjects := request(http.MethodGet, "/api/v1/canvas/projects", otherKey.Key, "")
		if ownProjects.Code != 0 || !strings.Contains(string(ownProjects.Data), "owner-project") || otherProjects.Code != 0 || strings.Contains(string(otherProjects.Data), "owner-project") {
			t.Fatal("existing resource ownership was not preserved")
		}
		_, list := request(http.MethodGet, "/api/v1/api-keys", ownerJWT, "")
		var keys struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(list.Data, &keys); err != nil {
			t.Fatal(err)
		}
		if list.Code != 0 || keys.Total != 2 || len(keys.Items) != 2 || strings.Contains(string(list.Data), ownerKey.Key) || strings.Contains(string(list.Data), otherKey.APIKey.ID) {
			t.Fatal("list exposed a secret/another user or returned an incorrect count")
		}
		used := false
		for _, key := range keys.Items {
			for field := range key {
				if strings.Contains(strings.ToLower(field), "hash") || field == "key" {
					t.Fatal("list exposed credential material")
				}
			}
			if key["id"] == ownerKey.APIKey.ID && key["lastUsedAt"] != nil {
				used = true
			}
		}
		if !used {
			t.Fatal("using a key did not record lastUsedAt")
		}
		_, _ = request(http.MethodDelete, "/api/v1/api-keys/"+ownerKey.APIKey.ID, otherJWT, "")
		status, _ = request(http.MethodGet, "/api/v1/canvas/projects", ownerKey.Key, "")
		if status != http.StatusOK {
			t.Fatal("another user revoked the owner's key")
		}
	})

	t.Run("route scopes and session-only control plane", func(t *testing.T) {
		keys := map[string]string{}
		for _, scope := range []string{"read", "generate", "write", "delete"} {
			keys[scope] = create(ownerJWT, "scope-"+scope, nil, scope).Key
		}
		keys["read+write"] = create(ownerJWT, "scope-read-write", nil, "read", "write").Key
		keys["all"] = ownerKey.Key
		exerciseAPIKeyRoutePolicy(t, engine, keys, ownerJWT, adminJWT, adminKey.Key, timedKey.Key)
	})

	t.Run("limits audit and recovery", func(t *testing.T) {
		exerciseAPIKeyLimitsAndAudit(t, engine, ownerJWT, otherJWT, users[0].ID, db)
	})

	t.Run("current role controls administrator access", func(t *testing.T) {
		status, _ := request(http.MethodGet, "/api/admin/users", ownerKey.Key, "")
		if status != http.StatusForbidden {
			t.Fatal("ordinary user key gained administrator access")
		}
		status, response := request(http.MethodGet, "/api/admin/users", adminKey.Key, "")
		if status != http.StatusForbidden || response.Code == 0 {
			t.Fatal("administrator key must not use session-only administrator API")
		}
		status, response = request(http.MethodGet, "/api/admin/users", adminJWT, "")
		if status != http.StatusOK || response.Code != 0 {
			t.Fatal("administrator session cannot access control plane")
		}
		if err := db.Model(&model.User{}).Where("id = ?", users[2].ID).Update("role", model.UserRoleUser).Error; err != nil {
			t.Fatal(err)
		}
		status, _ = request(http.MethodGet, "/api/admin/users", adminKey.Key, "")
		if status != http.StatusForbidden {
			t.Fatal("key retained access to session-only administrator API")
		}
		status, _ = request(http.MethodGet, "/api/admin/users", adminJWT, "")
		if status != http.StatusForbidden {
			t.Fatal("session retained stale administrator role")
		}
	})

	t.Run("expiration revocation and account lifecycle", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		if err := db.Table("user_api_keys").Where("id = ?", timedKey.APIKey.ID).Update("expires_at", past).Error; err != nil {
			t.Fatal(err)
		}
		status, _ := request(http.MethodGet, "/api/v1/canvas/projects", timedKey.Key, "")
		if status != http.StatusUnauthorized {
			t.Fatal("expired key remained valid")
		}
		_, response := request(http.MethodDelete, "/api/v1/api-keys/"+ownerKey.APIKey.ID, ownerJWT, "")
		if response.Code != 0 {
			t.Fatal("owner could not revoke key")
		}
		status, _ = request(http.MethodGet, "/api/v1/canvas/projects", ownerKey.Key, "")
		if status != http.StatusUnauthorized {
			t.Fatal("revoked key remained valid")
		}
		if err := db.Model(&model.User{}).Where("id = ?", users[1].ID).Update("status", model.UserStatusBan).Error; err != nil {
			t.Fatal(err)
		}
		status, _ = request(http.MethodGet, "/api/v1/canvas/projects", otherKey.Key, "")
		if status != http.StatusUnauthorized {
			t.Fatal("banned user's key remained valid")
		}
		if err := repository.DeleteUser(users[2].ID); err != nil {
			t.Fatal(err)
		}
		status, _ = request(http.MethodGet, "/api/v1/canvas/projects", adminKey.Key, "")
		if status != http.StatusUnauthorized {
			t.Fatal("deleted user's key remained valid")
		}
	})
	t.Run("database failure is distinct from invalid credentials", func(t *testing.T) {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
		status, response := request(http.MethodGet, "/api/v1/canvas/projects", otherKey.Key, "")
		if status != http.StatusServiceUnavailable || response.Code == 0 {
			t.Fatalf("database outage misreported as invalid key: HTTP %d", status)
		}
	})

}
