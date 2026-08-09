package wechat

import (
	"crypto/sha1"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	token := "throwaway-test-token" // arbitrary fixture; signature is computed from it below
	timestamp := "1709625600"
	nonce := "abc123"

	// Generate expected signature
	params := []string{token, timestamp, nonce}
	sort.Strings(params)
	raw := strings.Join(params, "")
	hash := sha1.Sum([]byte(raw))
	expected := fmt.Sprintf("%x", hash)

	if !VerifySignature(token, timestamp, nonce, expected) {
		t.Error("valid signature rejected")
	}
	if VerifySignature(token, timestamp, nonce, "invalid") {
		t.Error("invalid signature accepted")
	}
}
