package repository

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
)

func TestUserAPIKeyAuditRetention(t *testing.T) {
	if MaxUserAPIKeyAuditLogs != 10000 {
		t.Fatalf("default audit retention = %d, want 10000", MaxUserAPIKeyAuditLogs)
	}
	db := apiKeyTestDB(t)
	users := []model.User{
		{ID: "audit-owner", Username: "audit-owner", Role: model.UserRoleUser, Status: model.UserStatusActive},
		{ID: "audit-other", Username: "audit-other", Role: model.UserRoleUser, Status: model.UserStatusActive},
		{ID: "audit-concurrent", Username: "audit-concurrent", Role: model.UserRoleUser, Status: model.UserStatusActive},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"audit-key-a", "audit-key-b"} {
		key := model.UserAPIKey{ID: id, UserID: users[0].ID, Name: id, Prefix: "ic_test", KeyHash: id, Scopes: []string{"read", "generate"}, RateLimit: 60}
		if err := CreateUserAPIKey(key, 100); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	audit := func(userID, keyID, id string, seconds int) model.UserAPIKeyAuditLog {
		return model.UserAPIKeyAuditLog{ID: id, KeyID: keyID, UserID: userID, Method: "GET", Path: "/api/v1/canvas/projects", Status: 200, DurationMs: 1, CreatedAt: at.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)}
	}
	ids := func(t *testing.T, userID string) []string {
		t.Helper()
		var logs []model.UserAPIKeyAuditLog
		if err := db.Where("user_id = ?", userID).Order("created_at desc, id desc").Find(&logs).Error; err != nil {
			t.Fatal(err)
		}
		result := make([]string, 0, len(logs))
		for _, log := range logs {
			result = append(result, log.ID)
		}
		return result
	}
	assertIDs := func(t *testing.T, userID string, want []string) {
		t.Helper()
		if got := ids(t, userID); !reflect.DeepEqual(got, want) {
			t.Fatalf("audit IDs for %s = %v, want %v", userID, got, want)
		}
	}
	save := func(t *testing.T, log model.UserAPIKeyAuditLog, limit int) {
		t.Helper()
		if err := saveUserAPIKeyAuditLog(db, log, limit); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("newest records retained across keys with stable ties", func(t *testing.T) {
		save(t, audit(users[1].ID, "other-key", "other-audit", 0), 3)
		for _, log := range []model.UserAPIKeyAuditLog{
			audit(users[0].ID, "audit-key-a", "audit-01", 0),
			audit(users[0].ID, "audit-key-b", "audit-02", 1),
			audit(users[0].ID, "audit-key-a", "audit-03", 2),
			audit(users[0].ID, "audit-key-b", "audit-04", 2),
			audit(users[0].ID, "audit-key-a", "audit-05", 2),
		} {
			save(t, log, 3)
		}
		assertIDs(t, users[0].ID, []string{"audit-05", "audit-04", "audit-03"})
		assertIDs(t, users[1].ID, []string{"other-audit"})
	})

	t.Run("revocation retains audit within the same user budget", func(t *testing.T) {
		if deleted, err := DeleteUserAPIKey(users[0].ID, "audit-key-a"); err != nil || !deleted {
			t.Fatalf("revoke key: %v, %v", deleted, err)
		}
		assertIDs(t, users[0].ID, []string{"audit-05", "audit-04", "audit-03"})
		save(t, audit(users[0].ID, "audit-key-a", "audit-06", 3), 3)
		assertIDs(t, users[0].ID, []string{"audit-06", "audit-05", "audit-04"})
		save(t, audit(users[0].ID, "audit-key-b", "audit-07", 3), 3)
		assertIDs(t, users[0].ID, []string{"audit-07", "audit-06", "audit-05"})
		assertIDs(t, users[1].ID, []string{"other-audit"})
	})

	t.Run("late audit after account deletion cannot recreate orphan data", func(t *testing.T) {
		if err := DeleteUser(users[0].ID); err != nil {
			t.Fatal(err)
		}
		if err := SaveUserAPIKeyAuditLog(audit(users[0].ID, "audit-key-a", "late-audit", 4)); err != nil {
			t.Fatal(err)
		}
		assertIDs(t, users[0].ID, []string{})
		var count int64
		if err := db.Model(&model.User{}).Where("id = ?", users[0].ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("late audit recreated the deleted user")
		}
		assertIDs(t, users[1].ID, []string{"other-audit"})
	})

	t.Run("concurrent writes retain exactly the newest five", func(t *testing.T) {
		start, results := make(chan struct{}), make(chan error, 16)
		var workers sync.WaitGroup
		for index := range 16 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				log := audit(users[2].ID, fmt.Sprintf("concurrent-key-%d", index%2), fmt.Sprintf("concurrent-%02d", index), index)
				results <- saveUserAPIKeyAuditLog(db, log, 5)
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Errorf("concurrent audit write: %v", err)
			}
		}
		assertIDs(t, users[2].ID, []string{"concurrent-15", "concurrent-14", "concurrent-13", "concurrent-12", "concurrent-11"})
		assertIDs(t, users[1].ID, []string{"other-audit"})
	})
}
