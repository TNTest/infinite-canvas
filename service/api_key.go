package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

const MaxUserAPIKeys = 100

func ListCurrentUserAPIKeys(ctx context.Context) (model.UserAPIKeyList, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" || user.Role == model.UserRoleGuest {
		return model.UserAPIKeyList{}, safeMessageError{message: "请先登录"}
	}
	keys, err := repository.ListUserAPIKeys(user.ID)
	return model.UserAPIKeyList{Items: keys, Total: len(keys)}, err
}

func CreateCurrentUserAPIKey(ctx context.Context, name string, expiresAt *time.Time) (model.CreatedUserAPIKey, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" || user.Role == model.UserRoleGuest {
		return model.CreatedUserAPIKey{}, safeMessageError{message: "请先登录"}
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return model.CreatedUserAPIKey{}, safeMessageError{message: "名称须为 1 至 100 个字符"}
	}
	if expiresAt != nil {
		value := expiresAt.UTC()
		if !value.After(time.Now()) {
			return model.CreatedUserAPIKey{}, safeMessageError{message: "失效时间必须晚于当前时间"}
		}
		expiresAt = &value
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return model.CreatedUserAPIKey{}, err
	}
	key := "ic_" + base64.RawURLEncoding.EncodeToString(secret)
	metadata := model.UserAPIKey{
		ID: newID("key"), UserID: user.ID, Name: name, Prefix: key[:11],
		KeyHash: apiKeyHash(key), ExpiresAt: expiresAt, CreatedAt: now(),
	}
	if err := repository.CreateUserAPIKey(metadata, MaxUserAPIKeys); err != nil {
		if errors.Is(err, repository.ErrAPIKeyLimit) {
			return model.CreatedUserAPIKey{}, safeMessageError{message: "最多可保留 100 个 API Key，请先撤销不再使用的 Key"}
		}
		return model.CreatedUserAPIKey{}, err
	}
	return model.CreatedUserAPIKey{APIKey: metadata, Key: key}, nil
}

func DeleteCurrentUserAPIKey(ctx context.Context, id string) error {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" || user.Role == model.UserRoleGuest {
		return safeMessageError{message: "请先登录"}
	}
	deleted, err := repository.DeleteUserAPIKey(user.ID, id)
	if err != nil {
		return err
	}
	if !deleted {
		return safeMessageError{message: "API Key 不存在"}
	}
	return nil
}

func currentAPIKeyUser(token string) (model.AuthUser, bool) {
	if len(token) != 46 {
		return model.AuthUser{}, false
	}
	key, found, err := repository.GetUserAPIKeyByHash(apiKeyHash(token))
	usedAt := time.Now().UTC()
	if err != nil || !found || (key.ExpiresAt != nil && !key.ExpiresAt.After(usedAt)) {
		return model.AuthUser{}, false
	}
	user, found, err := repository.GetUserByID(key.UserID)
	if err != nil || !found || user.Status == model.UserStatusBan {
		return model.AuthUser{}, false
	}
	if err := repository.TouchUserAPIKey(key.ID, usedAt); err != nil {
		return model.AuthUser{}, false
	}
	return model.PublicUser(user), true
}

func apiKeyHash(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}
