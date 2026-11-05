package repository

import (
	"errors"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
)

var ErrAPIKeyLimit = errors.New("API key limit reached")

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
	return db.Transaction(func(tx *gorm.DB) error {
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

func TouchUserAPIKey(id string, usedAt time.Time) error {
	db, err := DB()
	if err != nil {
		return err
	}
	return db.Model(&model.UserAPIKey{}).Where("id = ?", id).UpdateColumn("last_used_at", usedAt).Error
}

func DeleteUserAPIKey(userID string, id string) (bool, error) {
	db, err := DB()
	if err != nil {
		return false, err
	}
	result := db.Where("user_id = ? AND id = ?", userID, id).Delete(&model.UserAPIKey{})
	return result.RowsAffected > 0, result.Error
}
