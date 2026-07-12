package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kefu/unica/pkg/model"
)

// ManagerConfig holds configuration for the state Manager.
type ManagerConfig struct {
	IdleTimeout   time.Duration // Duration after which idle conversations are auto-closed.
	CheckInterval time.Duration // How often to scan for idle conversations.
}

// DefaultManagerConfig returns a ManagerConfig with sensible defaults.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		IdleTimeout:   30 * time.Minute,
		CheckInterval: 1 * time.Minute,
	}
}

// OnCloseFunc is a callback invoked when a conversation transitions to closed state.
type OnCloseFunc func(ctx context.Context, conv *Conversation)

// Manager combines the repository, cache, and state machine to provide
// high-level conversation state management.
type Manager struct {
	repo      *Repository
	cache     *Cache
	config    ManagerConfig
	onCloseFn OnCloseFunc

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager creates a new state Manager.
func NewManager(repo *Repository, cache *Cache, config ManagerConfig) *Manager {
	return &Manager{
		repo:   repo,
		cache:  cache,
		config: config,
	}
}

// SetOnClose registers a callback that fires when a conversation transitions to closed.
func (m *Manager) SetOnClose(fn OnCloseFunc) {
	m.onCloseFn = fn
}

// Start launches background goroutines (idle timeout checker).
func (m *Manager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	go m.idleTimeoutLoop(ctx)
	log.Printf("[state] manager started (idle_timeout=%s, check_interval=%s)",
		m.config.IdleTimeout, m.config.CheckInterval)
}

// Stop gracefully shuts down the manager and waits for background goroutines.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	log.Printf("[state] manager stopped")
}

// HandleInboundMessage processes an inbound message: finds or creates the customer
// and conversation, stores the message, and returns the conversation ID.
func (m *Manager) HandleInboundMessage(ctx context.Context, msg *model.StandardMessage) (string, error) {
	if msg == nil {
		return "", fmt.Errorf("message must not be nil")
	}

	platformUserID := msg.Data.PlatformMeta.PlatformUserID
	channelID := msg.Data.ChannelID
	if platformUserID == "" || channelID == "" {
		return "", fmt.Errorf("platform_user_id and channel_id are required")
	}

	// Find or create customer
	customerID, err := m.findOrCreateCustomer(ctx, platformUserID, channelID, "")
	if err != nil {
		return "", fmt.Errorf("find/create customer: %w", err)
	}

	// Find active conversation or create a new one
	convID, err := m.findOrCreateConversation(ctx, customerID, channelID, msg.Data.ProductLineID)
	if err != nil {
		return "", fmt.Errorf("find/create conversation: %w", err)
	}

	// Store the message
	contentJSON, err := json.Marshal(msg.Data.Content)
	if err != nil {
		return "", fmt.Errorf("marshal content: %w", err)
	}

	dbMsg := &Message{
		ConversationID: convID,
		Direction:      "inbound",
		SenderType:     "customer",
		ContentJSON:    contentJSON,
		CorrelationID:  strPtr(msg.Data.CorrelationID),
	}
	if msg.Data.PlatformMsgID != "" {
		dbMsg.PlatformMsgID = strPtr(msg.Data.PlatformMsgID)
	}

	_, err = m.repo.CreateMessage(ctx, dbMsg)
	if err != nil {
		return "", fmt.Errorf("create message: %w", err)
	}

	log.Printf("[state] handled inbound message for conversation %s (customer=%s)", convID, customerID)
	return convID, nil
}

// TransitionState validates and performs a state transition on a conversation,
// updating both the database and the Redis cache.
func (m *Manager) TransitionState(ctx context.Context, convID string, newState ConversationState, actor string) error {
	// Get current state from cache first, fall back to DB
	currentState, productLineID, agentID, err := m.getConversationState(ctx, convID)
	if err != nil {
		return fmt.Errorf("get current state: %w", err)
	}

	// Validate the transition
	if err := ValidateTransition(currentState, newState); err != nil {
		return err
	}

	// Update database
	if err := m.repo.UpdateConversationState(ctx, convID, newState); err != nil {
		return fmt.Errorf("update db state: %w", err)
	}

	// Update cache
	if m.cache != nil {
		if newState == StateClosed {
			if err := m.cache.InvalidateSession(ctx, convID); err != nil {
				log.Printf("[state] warning: failed to invalidate session cache for %s: %v", convID, err)
			}
		} else {
			sessionData := &SessionData{
				State:         string(newState),
				ProductLineID: productLineID,
				AgentID:       agentID,
			}
			if err := m.cache.SetSession(ctx, convID, sessionData); err != nil {
				log.Printf("[state] warning: failed to update session cache for %s: %v", convID, err)
			}
		}
	}

	log.Printf("[state] transition conversation %s: %s -> %s (actor=%s)", convID, currentState, newState, actor)

	// Fire OnClose callback when transitioning to closed
	if newState == StateClosed && m.onCloseFn != nil {
		conv, err := m.repo.GetConversation(ctx, convID)
		if err != nil {
			log.Printf("[state] warning: failed to load conversation %s for OnClose callback: %v", convID, err)
		} else {
			m.onCloseFn(ctx, conv)
		}
	}

	return nil
}

// findOrCreateCustomer looks up a customer by platform identity, creating one if needed.
func (m *Manager) findOrCreateCustomer(ctx context.Context, platformIdentity, channelID, displayName string) (string, error) {
	// Check cache first
	if m.cache != nil {
		cachedID, err := m.cache.GetCustomerID(ctx, platformIdentity, channelID)
		if err != nil {
			log.Printf("[state] warning: customer cache lookup failed: %v", err)
		}
		if cachedID != "" {
			return cachedID, nil
		}
	}

	// Upsert in database
	customer := &Customer{
		PlatformIdentity: platformIdentity,
		ChannelID:        channelID,
		DisplayName:      displayName,
	}
	id, err := m.repo.UpsertCustomer(ctx, customer)
	if err != nil {
		return "", err
	}

	// Cache the result
	if m.cache != nil {
		if cacheErr := m.cache.SetCustomerID(ctx, platformIdentity, channelID, id); cacheErr != nil {
			log.Printf("[state] warning: failed to cache customer ID: %v", cacheErr)
		}
	}

	return id, nil
}

// findOrCreateConversation finds an active conversation for the customer or creates a new one.
func (m *Manager) findOrCreateConversation(ctx context.Context, customerID, channelID, productLineID string) (string, error) {
	// Try to find an existing active conversation
	conv, err := m.repo.GetConversationByCustomer(ctx, customerID)
	if err == nil && conv != nil {
		return conv.ID, nil
	}
	// Only "no rows" means the customer genuinely has no active conversation.
	// Any other error (timeout, connection drop) must abort rather than fall
	// through to create a second, competing conversation for the same customer.
	if err != nil && err.Error() != "get conversation by customer: "+sql.ErrNoRows.Error() {
		return "", fmt.Errorf("look up conversation: %w", err)
	}

	// Create a new conversation
	newConv := &Conversation{
		ChannelID:     channelID,
		ProductLineID: productLineID,
		CustomerID:    customerID,
		State:         StatePending,
	}
	convID, err := m.repo.CreateConversation(ctx, newConv)
	if err != nil {
		return "", err
	}

	// Cache the session
	if m.cache != nil {
		sessionData := &SessionData{
			State:         string(StatePending),
			ProductLineID: productLineID,
		}
		if cacheErr := m.cache.SetSession(ctx, convID, sessionData); cacheErr != nil {
			log.Printf("[state] warning: failed to cache new session: %v", cacheErr)
		}
	}

	log.Printf("[state] created new conversation %s for customer %s", convID, customerID)
	return convID, nil
}

// getConversationState retrieves the current state, product line, and agent from cache or DB.
func (m *Manager) getConversationState(ctx context.Context, convID string) (ConversationState, string, string, error) {
	// Try cache first
	if m.cache != nil {
		session, err := m.cache.GetSession(ctx, convID)
		if err != nil {
			log.Printf("[state] warning: session cache lookup failed for %s: %v", convID, err)
		}
		if session != nil {
			return ConversationState(session.State), session.ProductLineID, session.AgentID, nil
		}
	}

	// Fall back to DB
	if m.repo == nil {
		return "", "", "", fmt.Errorf("no cache or repository available for conversation %s", convID)
	}
	conv, err := m.repo.GetConversation(ctx, convID)
	if err != nil {
		return "", "", "", err
	}

	agentID := ""
	if conv.AssignedAgentID != nil {
		agentID = *conv.AssignedAgentID
	}
	return conv.State, conv.ProductLineID, agentID, nil
}

// idleTimeoutLoop periodically checks for idle conversations and closes them.
func (m *Manager) idleTimeoutLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[state] idle timeout checker stopped")
			return
		case <-ticker.C:
			m.closeIdleConversations(ctx)
		}
	}
}

// closeIdleConversations finds and closes conversations that have been idle beyond the timeout.
func (m *Manager) closeIdleConversations(ctx context.Context) {
	threshold := time.Now().Add(-m.config.IdleTimeout)
	query := `SELECT id, state FROM conversations
	          WHERE state NOT IN ('closed')
	          AND updated_at < $1`

	rows, err := m.repo.db.QueryContext(ctx, query, threshold)
	if err != nil {
		log.Printf("[state] error querying idle conversations: %v", err)
		return
	}
	defer rows.Close()

	var closed int
	for rows.Next() {
		var id, stateStr string
		if err := rows.Scan(&id, &stateStr); err != nil {
			log.Printf("[state] error scanning idle conversation: %v", err)
			continue
		}

		currentState := ConversationState(stateStr)
		// Only close if the transition is valid from the current state
		if !currentState.CanTransitionTo(StateClosed) {
			// For pending state, first transition to ai_processing, then close
			// Skip auto-close for states that can't directly transition to closed
			log.Printf("[state] skipping idle conversation %s in state %s (no direct close path)", id, currentState)
			continue
		}

		if err := m.repo.UpdateConversationState(ctx, id, StateClosed); err != nil {
			log.Printf("[state] error closing idle conversation %s: %v", id, err)
			continue
		}
		if m.cache != nil {
			if err := m.cache.InvalidateSession(ctx, id); err != nil {
				log.Printf("[state] warning: failed to invalidate cache for idle conversation %s: %v", id, err)
			}
		}
		closed++
	}

	if closed > 0 {
		log.Printf("[state] auto-closed %d idle conversations", closed)
	}
}

// GetConversation retrieves a conversation by ID, checking cache then DB.
func (m *Manager) GetConversation(ctx context.Context, convID string) (*Conversation, error) {
	return m.repo.GetConversation(ctx, convID)
}

// CreateMessage stores a message in the database and returns the generated ID.
func (m *Manager) CreateMessage(ctx context.Context, msg *Message) (string, error) {
	return m.repo.CreateMessage(ctx, msg)
}

// GetMessages retrieves messages for a conversation ordered by creation time.
func (m *Manager) GetMessages(ctx context.Context, convID string, limit int) ([]Message, error) {
	return m.repo.GetMessages(ctx, convID, limit)
}

// UpdateIntentSummary sets the intent_summary field on a conversation.
func (m *Manager) UpdateIntentSummary(ctx context.Context, convID string, summary string) error {
	return m.repo.UpdateIntentSummary(ctx, convID, summary)
}

// GetCustomer retrieves a customer by ID.
func (m *Manager) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	return m.repo.GetCustomer(ctx, id)
}

// CountConversations counts total conversations for a customer.
func (m *Manager) CountConversations(ctx context.Context, customerID string) (int, error) {
	return m.repo.CountConversations(ctx, customerID)
}

// GetMessagesAfterID retrieves messages created after a given message ID for a conversation.
func (m *Manager) GetMessagesAfterID(ctx context.Context, conversationID string, afterMessageID string, limit int) ([]Message, error) {
	return m.repo.GetMessagesAfterID(ctx, conversationID, afterMessageID, limit)
}

// UpdateSurveyStatus updates survey-related fields on a conversation.
func (m *Manager) UpdateSurveyStatus(ctx context.Context, convID string, status string, score *int) error {
	return m.repo.UpdateSurveyStatus(ctx, convID, status, score)
}

// CountCustomerMessages counts inbound messages from customer for a conversation.
func (m *Manager) CountCustomerMessages(ctx context.Context, convID string) (int, error) {
	return m.repo.CountCustomerMessages(ctx, convID)
}

// UpdateConversationMetadata merges the given JSON into conversation metadata.
func (m *Manager) UpdateConversationMetadata(ctx context.Context, convID string, metadataJSON json.RawMessage) error {
	return m.repo.UpdateConversationMetadata(ctx, convID, metadataJSON)
}

// MarkHandoff records a handoff event (increments handoff_count, stamps handoff_at).
func (m *Manager) MarkHandoff(ctx context.Context, convID string) error {
	return m.repo.MarkHandoff(ctx, convID)
}

// SetFirstAgentReply stamps first_agent_reply_at once for a conversation.
func (m *Manager) SetFirstAgentReply(ctx context.Context, convID string) error {
	return m.repo.SetFirstAgentReply(ctx, convID)
}

// MergeIntents unions detected intents into conversation metadata, preserving
// intents from earlier turns.
func (m *Manager) MergeIntents(ctx context.Context, convID string, intents []string, timestamps json.RawMessage) error {
	return m.repo.MergeIntents(ctx, convID, intents, timestamps)
}

// strPtr returns a pointer to the given string, or nil if the string is empty.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
