// Package kuaishou implements the ChannelAdapter interface for Kuaishou IM.
package kuaishou

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// VerifySignature validates a Kuaishou webhook signature using HMAC-SHA256.
// The raw string is constructed as: timestamp + nonce + body.
// The secret is the webhook secret configured in the Kuaishou open platform.
func VerifySignature(secret, timestamp, nonce, body, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
