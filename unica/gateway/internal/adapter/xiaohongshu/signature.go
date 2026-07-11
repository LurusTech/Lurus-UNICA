package xiaohongshu

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// VerifySignature validates an XHS webhook request using HMAC-SHA256.
// The raw string is: timestamp + nonce + body.
// The signature is compared against the hex-encoded HMAC-SHA256 digest.
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
