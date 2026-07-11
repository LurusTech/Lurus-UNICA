package xiaohongshu

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// computeSignature is a test helper that produces a valid HMAC-SHA256 signature.
func computeSignature(secret, timestamp, nonce, body string) string {
	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature_Valid(t *testing.T) {
	secret := "test_webhook_secret"
	timestamp := "1680000000"
	nonce := "abc123"
	body := `{"event":"im.message","msg_id":"m1"}`
	sig := computeSignature(secret, timestamp, nonce, body)

	if !VerifySignature(secret, timestamp, nonce, body, sig) {
		t.Fatal("expected valid signature to pass")
	}
}

func TestVerifySignature_Invalid(t *testing.T) {
	secret := "test_webhook_secret"
	timestamp := "1680000000"
	nonce := "abc123"
	body := `{"event":"im.message"}`

	if VerifySignature(secret, timestamp, nonce, body, "bad_signature") {
		t.Fatal("expected invalid signature to fail")
	}
}

func TestVerifySignature_EmptySecret(t *testing.T) {
	if VerifySignature("", "1680000000", "nonce", "body", "sig") {
		t.Fatal("expected empty secret to fail")
	}
}

func TestVerifySignature_EmptySignature(t *testing.T) {
	if VerifySignature("secret", "1680000000", "nonce", "body", "") {
		t.Fatal("expected empty signature to fail")
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	secret := "test_webhook_secret"
	timestamp := "1680000000"
	nonce := "abc123"
	body := `{"event":"im.message"}`
	sig := computeSignature(secret, timestamp, nonce, body)

	tamperedBody := `{"event":"im.message","extra":"injected"}`
	if VerifySignature(secret, timestamp, nonce, tamperedBody, sig) {
		t.Fatal("expected tampered body to fail")
	}
}

func TestVerifySignature_DifferentNonce(t *testing.T) {
	secret := "test_webhook_secret"
	timestamp := "1680000000"
	nonce := "abc123"
	body := `{"event":"im.message"}`
	sig := computeSignature(secret, timestamp, nonce, body)

	if VerifySignature(secret, timestamp, "different_nonce", body, sig) {
		t.Fatal("expected different nonce to fail")
	}
}

func TestVerifySignature_EmptyBody(t *testing.T) {
	secret := "test_secret"
	timestamp := "1680000000"
	nonce := "nonce"
	body := ""
	sig := computeSignature(secret, timestamp, nonce, body)

	if !VerifySignature(secret, timestamp, nonce, body, sig) {
		t.Fatal("expected valid signature with empty body to pass")
	}
}
