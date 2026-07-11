// Package token provides token lifecycle management for channel adapters.
package token

import "context"

// TokenRefresher defines the interface for platform-specific token refresh logic.
type TokenRefresher interface {
	// Platform returns the platform identifier (e.g., "wechat").
	Platform() string
	// Refresh obtains a new access token using the given credentials.
	Refresh(ctx context.Context, credentials ChannelCredentials) (*TokenResult, error)
}

// ChannelCredentials holds the authentication credentials for a channel.
type ChannelCredentials struct {
	AppID     string
	AppSecret string
	Extra     map[string]string // platform-specific fields
}

// TokenResult holds the result of a token refresh operation.
type TokenResult struct {
	AccessToken string
	ExpiresIn   int // seconds
}
