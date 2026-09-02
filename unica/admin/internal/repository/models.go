package repository

import "time"

// User represents a user in the system. Role and ProductLineID are the whole
// authorization model: an admin carries no product line, a user carries exactly
// one and may act only on it.
type User struct {
	ID             string    `json:"id"`
	Email          string    `json:"email"`
	PasswordHash   string    `json:"-"` // Never expose in JSON
	DisplayName    string    `json:"display_name"`
	Role           string    `json:"role"`
	ProductLineID  *string   `json:"product_line_id,omitempty"`
	ChatwootUserID *int      `json:"chatwoot_user_id,omitempty"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ProductLine represents a product line record.
type ProductLine struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	DisplayName       string    `json:"display_name"`
	ChatwootAccountID *int      `json:"chatwoot_account_id,omitempty"`
	DifyAgentID       *string   `json:"dify_agent_id,omitempty"`
	DifyDatasetID     *string   `json:"dify_dataset_id,omitempty"`
	HasDifyBinding    bool      `json:"has_dify_binding"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ChannelConfig represents a channel configuration record.
type ChannelConfig struct {
	ID                   string     `json:"id"`
	ProductLineID        string     `json:"product_line_id"`
	Platform             string     `json:"platform"`
	DisplayName          string     `json:"display_name"`
	AppID                string     `json:"app_id"`
	AppSecretEncrypted   []byte     `json:"-"`
	ExtraConfigEncrypted []byte     `json:"-"`
	WebhookToken         *string    `json:"webhook_token,omitempty"`
	IsEnabled            bool       `json:"is_enabled"`
	IsVerified           bool       `json:"is_verified"`
	LastTestAt           *time.Time `json:"last_test_at,omitempty"`
	LastTestResult       *string    `json:"last_test_result,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// ModelVersion is one stored revision of the answering model's configuration,
// as it lives in model_versions (021).
//
// ProductLineID is the row's scope rather than an optional attribute of it: nil
// is the platform default that every line answers with, and a value is that one
// line's deliberate override. The database column is nullable for the same
// reason, and every query in model_version.go folds the two cases together with
// COALESCE — see the migration for why a plain UNIQUE would not have held.
//
// The parameters are spread across fields rather than embedding difyapp.
// ModelSpec so that this file keeps describing rows the way the rest of it
// does, one field per column; ModelVersion.Spec converts to the spec type the
// bridge and the validator take.
type ModelVersion struct {
	ID            int64      `json:"id"`
	ProductLineID *string    `json:"product_line_id,omitempty"`
	Version       int        `json:"version"`
	Provider      string     `json:"provider"`
	Name          string     `json:"name"`
	Mode          string     `json:"mode"`
	Temperature   float64    `json:"temperature"`
	MaxTokens     int        `json:"max_tokens"`
	Active        bool       `json:"active"`
	// PushedAt is when this revision reached Dify, and nil when it has not. A
	// nil PushedAt on the active revision is the "versioned, not yet in effect"
	// state: the save is safe here while customers are still being answered by
	// the model the previous revision named.
	PushedAt  *time.Time `json:"pushed_at,omitempty"`
	Source    string     `json:"source"`
	Note      string     `json:"note,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}
