package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tigerowo/infinite-canvas/service"
)

func ReadUserCanvasProject(w http.ResponseWriter, r *http.Request, id string) {
	project, found, err := service.CurrentUserCanvasProject(r.Context(), id, r.URL.Query().Get("knownUpdatedAt"))
	if err != nil {
		FailError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !found {
		FailWithStatus(w, http.StatusNotFound, "画布不存在")
		return
	}
	OK(w, project)
}

func UserCanvasProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := service.CurrentUserCanvasProjects(r.Context())
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, projects)
}

func SaveUserCanvasProject(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Data              json.RawMessage `json:"data"`
		ExpectedUpdatedAt string          `json:"expectedUpdatedAt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
		len(request.Data) == 0 {
		Fail(w, "数据内容不能为空")
		return
	}

	project, err := service.SaveCurrentUserCanvasProject(
		r.Context(),
		request.Data,
		request.ExpectedUpdatedAt,
	)
	if errors.Is(err, service.ErrCanvasProjectConflict) {
		FailWithStatus(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, project)
}

func SyncUserCanvasProjects(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Projects []json.RawMessage `json:"projects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		Fail(w, "画布项目参数无效")
		return
	}

	projects, err := service.SyncCurrentUserCanvasProjects(
		r.Context(),
		request.Projects,
	)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, projects)
}
