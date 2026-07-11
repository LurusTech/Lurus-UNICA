// Package taobao implements the ChannelAdapter interface for Taobao Open Platform.
package taobao

import (
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"sort"
	"strings"
)

// VerifySignature validates a Taobao TOP API signature using HMAC-MD5.
// Taobao signature algorithm: sort all param keys alphabetically,
// concatenate key+value pairs, then HMAC-MD5 with app_secret.
// The result is compared (case-insensitive hex) against the provided signature.
func VerifySignature(appSecret string, params map[string]string, signature string) bool {
	if appSecret == "" || signature == "" {
		return false
	}
	expected := ComputeSignature(appSecret, params)
	return hmac.Equal([]byte(strings.ToUpper(expected)), []byte(strings.ToUpper(signature)))
}

// ComputeSignature computes the Taobao TOP HMAC-MD5 signature for the given parameters.
// Steps: 1) sort param keys, 2) concatenate key+value, 3) HMAC-MD5 with secret, 4) hex uppercase.
func ComputeSignature(appSecret string, params map[string]string) string {
	// Collect and sort keys (exclude "sign" itself)
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "sign" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build the raw string: key1value1key2value2...
	var buf strings.Builder
	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteString(params[k])
	}

	mac := hmac.New(md5.New, []byte(appSecret))
	mac.Write([]byte(buf.String()))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}
