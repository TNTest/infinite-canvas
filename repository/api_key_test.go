package repository

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

func TestCreateUserAPIKeyLimit(t *testing.T) {
	db := apiKeyTestDB(t)
	owner := model.User{ID: "owner", Username: "owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	key := func(index int) model.UserAPIKey {
		return model.UserAPIKey{ID: fmt.Sprintf("key-%d", index), UserID: owner.ID, Name: "automation", Prefix: "ic_test", KeyHash: fmt.Sprintf("%064x", index), Scopes: []string{"read", "generate"}, RateLimit: 60}
	}
	for i := range 99 {
		if err := CreateUserAPIKey(key(i), 100); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 16)
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- CreateUserAPIKey(key(99+i), 100)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAPIKeyLimit):
			limited++
		default:
			t.Errorf("unexpected concurrent create error: %v", err)
		}
	}
	if succeeded != 1 || limited != 15 {
		t.Fatalf("concurrent limit: %d succeeded, %d rejected; want 1 and 15", succeeded, limited)
	}
	keys, err := ListUserAPIKeys(owner.ID)
	if err != nil || len(keys) != 100 {
		t.Fatalf("key count = %d, error %v; want 100", len(keys), err)
	}
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&model.UserAPIKey{}).Where("user_id = ?", owner.ID).Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if err := CreateUserAPIKey(key(200), 100); !errors.Is(err, ErrAPIKeyLimit) {
		t.Fatalf("expired keys must still count toward limit: %v", err)
	}
	if deleted, err := DeleteUserAPIKey(owner.ID, keys[0].ID); err != nil || !deleted {
		t.Fatalf("delete key: %v, %v", deleted, err)
	}
	if err := CreateUserAPIKey(key(200), 100); err != nil {
		t.Fatalf("deleting a key should free one slot: %v", err)
	}
	if err := DeleteUser(owner.ID); err != nil {
		t.Fatal(err)
	}
	if keys, err := ListUserAPIKeys(owner.ID); err != nil || len(keys) != 0 {
		t.Fatalf("deleting owner must remove its credentials: %d, %v", len(keys), err)
	}
	if err := CreateUserAPIKey(key(201), 100); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted owner must not acquire new keys: %v", err)
	}
}

func apiKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousConfig, previousDB, previousErr := config.Cfg, db, dbErr
	db, dbErr, dbOnce = nil, nil, sync.Once{}
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: filepath.Join(t.TempDir(), "api-key.db") + "?_pragma=busy_timeout(5000)"}
	t.Cleanup(func() {
		if db != nil {
			if pool, err := db.DB(); err == nil {
				_ = pool.Close()
			}
		}
		config.Cfg, db, dbErr, dbOnce = previousConfig, previousDB, previousErr, sync.Once{}
		if previousDB != nil || previousErr != nil {
			dbOnce.Do(func() {})
		}
	})
	testDB, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(8)
	return testDB
}

func TestConsumeUserAPIKeyRequest(t *testing.T) {
	db := apiKeyTestDB(t)
	owner := model.User{ID: "owner", Username: "owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	newKey := func(id string, rate int, quota *int64, expiresAt *time.Time) model.UserAPIKey {
		t.Helper()
		key := model.UserAPIKey{ID: id, UserID: owner.ID, Name: id, Prefix: "ic_test", KeyHash: id, Scopes: []string{"read", "generate"}, RateLimit: rate, RequestLimit: quota, ExpiresAt: expiresAt}
		if err := CreateUserAPIKey(key, 100); err != nil {
			t.Fatal(err)
		}
		return key
	}
	loadKey := func(id string) model.UserAPIKey {
		t.Helper()
		var key model.UserAPIKey
		if err := db.First(&key, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		return key
	}
	consumeConcurrently := func(id string, count int) []error {
		t.Helper()
		start, results := make(chan struct{}), make(chan error, count)
		var workers sync.WaitGroup
		for range count {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				results <- ConsumeUserAPIKeyRequest(id)
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		errs := make([]error, 0, count)
		for err := range results {
			errs = append(errs, err)
		}
		return errs
	}

	t.Run("rate limit is atomic and windows reset", func(t *testing.T) {
		key := newKey("rate-key", 3, nil, nil)
		accepted, rejected := 0, 0
		for _, err := range consumeConcurrently(key.ID, 16) {
			var rateError *APIKeyRateLimitError
			switch {
			case err == nil:
				accepted++
			case errors.As(err, &rateError):
				rejected++
				if rateError.RetryAfter < 1 || rateError.RetryAfter > 60 {
					t.Errorf("RetryAfter = %d, want 1..60", rateError.RetryAfter)
				}
			default:
				t.Errorf("unexpected concurrent consume error: %v", err)
			}
		}
		stored := loadKey(key.ID)
		if accepted != 3 || rejected != 13 || stored.RequestCount != 3 || stored.RateWindowCount != 3 || stored.LastUsedAt == nil {
			t.Fatalf("rate limiter admitted %d, rejected %d, stored total/window %d/%d", accepted, rejected, stored.RequestCount, stored.RateWindowCount)
		}
		previousWindow := time.Now().Add(-61 * time.Second)
		if err := db.Model(&model.UserAPIKey{}).Where("id = ?", key.ID).Update("rate_window_started_at", previousWindow).Error; err != nil {
			t.Fatal(err)
		}
		nextWindow := time.Now().UTC()
		if err := ConsumeUserAPIKeyRequest(key.ID); err != nil {
			t.Fatalf("new rate window should allow a request: %v", err)
		}
		stored = loadKey(key.ID)
		if stored.RequestCount != 4 || stored.RateWindowCount != 1 || stored.RateWindowStartedAt == nil || stored.RateWindowStartedAt.Before(nextWindow) {
			t.Fatalf("new window counts = %d/%d, want 4/1", stored.RequestCount, stored.RateWindowCount)
		}
	})

	t.Run("request quota is atomic and rejections are not counted", func(t *testing.T) {
		quota := int64(2)
		key := newKey("quota-key", 60, &quota, nil)
		accepted, rejected := 0, 0
		for _, err := range consumeConcurrently(key.ID, 16) {
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrAPIKeyRequestLimit):
				rejected++
			default:
				t.Errorf("unexpected concurrent quota error: %v", err)
			}
		}
		stored := loadKey(key.ID)
		if accepted != 2 || rejected != 14 || stored.RequestCount != 2 || stored.RateWindowCount != 2 || stored.LastUsedAt == nil {
			t.Fatalf("quota admitted %d, rejected %d, stored total/window %d/%d", accepted, rejected, stored.RequestCount, stored.RateWindowCount)
		}
		previousUsage := *stored.LastUsedAt
		if err := db.Model(&model.UserAPIKey{}).Where("id = ?", key.ID).Update("rate_window_started_at", time.Now().Add(-61*time.Second)).Error; err != nil {
			t.Fatal(err)
		}
		if err := ConsumeUserAPIKeyRequest(key.ID); !errors.Is(err, ErrAPIKeyRequestLimit) {
			t.Fatalf("new minute must not reset lifetime quota: %v", err)
		}
		stored = loadKey(key.ID)
		if stored.RequestCount != 2 || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(previousUsage) {
			t.Fatal("quota rejection changed usage metadata")
		}
	})

	t.Run("expiration boundary and revocation recheck", func(t *testing.T) {
		key := newKey("expired-key", 60, nil, &at)
		if err := ConsumeUserAPIKeyRequest(key.ID); !errors.Is(err, ErrAPIKeyExpired) {
			t.Fatalf("expiresAt equal to request time must fail: %v", err)
		}
		if stored := loadKey(key.ID); stored.RequestCount != 0 || stored.LastUsedAt != nil {
			t.Fatal("expired request changed usage metadata")
		}
		key = newKey("revoked-key", 60, nil, nil)
		snapshot, found, err := GetUserAPIKeyByHash(key.KeyHash)
		if err != nil || !found {
			t.Fatalf("authentication snapshot: %v, %v", found, err)
		}
		if deleted, err := DeleteUserAPIKey(owner.ID, key.ID); err != nil || !deleted {
			t.Fatalf("delete key: %v, %v", deleted, err)
		}
		if err := ConsumeUserAPIKeyRequest(snapshot.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("revocation between authentication and admission must fail: %v", err)
		}
	})

	t.Run("admitted request audit survives key revocation", func(t *testing.T) {
		key := newKey("audited-key", 60, nil, nil)
		if err := ConsumeUserAPIKeyRequest(key.ID); err != nil {
			t.Fatal(err)
		}
		if stored := loadKey(key.ID); stored.RequestCount != 1 {
			t.Fatal("admitted request was not counted")
		}
		log := model.UserAPIKeyAuditLog{ID: "audit-entry", KeyID: key.ID, UserID: owner.ID, Method: "GET", Path: "/api/v1/canvas/projects", Status: 200, DurationMs: 12, CreatedAt: at.Format(time.RFC3339)}
		if err := SaveUserAPIKeyAuditLog(log); err != nil {
			t.Fatal(err)
		}
		if deleted, err := DeleteUserAPIKey(owner.ID, key.ID); err != nil || !deleted {
			t.Fatalf("revoke admitted key: %v, %v", deleted, err)
		}
		// An already admitted request can finish after revocation; its audit must remain.
		log.ID = "audit-finished-after-revocation"
		if err := SaveUserAPIKeyAuditLog(log); err != nil {
			t.Fatal(err)
		}
		logs, err := ListUserAPIKeyAuditLogs(owner.ID, key.ID, 20)
		if err != nil || len(logs) != 2 {
			t.Fatalf("revocation erased the request audit: %d records, %v", len(logs), err)
		}
		logs, err = ListUserAPIKeyAuditLogs("different-user", key.ID, 20)
		if err != nil || len(logs) != 0 {
			t.Fatalf("audit owner isolation failed: %d records, %v", len(logs), err)
		}
		if err := DeleteUser(owner.ID); err != nil {
			t.Fatal(err)
		}
		logs, err = ListUserAPIKeyAuditLogs(owner.ID, key.ID, 20)
		if err != nil || len(logs) != 0 {
			t.Fatalf("deleting owner left audit data: %d records, %v", len(logs), err)
		}
	})

	t.Run("database failures remain distinguishable", func(t *testing.T) {
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Close(); err != nil {
			t.Fatal(err)
		}
		err = ConsumeUserAPIKeyRequest("any-key")
		var rateError *APIKeyRateLimitError
		if err == nil || errors.Is(err, ErrAPIKeyExpired) || errors.Is(err, ErrAPIKeyRequestLimit) || errors.Is(err, gorm.ErrRecordNotFound) || errors.As(err, &rateError) {
			t.Fatalf("database failure was masked as credential/usage rejection: %v", err)
		}
	})
}
