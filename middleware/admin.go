package middleware

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tigerowo/infinite-canvas/handler"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/service"
)

func AdminAuth(c *gin.Context) { authorize(c, false, true) }

func UserAuth(c *gin.Context) { authorize(c, false, false) }

func OptionalAuth(c *gin.Context) { authorize(c, true, false) }

func authorize(c *gin.Context, optional, admin bool) {
	started := time.Now()
	header, supplied := c.Request.Header["Authorization"]
	if !supplied {
		if optional {
			c.Next()
			return
		}
		denyAccess(c, http.StatusUnauthorized, "请先登录", 0)
		return
	}
	if len(header) != 1 {
		denyAccess(c, http.StatusUnauthorized, "访问凭证无效", 0)
		return
	}
	parts := strings.Fields(header[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		denyAccess(c, http.StatusUnauthorized, "访问凭证无效", 0)
		return
	}
	user, key, err := service.AuthenticateAccessToken(parts[1])
	if key != nil {
		defer auditAPIKeyRequest(c, *key, started)
	}
	if err != nil {
		denyAccessError(c, err)
		return
	}
	if key != nil {
		// Count authenticated attempts before checking route permissions so denied
		// requests cannot bypass the key's rate and cumulative request limits.
		if err := service.ConsumeAPIKeyRequest(key.ID); err != nil {
			denyAccessError(c, err)
			return
		}
		scopes, allowed := apiKeyRouteScopes[c.Request.Method+" "+c.FullPath()]
		if admin || !allowed {
			denyAccess(c, http.StatusForbidden, "API Key 无权访问此接口", 0)
			return
		}
		for _, scope := range scopes {
			if !slices.Contains(key.Scopes, scope) {
				denyAccess(c, http.StatusForbidden, "API Key 无权访问此接口", 0)
				return
			}
		}
		c.Request = c.Request.WithContext(service.WithAPIKey(c.Request.Context(), key.ID))
	}
	if (admin && user.Role != model.UserRoleAdmin) || (!optional && user.Role == model.UserRoleGuest) {
		denyAccess(c, http.StatusForbidden, "权限不足", 0)
		return
	}
	c.Request = c.Request.WithContext(service.WithUser(c.Request.Context(), user))
	c.Next()
}

func denyAccessError(c *gin.Context, err error) {
	var accessError *service.AccessError
	if errors.As(err, &accessError) {
		denyAccess(c, accessError.Status, accessError.Message, accessError.RetryAfter)
		return
	}
	log.Printf("access authentication failed: %v", err)
	denyAccess(c, http.StatusServiceUnavailable, "鉴权服务暂不可用", 0)
}

func denyAccess(c *gin.Context, status int, message string, retryAfter int) {
	if retryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
	}
	handler.FailWithStatus(c.Writer, status, message)
	c.Abort()
}

func auditAPIKeyRequest(c *gin.Context, key model.UserAPIKey, started time.Time) {
	recovered := recover()
	status := c.Writer.Status()
	if recovered != nil {
		status = http.StatusInternalServerError
	}
	route := c.FullPath()
	entry := model.UserAPIKeyAuditLog{
		ID: uuid.NewString(), KeyID: key.ID, UserID: key.UserID,
		Method: c.Request.Method, Path: route, Status: status,
		DurationMs: time.Since(started).Milliseconds(), CreatedAt: started.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	if err := repository.SaveUserAPIKeyAuditLog(entry); err != nil {
		log.Printf("API key audit write failed: key_id=%s method=%s route=%s err=%v", key.ID, entry.Method, route, err)
	}
	if recovered != nil {
		panic(recovered)
	}
}

func NotFoundJSON(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"code": 1, "data": nil, "msg": "接口不存在"})
}
