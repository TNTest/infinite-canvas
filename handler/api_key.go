package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/tigerowo/infinite-canvas/service"
)

func UserAPIKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	keys, err := service.ListCurrentUserAPIKeys(r.Context())
	if err != nil {
		failAPIKeyError(w, err)
		return
	}
	OK(w, keys)
}

func CreateUserAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	var request struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expiresAt"`
		service.APIKeyOptions
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		Fail(w, "API Key 参数无效")
		return
	}
	key, err := service.CreateCurrentUserAPIKey(r.Context(), request.Name, request.ExpiresAt, request.APIKeyOptions)
	if err != nil {
		failAPIKeyError(w, err)
		return
	}
	OK(w, key)
}

func RevealUserAPIKey(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	key, err := service.RevealCurrentUserAPIKey(r.Context(), id)
	if err != nil {
		failAPIKeyError(w, err)
		return
	}
	OK(w, map[string]string{"key": key})
}

func DeleteUserAPIKey(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := service.DeleteCurrentUserAPIKey(r.Context(), id); err != nil {
		failAPIKeyError(w, err)
		return
	}
	OK(w, true)
}

func UserAPIKeyAuditLogs(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	logs, err := service.ListCurrentUserAPIKeyAuditLogs(r.Context(), id)
	if err != nil {
		failAPIKeyError(w, err)
		return
	}
	OK(w, map[string]any{"items": logs, "total": len(logs)})
}

func failAPIKeyError(w http.ResponseWriter, err error) {
	var access *service.AccessError
	if errors.As(err, &access) {
		FailWithStatus(w, access.Status, access.Message)
		return
	}
	FailError(w, err)
}
