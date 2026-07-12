// Package bridge provides external service integration clients for the router.
// This file implements the Chatwoot API client for custom channel integration.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// ChatwootConfig holds Chatwoot API connection parameters for a product line.
type ChatwootConfig struct {
	BaseURL      string `json:"base_url"`
	AccountID    int    `json:"account_id"`
	InboxID      int    `json:"inbox_id"`
	APIToken     string `json:"api_token"`
	WebhookToken string `json:"webhook_token"`
}

// --- Request/Response Types ---

// CWContact represents a Chatwoot contact.
type CWContact struct {
	ID         int               `json:"id"`
	Name       string            `json:"name,omitempty"`
	Email      string            `json:"email,omitempty"`
	Phone      string            `json:"phone_number,omitempty"`
	Identifier string            `json:"identifier,omitempty"`
	CustomAttr map[string]string `json:"custom_attributes,omitempty"`
}

// CWContactPayload wraps the contact in the API response envelope.
type CWContactPayload struct {
	Payload CWContact `json:"payload"`
}

// CWContactSearchResult holds search results from Chatwoot contact search API.
type CWContactSearchResult struct {
	Payload []CWContact `json:"payload"`
}

// CWCreateContactRequest is the request body for creating a contact.
type CWCreateContactRequest struct {
	InboxID    int               `json:"inbox_id"`
	Name       string            `json:"name,omitempty"`
	Email      string            `json:"email,omitempty"`
	Phone      string            `json:"phone_number,omitempty"`
	Identifier string            `json:"identifier,omitempty"`
	CustomAttr map[string]string `json:"custom_attributes,omitempty"`
}

// CWConversation represents a Chatwoot conversation.
type CWConversation struct {
	ID            int    `json:"id"`
	AccountID     int    `json:"account_id"`
	InboxID       int    `json:"inbox_id"`
	Status        string `json:"status"`
	ContactID     int    `json:"contact_id,omitempty"`
	AssigneeID    int    `json:"assignee_id,omitempty"`
	DisplayID     int    `json:"display_id,omitempty"`
	CustomAttr    map[string]string `json:"custom_attributes,omitempty"`
}

// CWCreateConversationRequest is the request body for creating a conversation.
type CWCreateConversationRequest struct {
	SourceID   string            `json:"source_id"`
	InboxID    int               `json:"inbox_id"`
	ContactID  int               `json:"contact_id"`
	Status     string            `json:"status,omitempty"`
	CustomAttr map[string]string `json:"custom_attributes,omitempty"`
}

// CWMessage represents a Chatwoot message.
type CWMessage struct {
	ID          int    `json:"id"`
	Content     string `json:"content"`
	MessageType string `json:"message_type"` // "incoming" or "outgoing"
	ContentType string `json:"content_type,omitempty"`
	Private     bool   `json:"private"`
}

// CWSendMessageRequest is the request body for sending a message.
type CWSendMessageRequest struct {
	Content     string `json:"content"`
	MessageType string `json:"message_type"` // "incoming" = from customer, "outgoing" = from agent
	Private     bool   `json:"private,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// ContactUpdate is the request body for updating a Chatwoot contact's attributes.
type ContactUpdate struct {
	Name             string            `json:"name,omitempty"`
	CustomAttributes map[string]string `json:"custom_attributes,omitempty"`
}

// CWConversationStatusRequest is the request body for toggling conversation status.
type CWConversationStatusRequest struct {
	Status string `json:"status"` // "open", "resolved", "pending"
}

// --- Client ---

// ChatwootClient communicates with the Chatwoot API.
type ChatwootClient struct {
	httpClient *http.Client
}

// NewChatwootClient creates a new ChatwootClient with sensible defaults.
func NewChatwootClient() *ChatwootClient {
	return &ChatwootClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewChatwootClientWithHTTP creates a ChatwootClient with a custom http.Client (for testing).
func NewChatwootClientWithHTTP(hc *http.Client) *ChatwootClient {
	return &ChatwootClient{httpClient: hc}
}

// FindOrCreateContact searches for an existing contact by identifier, or creates one.
// Returns the contact ID.
func (c *ChatwootClient) FindOrCreateContact(ctx context.Context, cfg ChatwootConfig, identifier, name string, customAttr map[string]string) (int, error) {
	// Search for existing contact by identifier
	// Escape the identifier: unescaped '&', '#', '%' or spaces would corrupt the
	// query string and cause a mismatched search, creating a duplicate contact.
	searchURL := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/search?q=%s&include_contacts=true", cfg.BaseURL, cfg.AccountID, url.QueryEscape(identifier))
	searchReq, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return 0, fmt.Errorf("create search request: %w", err)
	}
	c.setAuth(searchReq, cfg.APIToken)

	searchResp, err := c.httpClient.Do(searchReq)
	if err != nil {
		return 0, fmt.Errorf("search contact: %w", err)
	}
	defer searchResp.Body.Close()

	if searchResp.StatusCode == http.StatusOK {
		var result CWContactSearchResult
		if err := json.NewDecoder(searchResp.Body).Decode(&result); err == nil {
			for _, contact := range result.Payload {
				if contact.Identifier == identifier {
					log.Printf("[chatwoot] found existing contact %d for identifier %s", contact.ID, identifier)
					return contact.ID, nil
				}
			}
		}
	}

	// Create new contact
	createReq := CWCreateContactRequest{
		InboxID:    cfg.InboxID,
		Name:       name,
		Identifier: identifier,
		CustomAttr: customAttr,
	}
	contactID, err := c.CreateContact(ctx, cfg, createReq)
	if err != nil {
		return 0, fmt.Errorf("create contact: %w", err)
	}

	log.Printf("[chatwoot] created new contact %d for identifier %s", contactID, identifier)
	return contactID, nil
}

// CreateContact creates a new contact in Chatwoot.
func (c *ChatwootClient) CreateContact(ctx context.Context, cfg ChatwootConfig, req CWCreateContactRequest) (int, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts", cfg.BaseURL, cfg.AccountID)
	body, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("marshal create contact request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create contact request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("post create contact: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read contact response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("create contact returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result CWContactPayload
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("unmarshal contact response: %w", err)
	}

	return result.Payload.ID, nil
}

// CreateConversation creates a new conversation in Chatwoot.
func (c *ChatwootClient) CreateConversation(ctx context.Context, cfg ChatwootConfig, req CWCreateConversationRequest) (*CWConversation, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations", cfg.BaseURL, cfg.AccountID)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal create conversation request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create conversation request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post create conversation: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read conversation response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("create conversation returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var conv CWConversation
	if err := json.Unmarshal(respBody, &conv); err != nil {
		return nil, fmt.Errorf("unmarshal conversation response: %w", err)
	}

	log.Printf("[chatwoot] created conversation %d (inbox=%d, contact=%d)", conv.ID, cfg.InboxID, req.ContactID)
	return &conv, nil
}

// SendMessage sends a message to an existing Chatwoot conversation.
func (c *ChatwootClient) SendMessage(ctx context.Context, cfg ChatwootConfig, conversationID int, req CWSendMessageRequest) (*CWMessage, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages", cfg.BaseURL, cfg.AccountID, conversationID)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal send message request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create send message request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post send message: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read message response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("send message returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var msg CWMessage
	if err := json.Unmarshal(respBody, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message response: %w", err)
	}

	return &msg, nil
}

// UpdateConversationStatus changes the status of a Chatwoot conversation.
func (c *ChatwootClient) UpdateConversationStatus(ctx context.Context, cfg ChatwootConfig, conversationID int, status string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/toggle_status", cfg.BaseURL, cfg.AccountID, conversationID)
	body, err := json.Marshal(CWConversationStatusRequest{Status: status})
	if err != nil {
		return fmt.Errorf("marshal status request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create status request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post toggle status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("toggle status returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[chatwoot] updated conversation %d status to %s", conversationID, status)
	return nil
}

// AddNote adds an internal note (private message) to a conversation.
func (c *ChatwootClient) AddNote(ctx context.Context, cfg ChatwootConfig, conversationID int, content string) error {
	req := CWSendMessageRequest{
		Content:     content,
		MessageType: "outgoing",
		Private:     true,
	}
	_, err := c.SendMessage(ctx, cfg, conversationID, req)
	return err
}

// UpdateContact updates a Chatwoot contact's attributes.
func (c *ChatwootClient) UpdateContact(ctx context.Context, cfg ChatwootConfig, contactID int, update ContactUpdate) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/contacts/%d", cfg.BaseURL, cfg.AccountID, contactID)
	body, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal update contact request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create update contact request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("put update contact: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update contact returned status %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[chatwoot] updated contact %d attributes", contactID)
	return nil
}

// --- Canned Response Types & Methods ---

// CannedResponse represents a Chatwoot canned response.
type CannedResponse struct {
	ID        int    `json:"id"`
	ShortCode string `json:"short_code"`
	Content   string `json:"content"`
}

// ListCannedResponses lists canned responses, optionally filtered by search query.
func (c *ChatwootClient) ListCannedResponses(ctx context.Context, cfg ChatwootConfig, search string) ([]CannedResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/canned_responses", cfg.BaseURL, cfg.AccountID)
	if search != "" {
		url += "?search=" + search
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create list canned responses request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("get canned responses: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read canned responses response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list canned responses returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result []CannedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal canned responses: %w", err)
	}

	return result, nil
}

// CreateCannedResponse creates a new canned response.
func (c *ChatwootClient) CreateCannedResponse(ctx context.Context, cfg ChatwootConfig, shortCode, content string) (*CannedResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/canned_responses", cfg.BaseURL, cfg.AccountID)

	payload := map[string]string{
		"short_code": shortCode,
		"content":    content,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal create canned response request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create canned response request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post create canned response: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read canned response response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("create canned response returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var cr CannedResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("unmarshal canned response: %w", err)
	}

	return &cr, nil
}

// UpdateCannedResponse updates an existing canned response.
func (c *ChatwootClient) UpdateCannedResponse(ctx context.Context, cfg ChatwootConfig, id int, shortCode, content string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/canned_responses/%d", cfg.BaseURL, cfg.AccountID, id)

	payload := map[string]string{
		"short_code": shortCode,
		"content":    content,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update canned response request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create update canned response request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("put update canned response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update canned response returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// DeleteCannedResponse deletes a canned response.
func (c *ChatwootClient) DeleteCannedResponse(ctx context.Context, cfg ChatwootConfig, id int) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/canned_responses/%d", cfg.BaseURL, cfg.AccountID, id)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create delete canned response request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("delete canned response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete canned response returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// AssignConversation assigns a conversation to a specific agent in Chatwoot.
func (c *ChatwootClient) AssignConversation(ctx context.Context, cfg ChatwootConfig, conversationID int, agentID int) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/assignments", cfg.BaseURL, cfg.AccountID, conversationID)
	body, err := json.Marshal(map[string]int{"assignee_id": agentID})
	if err != nil {
		return fmt.Errorf("marshal assignment request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create assignment request: %w", err)
	}
	c.setAuth(httpReq, cfg.APIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post assign conversation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("assign conversation returned status %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[chatwoot] assigned conversation %d to agent %d", conversationID, agentID)
	return nil
}

// setAuth sets the API token header on an HTTP request.
func (c *ChatwootClient) setAuth(req *http.Request, token string) {
	req.Header.Set("api_access_token", token)
}
