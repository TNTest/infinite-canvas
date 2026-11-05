package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tigerowo/infinite-canvas/service"
)

func UserAPIKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	keys, err := service.ListCurrentUserAPIKeys(r.Context())
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, keys)
}

func CreateUserAPIKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		Fail(w, "API Key 参数无效")
		return
	}
	key, err := service.CreateCurrentUserAPIKey(r.Context(), request.Name, request.ExpiresAt)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, key)
}

func DeleteUserAPIKey(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	if err := service.DeleteCurrentUserAPIKey(r.Context(), id); err != nil {
		FailError(w, err)
		return
	}
	OK(w, true)
}
