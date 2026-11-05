package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/libtnb/sqlite"
	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"gorm.io/gorm"
)

func TestAPIKeyLegacySchemaMigration(t *testing.T) {
	const marker = "API_KEY_LEGACY_SCHEMA_CHILD"
	if os.Getenv(marker) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAPIKeyLegacySchemaMigration$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), marker+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated legacy schema migration: %v\n%s", err, output)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// The deployed table predates encrypted_key; keep its existing policy data.
	if err := legacy.Exec(`CREATE TABLE user_api_keys (
		id TEXT PRIMARY KEY, user_id TEXT, name TEXT, prefix TEXT, key_hash TEXT,
		expires_at DATETIME, last_used_at DATETIME, created_at TEXT, scopes TEXT,
		rate_limit INTEGER, request_limit INTEGER, request_count INTEGER,
		rate_window_started_at DATETIME, rate_window_count INTEGER
	)`).Error; err != nil {
		t.Fatal(err)
	}
	plaintext := "ic_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	expiry := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	if err := legacy.Exec(`INSERT INTO user_api_keys
		(id,user_id,name,prefix,key_hash,expires_at,created_at,scopes,rate_limit,request_limit,request_count,rate_window_count)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "legacy-key", "legacy-owner", "legacy", plaintext[:11], apiKeyHash(plaintext), expiry, now(), `["read"]`, 123, 99, 7, 0).Error; err != nil {
		t.Fatal(err)
	}
	legacyPool, err := legacy.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyPool.Close(); err != nil {
		t.Fatal(err)
	}
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: path, JWTSecret: "legacy-schema-session-secret"}
	db, err := repository.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	user := model.User{ID: "legacy-owner", Username: "legacy-owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if _, err := repository.SaveUser(user); err != nil {
		t.Fatal(err)
	}
	stored, found, err := repository.GetUserAPIKeyByHash(apiKeyHash(plaintext))
	if err != nil || !found || stored.KeyHash != apiKeyHash(plaintext) || stored.EncryptedKey != "" || stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(expiry) || len(stored.Scopes) != 1 || stored.Scopes[0] != "read" || stored.RateLimit != 123 || stored.RequestLimit == nil || *stored.RequestLimit != 99 || stored.RequestCount != 7 {
		t.Fatal("migration changed legacy authentication, expiration, or permissions")
	}
	var encrypted sql.NullString
	if err := db.Raw("SELECT encrypted_key FROM user_api_keys WHERE id = ?", stored.ID).Row().Scan(&encrypted); err != nil || encrypted.Valid {
		t.Fatal("existing hash-only row must keep a NULL encrypted column")
	}
	if resolved, _, err := AuthenticateAccessToken(plaintext); err != nil || resolved.ID != user.ID {
		t.Fatal("legacy hash authentication must work without an encryption master")
	}
	ctx := WithUser(context.Background(), model.PublicUser(user))
	list, err := ListCurrentUserAPIKeys(ctx)
	if err != nil || len(list.Items) != 1 || list.Items[0].CanCopy {
		t.Fatal("legacy hash-only key must remain listed without claiming recovery support")
	}
	value, err := RevealCurrentUserAPIKey(ctx, stored.ID)
	var access *AccessError
	if value != "" || !errors.As(err, &access) || access.Status != http.StatusConflict {
		t.Fatal("legacy hash-only key must be preserved and report that it cannot be recovered")
	}
}
