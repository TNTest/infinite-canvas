package repository

import (
	"errors"
	"strings"

	"github.com/tigerowo/infinite-canvas/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrCanvasProjectConflict = errors.New("画布版本已更新，请重新读取后再保存")

func GetUserCanvasProject(userID, id string) (model.CanvasProject, error) {
	var project model.CanvasProject
	db, err := DB()
	if err != nil {
		return project, err
	}
	err = db.Where("user_id = ? AND id = ? AND deleted_at = ''", userID, id).Take(&project).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, nil
	}
	return project, err
}

func ListUserCanvasProjects(userID string) ([]model.CanvasProject, error) {
	db, err := DB()
	if err != nil {
		return nil, err
	}

	var projects []model.CanvasProject
	err = db.Where(
		"user_id = ? AND deleted_at = ''",
		strings.TrimSpace(userID),
	).Order("updated_at DESC").Find(&projects).Error
	return projects, err
}

func SaveUserCanvasProject(
	project model.CanvasProject,
	expectedUpdatedAt string,
) (model.CanvasProject, error) {
	db, err := DB()
	if err != nil {
		return project, err
	}

	project.UserID = strings.TrimSpace(project.UserID)
	project.ID = strings.TrimSpace(project.ID)

	var result *gorm.DB
	if expectedUpdatedAt == "" {
		result = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&project)
	} else {
		result = db.Model(&model.CanvasProject{}).
			Where("user_id = ? AND id = ? AND deleted_at = '' AND updated_at = ?",
				project.UserID, project.ID, expectedUpdatedAt).
			Updates(map[string]any{"project_data": project.ProjectData, "updated_at": project.UpdatedAt})
	}
	if result.Error != nil {
		return project, result.Error
	}
	if result.RowsAffected == 0 {
		return project, ErrCanvasProjectConflict
	}
	return project, nil
}

func SaveUserCanvasProjects(
	userID string,
	projects []model.CanvasProject,
) ([]model.CanvasProject, error) {
	for _, project := range projects {
		project.UserID = userID
		if _, err := SaveUserCanvasProject(project, ""); err != nil && !errors.Is(err, ErrCanvasProjectConflict) {
			return nil, err
		}
	}
	return ListUserCanvasProjects(userID)
}

func SoftDeleteUserCanvasProjects(
	userID string,
	ids []string,
	deletedAt string,
) error {
	ids = uniqueTrimmedValues(ids...)
	if len(ids) == 0 {
		return nil
	}

	db, err := DB()
	if err != nil {
		return err
	}

	records := make([]model.CanvasProject, 0, len(ids))
	for _, id := range ids {
		records = append(records, model.CanvasProject{
			UserID:    strings.TrimSpace(userID),
			ID:        id,
			CreatedAt: deletedAt,
			UpdatedAt: deletedAt,
			DeletedAt: deletedAt,
		})
	}

	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"project_data",
			"updated_at",
			"deleted_at",
		}),
	}).Create(&records).Error
}

func CleanupDeletedCanvasProjects(before string) error {
	db, err := DB()
	if err != nil {
		return err
	}

	return db.Where(
		"deleted_at <> '' AND deleted_at < ?",
		before,
	).Delete(&model.CanvasProject{}).Error
}
