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
