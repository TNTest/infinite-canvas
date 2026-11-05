package model

import "time"

// UserAPIKey stores only a digest; the original key is returned once on creation.
type UserAPIKey struct {
	ID         string     `json:"id" gorm:"primaryKey;size:191"`
	UserID     string     `json:"-" gorm:"size:191;index"`
	Name       string     `json:"name" gorm:"size:100"`
	Prefix     string     `json:"prefix" gorm:"size:16"`
	KeyHash    string     `json:"-" gorm:"size:64;uniqueIndex"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  string     `json:"createdAt" gorm:"size:64"`
}

type UserAPIKeyList struct {
	Items []UserAPIKey `json:"items"`
	Total int          `json:"total"`
}

type CreatedUserAPIKey struct {
	APIKey UserAPIKey `json:"apiKey"`
	Key    string     `json:"key"`
}
