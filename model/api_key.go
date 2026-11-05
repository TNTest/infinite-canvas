package model

import "time"

// UserAPIKey authenticates by digest and stores an encrypted copy for its owner.
type UserAPIKey struct {
	ID                  string     `json:"id" gorm:"primaryKey;size:191"`
	UserID              string     `json:"-" gorm:"size:191;index"`
	Name                string     `json:"name" gorm:"size:100"`
	Prefix              string     `json:"prefix" gorm:"size:16"`
	KeyHash             string     `json:"-" gorm:"size:64;uniqueIndex"`
	EncryptedKey        string     `json:"-" gorm:"type:text"`
	CanCopy             bool       `json:"canCopy" gorm:"-"`
	ExpiresAt           *time.Time `json:"expiresAt"`
	LastUsedAt          *time.Time `json:"lastUsedAt"`
	CreatedAt           string     `json:"createdAt" gorm:"size:64"`
	Scopes              []string   `json:"scopes" gorm:"serializer:json;type:text"`
	RateLimit           int        `json:"rateLimit"`
	RequestLimit        *int64     `json:"requestLimit"`
	RequestCount        int64      `json:"requestCount"`
	RateWindowStartedAt *time.Time `json:"-"`
	RateWindowCount     int        `json:"-"`
}

type UserAPIKeyAuditLog struct {
	ID         string `json:"id" gorm:"primaryKey;size:191"`
	KeyID      string `json:"keyId" gorm:"size:191;index"`
	UserID     string `json:"userId" gorm:"size:191;index:idx_api_key_audit_user_created,priority:1"`
	Method     string `json:"method" gorm:"size:16"`
	Path       string `json:"path" gorm:"size:512"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"durationMs"`
	CreatedAt  string `json:"createdAt" gorm:"size:64;index:idx_api_key_audit_user_created,priority:2"`
}

type UserAPIKeyList struct {
	Items []UserAPIKey `json:"items"`
	Total int          `json:"total"`
}

type CreatedUserAPIKey struct {
	APIKey UserAPIKey `json:"apiKey"`
	Key    string     `json:"key"`
}
