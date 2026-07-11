package kuaishou

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
	body := `{"event":"im","from_user_id":"user1"}`

	sig := computeSignature(secret, timestamp, nonce, body)
	if !VerifySignature(secret, timestamp, nonce, body, sig) {
		t.Fatal("expected valid signature to pass")
	}
}

func TestVerifySignature_Invalid(t *testing.T) {
	secret := "test_webhook_secret"
	if VerifySignature(secret, "1680000000", "abc", "body", "bad_signature") {
		t.Fatal("expected invalid signature to fail")
	}
}

func TestVerifySignature_EmptySecret(t *testing.T) {
	if VerifySignature("", "1680000000", "abc", "body", "anything") {
		t.Fatal("expected empty secret to fail")
	}
}

func TestVerifySignature_EmptySignature(t *testing.T) {
	if VerifySignature("secret", "1680000000", "abc", "body", "") {
		t.Fatal("expected empty signature to fail")
	}
}

func TestVerifySignature_DifferentBody(t *testing.T) {
	secret := "mysecret"
	timestamp := "1680000000"
	nonce := "nonce1"
	body1 := `{"msg":"hello"}`
	body2 := `{"msg":"world"}`

	sig := computeSignature(secret, timestamp, nonce, body1)
	if VerifySignature(secret, timestamp, nonce, body2, sig) {
		t.Fatal("expected signature for different body to fail")
	}
}

func TestVerifySignature_DifferentNonce(t *testing.T) {
	secret := "mysecret"
	timestamp := "1680000000"
	body := `{"msg":"hello"}`

	sig := computeSignature(secret, timestamp, "nonce1", body)
	if VerifySignature(secret, timestamp, "nonce2", body, sig) {
		t.Fatal("expected signature with different nonce to fail")
	}
}

func TestVerifySignature_DifferentTimestamp(t *testing.T) {
	secret := "mysecret"
	nonce := "nonce1"
	body := `{"msg":"hello"}`

	sig := computeSignature(secret, "1680000000", nonce, body)
	if VerifySignature(secret, "1680000001", nonce, body, sig) {
		t.Fatal("expected signature with different timestamp to fail")
	}
}
