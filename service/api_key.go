package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"gorm.io/gorm"
)

const MaxUserAPIKeys = 100

type AccessError struct {
	Status     int
	Message    string
	RetryAfter int
}

func (e *AccessError) Error() string       { return e.Message }
func (e *AccessError) SafeMessage() string { return e.Message }

type APIKeyOptions struct {
	Scopes       *[]string `json:"scopes"`
	RateLimit    *int      `json:"rateLimit"`
	RequestLimit *int64    `json:"requestLimit"`
}

func sessionAPIKeyOwner(ctx context.Context) (model.AuthUser, error) {
	if _, ok := APIKeyIDFromContext(ctx); ok {
		return model.AuthUser{}, &AccessError{Status: http.StatusForbidden, Message: "API Key 不允许管理密钥，请使用登录会话"}
	}
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" || user.Role == model.UserRoleGuest {
		return model.AuthUser{}, &AccessError{Status: http.StatusUnauthorized, Message: "请先登录"}
	}
	return user, nil
}

func ListCurrentUserAPIKeys(ctx context.Context) (model.UserAPIKeyList, error) {
	user, err := sessionAPIKeyOwner(ctx)
	if err != nil {
		return model.UserAPIKeyList{}, err
	}
	keys, err := repository.ListUserAPIKeys(user.ID)
	for i := range keys {
		if keys[i].Scopes == nil {
			keys[i].Scopes = []string{}
		}
	}
	return model.UserAPIKeyList{Items: keys, Total: len(keys)}, err
}

func CreateCurrentUserAPIKey(ctx context.Context, name string, expiresAt *time.Time, options ...APIKeyOptions) (model.CreatedUserAPIKey, error) {
	user, err := sessionAPIKeyOwner(ctx)
	if err != nil {
		return model.CreatedUserAPIKey{}, err
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
	scopes := []string{"read", "generate"}
	rateLimit := 60
	var requestLimit *int64
	if len(options) > 0 {
		option := options[0]
		if option.Scopes != nil {
			scopes = append([]string{}, (*option.Scopes)...)
		}
		if option.RateLimit != nil {
			rateLimit = *option.RateLimit
		}
		requestLimit = option.RequestLimit
	}
	if len(scopes) == 0 {
		return model.CreatedUserAPIKey{}, safeMessageError{message: "请至少选择一项 API Key 权限"}
	}
	for _, scope := range scopes {
		switch scope {
		case "read", "generate", "write", "delete":
		default:
			return model.CreatedUserAPIKey{}, safeMessageError{message: "API Key 权限无效"}
		}
	}
	if rateLimit < 1 || rateLimit > 6000 {
		return model.CreatedUserAPIKey{}, safeMessageError{message: "每分钟请求上限须为 1 至 6000"}
	}
	if requestLimit != nil && *requestLimit <= 0 {
		return model.CreatedUserAPIKey{}, safeMessageError{message: "累计请求额度须为正整数"}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return model.CreatedUserAPIKey{}, err
	}
	key := "ic_" + base64.RawURLEncoding.EncodeToString(secret)
	metadata := model.UserAPIKey{
		ID: newID("key"), UserID: user.ID, Name: name, Prefix: key[:11],
		KeyHash: apiKeyHash(key), ExpiresAt: expiresAt, CreatedAt: now(),
		Scopes: scopes, RateLimit: rateLimit, RequestLimit: requestLimit,
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
	user, err := sessionAPIKeyOwner(ctx)
	if err != nil {
		return err
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

func AuthenticateAccessToken(token string) (model.AuthUser, *model.UserAPIKey, error) {
	unauthorized := &AccessError{Status: http.StatusUnauthorized, Message: "登录凭证无效或已失效"}
	var key *model.UserAPIKey
	var userID string
	if strings.HasPrefix(token, "ic_") {
		if len(token) != 46 {
			return model.AuthUser{}, nil, unauthorized
		}
		value, found, err := repository.GetUserAPIKeyByHash(apiKeyHash(token))
		if err != nil {
			return model.AuthUser{}, nil, accessDatabaseError()
		}
		if !found {
			return model.AuthUser{}, nil, unauthorized
		}
		key = &value
		if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
			return model.AuthUser{}, key, unauthorized
		}
		userID = key.UserID
	} else {
		claims, err := ParseToken(token)
		if err != nil {
			return model.AuthUser{}, nil, unauthorized
		}
		userID = claims.UserID
	}
	user, found, err := repository.GetUserByID(userID)
	if err != nil {
		return model.AuthUser{}, key, accessDatabaseError()
	}
	if !found || user.Status == model.UserStatusBan || user.Role == model.UserRoleGuest {
		return model.AuthUser{}, key, unauthorized
	}
	return model.PublicUser(user), key, nil
}

func ConsumeAPIKeyRequest(keyID string) error {
	err := repository.ConsumeUserAPIKeyRequest(keyID)
	var limited *repository.APIKeyRateLimitError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, repository.ErrAPIKeyExpired):
		return &AccessError{Status: http.StatusUnauthorized, Message: "API Key 无效或已失效"}
	case errors.Is(err, repository.ErrAPIKeyRequestLimit):
		return &AccessError{Status: http.StatusForbidden, Message: "API Key 累计请求额度已用尽"}
	case errors.Is(err, repository.ErrAPIKeyPolicy):
		return &AccessError{Status: http.StatusForbidden, Message: "API Key 权限配置无效，请撤销后重新创建"}
	case errors.As(err, &limited):
		return &AccessError{Status: http.StatusTooManyRequests, Message: "API Key 请求过于频繁", RetryAfter: limited.RetryAfter}
	default:
		return accessDatabaseError()
	}
}

func accessDatabaseError() *AccessError {
	return &AccessError{Status: http.StatusServiceUnavailable, Message: "鉴权服务暂时不可用，请稍后重试"}
}

func ListCurrentUserAPIKeyAuditLogs(ctx context.Context, keyID string) ([]model.UserAPIKeyAuditLog, error) {
	user, err := sessionAPIKeyOwner(ctx)
	if err != nil {
		return nil, err
	}
	return repository.ListUserAPIKeyAuditLogs(user.ID, keyID, 100)
}

func apiKeyHash(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}
