package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

type canvasProjectMetadata struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

var ErrCanvasProjectConflict = repository.ErrCanvasProjectConflict

func canvasProjectFromRaw(
	userID string,
	raw json.RawMessage,
) (model.CanvasProject, error) {
	var metadata canvasProjectMetadata
	if len(raw) == 0 || json.Unmarshal(raw, &metadata) != nil {
		return model.CanvasProject{}, errors.New("画布项目数据无效")
	}

	metadata.ID = strings.TrimSpace(metadata.ID)
	metadata.CreatedAt = strings.TrimSpace(metadata.CreatedAt)
	metadata.UpdatedAt = strings.TrimSpace(metadata.UpdatedAt)
	if metadata.ID == "" || metadata.CreatedAt == "" ||
		metadata.UpdatedAt == "" {
		return model.CanvasProject{}, errors.New("画布项目数据无效")
	}

	return model.CanvasProject{
		UserID:      strings.TrimSpace(userID),
		ID:          metadata.ID,
		ProjectData: string(raw),
		CreatedAt:   metadata.CreatedAt,
		UpdatedAt:   metadata.UpdatedAt,
	}, nil
}

func canvasProjectData(
	projects []model.CanvasProject,
) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(projects))
	for _, project := range projects {
		if strings.TrimSpace(project.ProjectData) != "" {
			result = append(
				result,
				json.RawMessage(project.ProjectData),
			)
		}
	}
	return result
}

func CurrentUserCanvasProjects(
	ctx context.Context,
) ([]json.RawMessage, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" {
		return nil, errors.New("请先登录")
	}

	projects, err := repository.ListUserCanvasProjects(user.ID)
	if err != nil {
		return nil, err
	}
	return canvasProjectData(projects), nil
}

func SaveCurrentUserCanvasProject(
	ctx context.Context,
	raw json.RawMessage,
	expectedUpdatedAt string,
) (json.RawMessage, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" {
		return nil, errors.New("请先登录")
	}

	project, err := canvasProjectFromRaw(user.ID, raw)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if previous, err := time.Parse(time.RFC3339Nano, expectedUpdatedAt); err == nil && !now.After(previous) {
		now = previous.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	}
	project.UpdatedAt = now.Format("2006-01-02T15:04:05.000Z")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	fields["updatedAt"], _ = json.Marshal(project.UpdatedAt)
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	project.ProjectData = string(data)
	saved, err := repository.SaveUserCanvasProject(project, expectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(saved.ProjectData), nil
}

func CurrentUserCanvasProject(ctx context.Context, id, knownUpdatedAt string) (json.RawMessage, bool, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" {
		return nil, false, errors.New("请先登录")
	}
	project, err := repository.GetUserCanvasProject(user.ID, id)
	if err != nil || project.ID == "" {
		return nil, false, err
	}
	if project.UpdatedAt == knownUpdatedAt {
		return nil, true, nil
	}
	return json.RawMessage(project.ProjectData), true, nil
}

func SyncCurrentUserCanvasProjects(
	ctx context.Context,
	rawProjects []json.RawMessage,
) ([]json.RawMessage, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" {
		return nil, errors.New("请先登录")
	}

	projects := make([]model.CanvasProject, 0, len(rawProjects))
	for _, raw := range rawProjects {
		project, err := canvasProjectFromRaw(user.ID, raw)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}

	saved, err := repository.SaveUserCanvasProjects(user.ID, projects)
	if err != nil {
		return nil, err
	}
	return canvasProjectData(saved), nil
}

func DeleteCurrentUserCanvasProjects(
	ctx context.Context,
	projectIDs []string,
) error {
	user, ok := UserFromContext(ctx)
	if !ok || user.ID == "" {
		return errors.New("请先登录")
	}

	for _, projectID := range projectIDs {
		if strings.TrimSpace(projectID) != "" {
			return repository.SoftDeleteUserCanvasProjects(
				user.ID,
				projectIDs,
				time.Now().UTC().Format(time.RFC3339Nano),
			)
		}
	}
	return errors.New("画布项目参数无效")
}
