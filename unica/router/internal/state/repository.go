package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// Repository provides PostgreSQL CRUD operations for customers, conversations, and messages.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new Repository backed by the given database connection.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Customer represents a row in the customers table.
type Customer struct {
	ID               string    `json:"id"`
	PlatformIdentity string    `json:"platform_identity"`
	ChannelID        string    `json:"channel_id"`
	DisplayName      string    `json:"display_name"`
	Tags             []string  `json:"tags"`
	Notes            string    `json:"notes"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
}

// Conversation represents a row in the conversations table.
type Conversation struct {
	ID              string            `json:"id"`
	ChannelID       string            `json:"channel_id"`
	ProductLineID   string            `json:"product_line_id"`
	CustomerID      string            `json:"customer_id"`
	State           ConversationState `json:"state"`
	AIConfidence    *float32          `json:"ai_confidence"`
	AssignedAgentID *string           `json:"assigned_agent_id"`
	IntentSummary   *string           `json:"intent_summary"`
	Metadata        json.RawMessage   `json:"metadata"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ClosedAt        *time.Time        `json:"closed_at"`
}

// Message represents a row in the messages table.
type Message struct {
	ID              string          `json:"id"`
	ConversationID  string          `json:"conversation_id"`
	Direction       string          `json:"direction"`
	SenderType      string          `json:"sender_type"`
	ContentJSON     json.RawMessage `json:"content_json"`
	PlatformMsgID   *string         `json:"platform_msg_id"`
	ConfidenceScore *float32        `json:"confidence_score"`
	CorrelationID   *string         `json:"correlation_id"`
	CreatedAt       time.Time       `json:"created_at"`
}

// CreateCustomer inserts a new customer and returns the generated ID.
func (r *Repository) CreateCustomer(ctx context.Context, c *Customer) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name, tags, notes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		c.PlatformIdentity, c.ChannelID, c.DisplayName, tagsToJSON(c.Tags), c.Notes,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create customer: %w", err)
	}
	return id, nil
}

// UpsertCustomer inserts or updates a customer by platform_identity + channel_id.
// On conflict, it updates display_name and last_seen_at. Returns the customer ID.
func (r *Repository) UpsertCustomer(ctx context.Context, c *Customer) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name, tags, notes)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (platform_identity, channel_id) DO UPDATE
		 SET display_name = EXCLUDED.display_name,
		     last_seen_at = NOW()
		 RETURNING id`,
		c.PlatformIdentity, c.ChannelID, c.DisplayName, tagsToJSON(c.Tags), c.Notes,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert customer: %w", err)
	}
	return id, nil
}

// CreateConversation inserts a new conversation and returns the generated ID.
func (r *Repository) CreateConversation(ctx context.Context, conv *Conversation) (string, error) {
	var id string
	metadata := conv.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state, metadata)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		conv.ChannelID, conv.ProductLineID, conv.CustomerID, string(conv.State), metadata,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}
	return id, nil
}

// GetConversation retrieves a conversation by its ID.
func (r *Repository) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	conv := &Conversation{}
	var state string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, channel_id, product_line_id, customer_id, state,
		        ai_confidence, assigned_agent_id, intent_summary, metadata,
		        created_at, updated_at, closed_at
		 FROM conversations WHERE id = $1`, id,
	).Scan(
		&conv.ID, &conv.ChannelID, &conv.ProductLineID, &conv.CustomerID, &state,
		&conv.AIConfidence, &conv.AssignedAgentID, &conv.IntentSummary, &conv.Metadata,
		&conv.CreatedAt, &conv.UpdatedAt, &conv.ClosedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	conv.State = ConversationState(state)
	return conv, nil
}

// GetConversationByCustomer returns the most recent active (non-closed) conversation for a customer.
func (r *Repository) GetConversationByCustomer(ctx context.Context, customerID string) (*Conversation, error) {
	conv := &Conversation{}
	var state string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, channel_id, product_line_id, customer_id, state,
		        ai_confidence, assigned_agent_id, intent_summary, metadata,
		        created_at, updated_at, closed_at
		 FROM conversations
		 WHERE customer_id = $1 AND state != 'closed'
		 ORDER BY created_at DESC
		 LIMIT 1`, customerID,
	).Scan(
		&conv.ID, &conv.ChannelID, &conv.ProductLineID, &conv.CustomerID, &state,
		&conv.AIConfidence, &conv.AssignedAgentID, &conv.IntentSummary, &conv.Metadata,
		&conv.CreatedAt, &conv.UpdatedAt, &conv.ClosedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation by customer: %w", err)
	}
	conv.State = ConversationState(state)
	return conv, nil
}

// UpdateConversationState updates the state of a conversation and sets updated_at.
// If the new state is "closed", it also sets closed_at.
func (r *Repository) UpdateConversationState(ctx context.Context, id string, newState ConversationState) error {
	var query string
	if newState == StateClosed {
		query = `UPDATE conversations SET state = $1, updated_at = NOW(), closed_at = NOW() WHERE id = $2`
	} else {
		query = `UPDATE conversations SET state = $1, updated_at = NOW(), closed_at = NULL WHERE id = $2`
	}
	result, err := r.db.ExecContext(ctx, query, string(newState), id)
	if err != nil {
		return fmt.Errorf("update conversation state: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update conversation state (rows affected): %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("conversation not found: %s", id)
	}
	return nil
}

// CreateMessage inserts a new message record and returns its generated ID.
func (r *Repository) CreateMessage(ctx context.Context, msg *Message) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO messages (conversation_id, direction, sender_type, content_json, platform_msg_id, confidence_score, correlation_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		msg.ConversationID, msg.Direction, msg.SenderType, msg.ContentJSON,
		msg.PlatformMsgID, msg.ConfidenceScore, msg.CorrelationID,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create message: %w", err)
	}
	return id, nil
}

// GetMessages retrieves messages for a conversation ordered by creation time.
func (r *Repository) GetMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, conversation_id, direction, sender_type, content_json,
		        platform_msg_id, confidence_score, correlation_id, created_at
		 FROM messages
		 WHERE conversation_id = $1
		 ORDER BY created_at ASC
		 LIMIT $2`, conversationID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.ConversationID, &m.Direction, &m.SenderType, &m.ContentJSON,
			&m.PlatformMsgID, &m.ConfidenceScore, &m.CorrelationID, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// UpdateIntentSummary sets the intent_summary field on a conversation.
func (r *Repository) UpdateIntentSummary(ctx context.Context, convID string, summary string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE conversations SET intent_summary = $1, updated_at = NOW() WHERE id = $2`,
		summary, convID,
	)
	if err != nil {
		return fmt.Errorf("update intent summary: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update intent summary (rows affected): %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("conversation not found: %s", convID)
	}
	return nil
}

// GetCustomer retrieves a customer by ID.
func (r *Repository) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	c := &Customer{}
	var tagsJSON []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT id, platform_identity, channel_id, display_name, tags, notes, first_seen_at, last_seen_at
		 FROM customers WHERE id = $1`, id,
	).Scan(&c.ID, &c.PlatformIdentity, &c.ChannelID, &c.DisplayName, &tagsJSON, &c.Notes, &c.FirstSeenAt, &c.LastSeenAt)
	if err != nil {
		return nil, fmt.Errorf("get customer: %w", err)
	}
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &c.Tags)
	}
	return c, nil
}

// CountConversations counts total conversations for a customer.
func (r *Repository) CountConversations(ctx context.Context, customerID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversations WHERE customer_id = $1`, customerID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count conversations: %w", err)
	}
	return count, nil
}

// GetMessagesAfterID retrieves messages for a conversation created after a given message ID, ordered by creation time.
func (r *Repository) GetMessagesAfterID(ctx context.Context, conversationID string, afterMessageID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	// Get the created_at of the watermark message to filter newer messages.
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, conversation_id, direction, sender_type, content_json,
		        platform_msg_id, confidence_score, correlation_id, created_at
		 FROM messages
		 WHERE conversation_id = $1
		   AND created_at > (SELECT created_at FROM messages WHERE id = $2)
		 ORDER BY created_at ASC
		 LIMIT $3`, conversationID, afterMessageID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages after id: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.ConversationID, &m.Direction, &m.SenderType, &m.ContentJSON,
			&m.PlatformMsgID, &m.ConfidenceScore, &m.CorrelationID, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// UpdateSurveyStatus updates survey-related fields on a conversation.
func (r *Repository) UpdateSurveyStatus(ctx context.Context, convID string, status string, score *int) error {
	var query string
	var args []interface{}

	if score != nil {
		query = `UPDATE conversations SET survey_status = $1, satisfaction_score = $2, updated_at = NOW() WHERE id = $3`
		args = []interface{}{status, *score, convID}
	} else if status == "sent" {
		query = `UPDATE conversations SET survey_status = $1, survey_sent_at = NOW(), updated_at = NOW() WHERE id = $2`
		args = []interface{}{status, convID}
	} else {
		query = `UPDATE conversations SET survey_status = $1, updated_at = NOW() WHERE id = $2`
		args = []interface{}{status, convID}
	}

	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update survey status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update survey status (rows affected): %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("conversation not found: %s", convID)
	}
	return nil
}

// CountCustomerMessages counts inbound messages from customer for a conversation.
func (r *Repository) CountCustomerMessages(ctx context.Context, convID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE conversation_id = $1 AND direction = 'inbound' AND sender_type = 'customer'`,
		convID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count customer messages: %w", err)
	}
	return count, nil
}

// UpdateConversationMetadata merges the given JSON object into the existing
// metadata JSONB column using PostgreSQL's || operator. This allows adding
// fields without overwriting existing ones.
func (r *Repository) UpdateConversationMetadata(ctx context.Context, convID string, metadataJSON json.RawMessage) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE conversations
		 SET metadata = COALESCE(metadata, '{}'::jsonb) || $1::jsonb,
		     updated_at = NOW()
		 WHERE id = $2`,
		string(metadataJSON), convID,
	)
	if err != nil {
		return fmt.Errorf("update conversation metadata: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update conversation metadata (rows affected): %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("conversation not found: %s", convID)
	}
	return nil
}

// tagsToJSON converts a string slice into a JSON byte representation.
func tagsToJSON(tags []string) []byte {
	if tags == nil {
		tags = []string{}
	}
	b, _ := json.Marshal(tags)
	return b
}
