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
	previous := config.Cfg
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: filepath.Join(t.TempDir(), "limit.db") + "?_pragma=busy_timeout(5000)"}
	t.Cleanup(func() { config.Cfg = previous })
	db, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = pool.Close() })
	owner := model.User{ID: "owner", Username: "owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	key := func(index int) model.UserAPIKey {
		return model.UserAPIKey{ID: fmt.Sprintf("key-%d", index), UserID: owner.ID, Name: "automation", Prefix: "ic_test", KeyHash: fmt.Sprintf("%064x", index)}
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
