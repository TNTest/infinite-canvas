package repository

import (
	"errors"
	"math"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrAPIKeyLimit = errors.New("API key limit reached")

var ErrAPIKeyExpired = errors.New("API key expired")
var ErrAPIKeyRequestLimit = errors.New("API key request limit reached")
var ErrAPIKeyPolicy = errors.New("API key policy is invalid")
var errAPIKeyAuditOwnerMissing = errors.New("API key audit owner no longer exists")

const MaxUserAPIKeyAuditLogs = 10000

type APIKeyRateLimitError struct {
	RetryAfter int
}

func (e *APIKeyRateLimitError) Error() string { return "API key rate limit reached" }

func ListUserAPIKeys(userID string) ([]model.UserAPIKey, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	keys := []model.UserAPIKey{}
	err = db.Where("user_id = ?", userID).Order("created_at desc, id desc").Find(&keys).Error
	return keys, err
}

func CreateUserAPIKey(key model.UserAPIKey, limit int) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return db.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		// A write to the owner row serializes count + insert across processes and
		// all supported databases, including MySQL's default REPEATABLE READ.
		if err := tx.Model(&model.User{}).Where("id = ?", key.UserID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		var owner model.User
		if err := tx.Select("id").First(&owner, "id = ?", key.UserID).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.UserAPIKey{}).Where("user_id = ?", key.UserID).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit) {
			return ErrAPIKeyLimit
		}
		return tx.Create(&key).Error
	})
}

func GetUserAPIKeyByHash(hash string) (model.UserAPIKey, bool, error) {
	db, err := DB()
	if err != nil {
		return model.UserAPIKey{}, false, err
	}
	var key model.UserAPIKey
	err = db.Where("key_hash = ?", hash).First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.UserAPIKey{}, false, nil
	}
	return key, err == nil, err
}

func GetUserAPIKey(userID string, id string) (model.UserAPIKey, bool, error) {
	db, err := DB()
	if err != nil {
		return model.UserAPIKey{}, false, err
	}
	var key model.UserAPIKey
	err = db.Where("user_id = ? AND id = ?", userID, id).First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.UserAPIKey{}, false, nil
	}
	return key, err == nil, err
}

func ConsumeUserAPIKeyRequest(id string) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// Lock before reading so quota checks and increments share one snapshot.
		if err := tx.Model(&model.UserAPIKey{}).Where("id = ?", id).UpdateColumn("request_count", gorm.Expr("request_count")).Error; err != nil {
			return err
		}
		var key model.UserAPIKey
		if err := tx.First(&key, "id = ?", id).Error; err != nil {
			return err
		}
		usedAt := time.Now().UTC()
		if key.ExpiresAt != nil && !key.ExpiresAt.After(usedAt) {
			return ErrAPIKeyExpired
		}
		if len(key.Scopes) == 0 || key.RateLimit < 1 || key.RateLimit > 6000 {
			return ErrAPIKeyPolicy
		}
		if key.RequestLimit != nil && key.RequestCount >= *key.RequestLimit {
			return ErrAPIKeyRequestLimit
		}
		if key.RateWindowStartedAt == nil || !usedAt.Before(key.RateWindowStartedAt.Add(time.Minute)) {
			key.RateWindowStartedAt = &usedAt
			key.RateWindowCount = 0
		}
		if key.RateWindowCount >= key.RateLimit {
			retry := int((key.RateWindowStartedAt.Add(time.Minute).Sub(usedAt) + time.Second - 1) / time.Second)
			return &APIKeyRateLimitError{RetryAfter: retry}
		}
		return tx.Model(&model.UserAPIKey{}).Where("id = ?", id).Updates(map[string]any{
			"request_count":          key.RequestCount + 1,
			"rate_window_started_at": key.RateWindowStartedAt,
			"rate_window_count":      key.RateWindowCount + 1,
			"last_used_at":           usedAt,
		}).Error
	})
}

func DeleteUserAPIKey(userID string, id string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Where("user_id = ? AND id = ?", userID, id).Delete(&model.UserAPIKey{})
	return result.RowsAffected > 0, result.Error
}

func SaveUserAPIKeyAuditLog(log model.UserAPIKeyAuditLog) error {
	err := SaveRequiredUserAPIKeyAuditLog(log)
	if errors.Is(err, errAPIKeyAuditOwnerMissing) {
		return nil
	}
	return err
}

// Sensitive reads must not proceed when their audit record cannot be stored.
func SaveRequiredUserAPIKeyAuditLog(log model.UserAPIKeyAuditLog) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return saveUserAPIKeyAuditLog(db, log, MaxUserAPIKeyAuditLogs)
}

func saveUserAPIKeyAuditLog(db *gorm.DB, log model.UserAPIKeyAuditLog, limit int) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// The owner lock serializes retention with other requests and user deletion.
		if err := tx.Model(&model.User{}).Where("id = ?", log.UserID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		var owner model.User
		if err := tx.Select("id").First(&owner, "id = ?", log.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errAPIKeyAuditOwnerMissing
			}
			return err
		}
		if err := tx.Create(&log).Error; err != nil {
			return err
		}
		var oldIDs []string
		// MySQL requires a LIMIT alongside OFFSET, even when all excess rows are wanted.
		if err := tx.Model(&model.UserAPIKeyAuditLog{}).Where("user_id = ?", log.UserID).Order("created_at desc, id desc").Offset(limit).Limit(math.MaxInt).Pluck("id", &oldIDs).Error; err != nil {
			return err
		}
		if len(oldIDs) == 0 {
			return nil
		}
		return tx.Where("user_id = ? AND id IN ?", log.UserID, oldIDs).Delete(&model.UserAPIKeyAuditLog{}).Error
	})
}

func ListUserAPIKeyAuditLogs(userID string, keyID string, limit int) ([]model.UserAPIKeyAuditLog, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}
	logs := []model.UserAPIKeyAuditLog{}
	err = db.Where("user_id = ? AND key_id = ?", userID, keyID).Order("created_at desc, id desc").Limit(limit).Find(&logs).Error
	return logs, err
}
