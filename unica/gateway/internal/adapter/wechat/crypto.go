package wechat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// VerifySignature checks the WeChat webhook signature.
// WeChat signs requests with SHA1(sort(token, timestamp, nonce)).
func VerifySignature(token, timestamp, nonce, signature string) bool {
	params := []string{token, timestamp, nonce}
	sort.Strings(params)
	raw := strings.Join(params, "")
	hash := sha1.Sum([]byte(raw))
	computed := fmt.Sprintf("%x", hash)
	// Constant-time comparison to avoid a timing side channel that could let an
	// attacker recover a valid signature byte-by-byte.
	return subtle.ConstantTimeCompare([]byte(computed), []byte(signature)) == 1
}

// DecryptMessage decrypts a WeChat AES-256-CBC encrypted message.
// encodingAESKey is the 43-char key from WeChat admin (base64-encoded without trailing '=').
// Returns the decrypted XML message body and the appID embedded in the plaintext.
func DecryptMessage(encodingAESKey, encrypted string) ([]byte, string, error) {
	// Decode AES key: add padding '=' and base64-decode
	aesKey, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return nil, "", fmt.Errorf("decode AES key: %w", err)
	}
	if len(aesKey) != 32 {
		return nil, "", fmt.Errorf("AES key must be 32 bytes, got %d", len(aesKey))
	}

	// Decode encrypted message
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, "", fmt.Errorf("decode ciphertext: %w", err)
	}

	// AES-256-CBC decrypt, IV = first 16 bytes of key
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, "", fmt.Errorf("create cipher: %w", err)
	}
	if len(ciphertext) < aes.BlockSize || len(ciphertext)%aes.BlockSize != 0 {
		return nil, "", errors.New("invalid ciphertext length")
	}

	iv := aesKey[:16]
	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove PKCS#7 padding
	plaintext, err = pkcs7Unpad(plaintext)
	if err != nil {
		return nil, "", fmt.Errorf("unpad: %w", err)
	}

	// Plaintext format: random(16) + msg_len(4, big-endian) + msg + appid
	if len(plaintext) < 20 {
		return nil, "", errors.New("plaintext too short")
	}
	// Convert to int before arithmetic: uint32 (attacker-controlled) with a
	// literal offset can wrap around zero and bypass the bounds check, causing
	// an out-of-range slice panic below.
	msgLen := int(binary.BigEndian.Uint32(plaintext[16:20]))
	if msgLen < 0 || 20+msgLen > len(plaintext) {
		return nil, "", errors.New("message length exceeds plaintext")
	}
	msg := plaintext[20 : 20+msgLen]
	appID := string(plaintext[20+msgLen:])

	return msg, appID, nil
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty data")
	}
	pad := int(data[len(data)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(data) {
		return nil, errors.New("invalid padding")
	}
	for i := len(data) - pad; i < len(data); i++ {
		if data[i] != byte(pad) {
			return nil, errors.New("invalid padding bytes")
		}
	}
	return data[:len(data)-pad], nil
}
