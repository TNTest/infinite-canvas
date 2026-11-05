package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

func TestAPIKeyValidation(t *testing.T) {
	if _, err := ListCurrentUserAPIKeys(context.Background()); err == nil {
		t.Fatal("anonymous key listing should fail")
	}
	if _, err := CreateCurrentUserAPIKey(context.Background(), "key", nil); err == nil {
		t.Fatal("anonymous key creation should fail")
	}
	if err := DeleteCurrentUserAPIKey(context.Background(), "key"); err == nil {
		t.Fatal("anonymous key deletion should fail")
	}
	ctx := WithUser(context.Background(), model.AuthUser{ID: "owner", Role: model.UserRoleUser})
	past := time.Now().Add(-time.Second)
	for _, input := range []struct {
		name      string
		expiresAt *time.Time
	}{
		{name: "  "},
		{name: strings.Repeat("密", 101)},
		{name: "expired", expiresAt: &past},
	} {
		if _, err := CreateCurrentUserAPIKey(ctx, input.name, input.expiresAt); err == nil {
			t.Fatalf("invalid input accepted: %q", input.name)
		}
	}
	for _, token := range []string{"ic_", "ic_invalid", "ic_" + strings.Repeat("x", 100)} {
		if _, ok := CurrentAuthUser(token); ok {
			t.Fatal("invalid key accepted")
		}
	}
	for _, scopes := range [][]string{{}, {"admin"}, {"read", "unknown"}} {
		if _, err := CreateCurrentUserAPIKey(ctx, "key", nil, APIKeyOptions{Scopes: &scopes}); err == nil {
			t.Fatal("invalid scopes accepted")
		}
	}
	for _, rate := range []int{0, -1, 6001} {
		if _, err := CreateCurrentUserAPIKey(ctx, "key", nil, APIKeyOptions{RateLimit: &rate}); err == nil {
			t.Fatal("invalid RPM accepted")
		}
	}
	for _, limit := range []int64{0, -1} {
		if _, err := CreateCurrentUserAPIKey(ctx, "key", nil, APIKeyOptions{RequestLimit: &limit}); err == nil {
			t.Fatal("invalid request quota accepted")
		}
	}
	keyCtx := WithAPIKey(ctx, "key-id")
	if id, ok := APIKeyIDFromContext(keyCtx); !ok || id != "key-id" {
		t.Fatal("API key context was not preserved")
	}
	if _, err := ListCurrentUserAPIKeys(keyCtx); err == nil {
		t.Fatal("API key must not list credentials")
	}
	if _, err := CreateCurrentUserAPIKey(keyCtx, "key", nil); err == nil {
		t.Fatal("API key must not mint credentials")
	}
	if err := DeleteCurrentUserAPIKey(keyCtx, "key-id"); err == nil {
		t.Fatal("API key must not revoke credentials")
	}
	if _, err := ListCurrentUserAPIKeyAuditLogs(keyCtx, "key-id"); err == nil {
		t.Fatal("API key must not read management audit logs")
	}
}

func TestAPIKeyAccessTokens(t *testing.T) {
	const marker = "API_KEY_ACCESS_TOKENS_CHILD"
	if os.Getenv(marker) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAPIKeyAccessTokens$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), marker+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated API key access: %v\n%s", err, output)
		}
		return
	}
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: filepath.Join(t.TempDir(), "access.db"), JWTSecret: "access-test-secret", APIKeyEncryptionKey: strings.Repeat("1", 64)}
	db, err := repository.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	user := model.User{ID: "owner", Username: "owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if _, err := repository.SaveUser(user); err != nil {
		t.Fatal(err)
	}
	ctx := WithUser(context.Background(), model.PublicUser(user))
	created, err := CreateCurrentUserAPIKey(ctx, "default", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(created.APIKey.Scopes, ",") != "read,generate" || created.APIKey.RateLimit != 60 || created.APIKey.RequestLimit != nil {
		t.Fatalf("incorrect defaults: %+v", created.APIKey)
	}
	checkAccess := func(err error, status int) *AccessError {
		t.Helper()
		var access *AccessError
		if !errors.As(err, &access) || access.Status != status {
			t.Fatalf("expected access error %d, got %v", status, err)
		}
		return access
	}
	resolved, key, err := AuthenticateAccessToken(created.Key)
	if err != nil || key == nil || key.ID != created.APIKey.ID || resolved.ID != user.ID {
		t.Fatalf("key authentication failed: %v", err)
	}
	if _, ok := CurrentAuthUser(created.Key); !ok {
		t.Fatal("CurrentAuthUser wrapper rejected valid key")
	}
	stored, found, err := repository.GetUserAPIKeyByHash(apiKeyHash(created.Key))
	if err != nil || !found || stored.RequestCount != 0 || stored.LastUsedAt != nil {
		t.Fatal("authentication must not consume quota or touch last-used time")
	}
	jwt, err := newToken(user)
	if err != nil {
		t.Fatal(err)
	}
	user.Role = model.UserRoleAdmin
	if _, err := repository.SaveUser(user); err != nil {
		t.Fatal(err)
	}
	resolved, jwtKey, err := AuthenticateAccessToken(jwt)
	if err != nil || jwtKey != nil || resolved.Role != model.UserRoleAdmin {
		t.Fatal("JWT must resolve current role with nil key metadata")
	}
	resolved, key, err = AuthenticateAccessToken(created.Key)
	if err != nil || key == nil || resolved.Role != model.UserRoleAdmin {
		t.Fatal("API key must resolve current role")
	}

	scopes, rpm, limit := []string{"read"}, 2, int64(3)
	limited, err := CreateCurrentUserAPIKey(ctx, "limited", nil, APIKeyOptions{Scopes: &scopes, RateLimit: &rpm, RequestLimit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ConsumeAPIKeyRequest(limited.APIKey.ID); err != nil {
			t.Fatal(err)
		}
	}
	access := checkAccess(ConsumeAPIKeyRequest(limited.APIKey.ID), http.StatusTooManyRequests)
	if access.RetryAfter < 1 || access.RetryAfter > 60 {
		t.Fatalf("invalid RetryAfter: %d", access.RetryAfter)
	}
	if err := db.Model(&model.UserAPIKey{}).Where("id = ?", limited.APIKey.ID).Update("rate_window_started_at", time.Now().Add(-61*time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := ConsumeAPIKeyRequest(limited.APIKey.ID); err != nil {
		t.Fatal(err)
	}
	checkAccess(ConsumeAPIKeyRequest(limited.APIKey.ID), http.StatusForbidden)

	if _, err := repository.DeleteUserAPIKey(user.ID, created.APIKey.ID); err != nil {
		t.Fatal(err)
	}
	checkAccess(ConsumeAPIKeyRequest(created.APIKey.ID), http.StatusUnauthorized)
	_, key, err = AuthenticateAccessToken(created.Key)
	checkAccess(err, http.StatusUnauthorized)
	if key != nil {
		t.Fatal("revoked key must not authenticate as an existing credential")
	}
	expiring, err := CreateCurrentUserAPIKey(ctx, "expiring", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UserAPIKey{}).Where("id = ?", expiring.APIKey.ID).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	_, key, err = AuthenticateAccessToken(expiring.Key)
	checkAccess(err, http.StatusUnauthorized)
	if key == nil || key.ID != expiring.APIKey.ID {
		t.Fatal("known expired key metadata must remain available for auditing")
	}
	checkAccess(ConsumeAPIKeyRequest(expiring.APIKey.ID), http.StatusUnauthorized)

	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("status", model.UserStatusBan).Error; err != nil {
		t.Fatal(err)
	}
	_, key, err = AuthenticateAccessToken(limited.Key)
	checkAccess(err, http.StatusUnauthorized)
	if key == nil {
		t.Fatal("known banned user's key metadata is required for auditing")
	}
	if err := db.Model(&model.User{}).Where("id = ?", user.ID).Update("status", model.UserStatusActive).Error; err != nil {
		t.Fatal(err)
	}
	legacy := model.UserAPIKey{ID: "legacy", UserID: user.ID, KeyHash: strings.Repeat("0", 64)}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	list, err := ListCurrentUserAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.ID == legacy.ID && (item.Scopes == nil || len(item.Scopes) != 0 || item.RateLimit != 0) {
			t.Fatal("legacy metadata must expose [] without granting permissions")
		}
	}
	checkAccess(ConsumeAPIKeyRequest(legacy.ID), http.StatusForbidden)
	if err := db.Delete(&model.User{}, "id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	_, key, err = AuthenticateAccessToken(limited.Key)
	checkAccess(err, http.StatusUnauthorized)
	if key == nil {
		t.Fatal("known deleted user's key metadata is required for auditing")
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = AuthenticateAccessToken(jwt)
	checkAccess(err, http.StatusServiceUnavailable)
	_, _, err = AuthenticateAccessToken(limited.Key)
	checkAccess(err, http.StatusServiceUnavailable)
	checkAccess(ConsumeAPIKeyRequest(limited.APIKey.ID), http.StatusServiceUnavailable)
}
