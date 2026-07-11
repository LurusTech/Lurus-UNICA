package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	tests := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("hello")},
		{"json", []byte(`{"app_id":"wx123","secret":"s3cr3t"}`)},
		{"binary", func() []byte { b := make([]byte, 256); rand.Read(b); return b }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ciphertext, err := Encrypt(tt.plaintext, key)
			if err != nil {
				t.Fatalf("Encrypt failed: %v", err)
			}

			// Ciphertext should differ from plaintext
			if bytes.Equal(ciphertext, tt.plaintext) && len(tt.plaintext) > 0 {
				t.Error("ciphertext equals plaintext")
			}

			decrypted, err := Decrypt(ciphertext, key)
			if err != nil {
				t.Fatalf("Decrypt failed: %v", err)
			}

			if !bytes.Equal(decrypted, tt.plaintext) {
				t.Errorf("decrypted != plaintext\ngot:  %x\nwant: %x", decrypted, tt.plaintext)
			}
		})
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	plaintext := []byte("same plaintext")

	ct1, _ := Encrypt(plaintext, key)
	ct2, _ := Encrypt(plaintext, key)

	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext should produce different ciphertexts (different nonces)")
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)

	ciphertext, err := Encrypt([]byte("secret data"), key1)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	_, err = Decrypt(ciphertext, key2)
	if err == nil {
		t.Error("expected decryption to fail with wrong key")
	}
}

func TestInvalidKeyLength(t *testing.T) {
	shortKey := make([]byte, 16)
	_, err := Encrypt([]byte("data"), shortKey)
	if err == nil {
		t.Error("expected error for 16-byte key")
	}

	_, err = Decrypt([]byte("data"), shortKey)
	if err == nil {
		t.Error("expected error for 16-byte key")
	}
}

func TestDecryptTooShortCiphertext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	_, err := Decrypt([]byte("short"), key)
	if err == nil {
		t.Error("expected error for too-short ciphertext")
	}
}

func TestParseHexKey(t *testing.T) {
	// Valid 32-byte key in hex
	rawKey := make([]byte, 32)
	rand.Read(rawKey)
	hexKey := hex.EncodeToString(rawKey)

	parsed, err := ParseHexKey(hexKey)
	if err != nil {
		t.Fatalf("ParseHexKey failed: %v", err)
	}
	if !bytes.Equal(parsed, rawKey) {
		t.Error("parsed key does not match original")
	}

	// Invalid hex
	_, err = ParseHexKey("not-hex")
	if err == nil {
		t.Error("expected error for invalid hex")
	}

	// Wrong length
	_, err = ParseHexKey("aabb")
	if err == nil {
		t.Error("expected error for wrong length hex key")
	}
}
