package repository

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm/logger"
)

func TestAPIKeyInsertDoesNotLogCiphertext(t *testing.T) {
	db := apiKeyTestDB(t)
	owner := model.User{ID: "cipher-owner", Username: "cipher-owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	db.Config.Logger = logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})
	key := model.UserAPIKey{ID: "cipher-key-a", UserID: owner.ID, Name: "fixture", KeyHash: strings.Repeat("1", 64), EncryptedKey: "v1:encrypted-fixture-must-not-be-logged"}
	if err := CreateUserAPIKey(key, 100); err != nil {
		t.Fatal("initial encrypted key insert failed")
	}
	key.ID = "cipher-key-b"
	if err := CreateUserAPIKey(key, 100); err == nil {
		t.Fatal("duplicate key digest should fail")
	}
	if strings.Contains(output.String(), key.EncryptedKey) {
		t.Fatal("failed key persistence exposed encrypted credential material in SQL logs")
	}
}

func TestRequiredAPIKeyAuditRejectsDeletedOwner(t *testing.T) {
	apiKeyTestDB(t)
	entry := model.UserAPIKeyAuditLog{ID: "late-reveal", UserID: "deleted-owner", KeyID: "deleted-key", Method: "POST", Path: "/api/v1/api-keys/:id/reveal", Status: 200}
	if err := SaveRequiredUserAPIKeyAuditLog(entry); err == nil {
		t.Fatal("sensitive key disclosure must fail when its owner was deleted before audit persistence")
	}
	if err := SaveUserAPIKeyAuditLog(entry); err != nil {
		t.Fatal("ordinary in-flight request auditing must still skip deleted owners")
	}
}
