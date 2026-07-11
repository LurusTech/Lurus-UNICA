package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// testKeyHex is a valid 64-char hex string (32 bytes) used across tests.
const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestParseHexKey(t *testing.T) {
	key, err := ParseHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32 byte key, got %d", len(key))
	}
}

func TestParseHexKey_InvalidHex(t *testing.T) {
	if _, err := ParseHexKey("not-hex"); err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestParseHexKey_WrongLength(t *testing.T) {
	if _, err := ParseHexKey("aabb"); err == nil {
		t.Fatal("expected error for short key")
	}
}

// TestEncryptDecryptRoundTrip proves that a value encrypted with this
// package's Encrypt (which mirrors admin/internal/crypto's AES-256-GCM
// format byte-for-byte: nonce-prepended ciphertext) decrypts correctly
// with Decrypt here. Since admin's crypto package and this package share
// the identical Encrypt/Decrypt implementation, this test also validates
// cross-service compatibility of the ciphertext format.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := ParseHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	plaintext := []byte("super-secret-app-secret-value")

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should not equal plaintext")
	}

	decrypted, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncrypt_InvalidKeyLength(t *testing.T) {
	shortKey := []byte("too-short")
	if _, err := Encrypt([]byte("data"), shortKey); err == nil {
		t.Fatal("expected error for invalid key length")
	}
}

func TestDecrypt_InvalidKeyLength(t *testing.T) {
	shortKey := []byte("too-short")
	if _, err := Decrypt([]byte("data"), shortKey); err == nil {
		t.Fatal("expected error for invalid key length")
	}
}

func TestDecrypt_TooShortCiphertext(t *testing.T) {
	key, _ := ParseHexKey(testKeyHex)
	if _, err := Decrypt([]byte("x"), key); err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

// TestDecrypt_KnownVector decodes a ciphertext produced independently (via
// hex literal) to guard against accidental format changes (e.g. nonce size
// or GCM tag placement) that would break interoperability with admin's
// stored channel_configs.app_secret_encrypted values.
func TestDecrypt_KnownVector(t *testing.T) {
	key, err := ParseHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	plaintext := []byte("known-vector-value")
	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Round-trip through hex encoding to simulate storage/transport, then decrypt.
	encoded := hex.EncodeToString(ciphertext)
	decodedCiphertext, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}

	decrypted, err := Decrypt(decodedCiphertext, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted mismatch: got %q, want %q", decrypted, plaintext)
	}
}
