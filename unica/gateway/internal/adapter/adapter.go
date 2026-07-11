// Package adapter defines the ChannelAdapter interface and related types.
// Each platform (WeChat, Douyin, Xiaohongshu, etc.) provides a concrete
// implementation of ChannelAdapter that handles platform-specific webhook
// verification, message parsing, formatting, and delivery.
package adapter

import (
	"context"
	"net/http"

	"github.com/kefu/unica/pkg/model"
)

// ChannelAdapter defines the contract all platform adapters must implement.
// Each method handles one phase of the message lifecycle:
//
//  1. VerifyWebhook  – authenticate incoming webhook requests
//  2. ParseInbound   – normalize platform messages into StandardMessage
//  3. FormatOutbound  – convert StandardMessage into platform-specific payload
//  4. SendMessage     – deliver the formatted payload to the platform API
//
// Implementations MUST be safe for concurrent use by multiple goroutines.
type ChannelAdapter interface {
	// VerifyWebhook validates the platform's webhook signature or verification
	// token from the incoming HTTP request. Platforms typically send a challenge
	// request on initial webhook registration and sign every subsequent callback.
	//
	// Returns nil if the request is authentic, or an error describing why
	// verification failed (e.g. invalid signature, expired timestamp).
	VerifyWebhook(ctx context.Context, r *http.Request) error

	// ParseInbound extracts and normalizes an inbound platform message into a
	// StandardMessage. The HTTP request body contains the raw platform payload
	// (XML for WeChat, JSON for Douyin, etc.).
	//
	// The adapter is responsible for:
	//   - Deserializing the platform-specific format
	//   - Mapping content to the appropriate MessageContent type
	//   - Populating PlatformMeta with original identifiers
	//   - Generating a unique message ID (UUID)
	//
	// Returns the normalized message, or an error if the payload is malformed.
	ParseInbound(ctx context.Context, r *http.Request) (*model.StandardMessage, error)

	// FormatOutbound converts a StandardMessage into platform-specific payload
	// bytes suitable for delivery via SendMessage. The output format depends on
	// the target platform (e.g. JSON for Douyin, XML for WeChat).
	//
	// Returns the serialized payload bytes, or an error if the message cannot
	// be represented in the target platform's format (e.g. unsupported content type).
	FormatOutbound(ctx context.Context, msg *model.StandardMessage) ([]byte, error)

	// SendMessage delivers the platform-formatted payload to the external
	// platform API for the specified channel.
	//
	// channelID identifies the target channel/account on the platform.
	// payload is the byte slice produced by FormatOutbound.
	//
	// Returns nil on successful delivery, or an error on failure. The gateway
	// will apply retry logic for transient errors.
	SendMessage(ctx context.Context, channelID string, payload []byte) error
}
