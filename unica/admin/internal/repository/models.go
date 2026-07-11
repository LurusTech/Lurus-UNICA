package repository

import "time"

// User represents a user in the system.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // Never expose in JSON
	DisplayName  string    `json:"display_name"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// RoleModel represents a role record.
type RoleModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// UserRole represents a user-role-product-line assignment.
type UserRole struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	RoleID        string    `json:"role_id"`
	RoleName      string    `json:"role_name"`
	ProductLineID *string   `json:"product_line_id,omitempty"` // nil for global roles
	CreatedAt     time.Time `json:"created_at"`
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

// UserWithRoles combines user info with their role assignments.
type UserWithRoles struct {
	User  User       `json:"user"`
	Roles []UserRole `json:"roles"`
}

// AIAgentConfig holds per-product-line AI agent settings.
type AIAgentConfig struct {
	ProductLineID       string    `json:"product_line_id"`
	ConfidenceThreshold float64   `json:"confidence_threshold"`
	HandoffKeywords     []string  `json:"handoff_keywords"`
	BlockedTopics       []string  `json:"blocked_topics"`
	MaxAITurns          int       `json:"max_ai_turns"`
	UpdatedAt           time.Time `json:"updated_at"`
	UpdatedBy           *string   `json:"updated_by,omitempty"`
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
