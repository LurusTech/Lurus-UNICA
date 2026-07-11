package taobao

import (
	"testing"
)

func TestVerifySignature_Valid(t *testing.T) {
	secret := "test_app_secret"
	params := map[string]string{
		"method":    "taobao.message.get",
		"app_key":   "12345",
		"timestamp": "2026-03-06 10:00:00",
	}

	sig := ComputeSignature(secret, params)
	if !VerifySignature(secret, params, sig) {
		t.Fatal("expected valid signature to pass")
	}
}

func TestVerifySignature_Invalid(t *testing.T) {
	secret := "test_app_secret"
	params := map[string]string{
		"method":  "taobao.message.get",
		"app_key": "12345",
	}

	if VerifySignature(secret, params, "bad_signature") {
		t.Fatal("expected invalid signature to fail")
	}
}

func TestVerifySignature_EmptySecret(t *testing.T) {
	params := map[string]string{"method": "test"}
	if VerifySignature("", params, "anything") {
		t.Fatal("expected empty secret to fail")
	}
}

func TestVerifySignature_EmptySignature(t *testing.T) {
	params := map[string]string{"method": "test"}
	if VerifySignature("secret", params, "") {
		t.Fatal("expected empty signature to fail")
	}
}

func TestVerifySignature_DifferentParams(t *testing.T) {
	secret := "mysecret"
	params1 := map[string]string{"method": "get", "app_key": "111"}
	params2 := map[string]string{"method": "get", "app_key": "222"}

	sig := ComputeSignature(secret, params1)
	if VerifySignature(secret, params2, sig) {
		t.Fatal("expected signature for different params to fail")
	}
}

func TestVerifySignature_CaseInsensitive(t *testing.T) {
	secret := "test_secret"
	params := map[string]string{"key": "value"}

	sig := ComputeSignature(secret, params)
	// Lowercase version should also pass
	lowerSig := sig
	if !VerifySignature(secret, params, lowerSig) {
		t.Fatal("expected case-insensitive comparison to pass")
	}
}

func TestComputeSignature_ExcludesSignParam(t *testing.T) {
	secret := "test_secret"
	params1 := map[string]string{"method": "test", "app_key": "123"}
	params2 := map[string]string{"method": "test", "app_key": "123", "sign": "should_be_ignored"}

	sig1 := ComputeSignature(secret, params1)
	sig2 := ComputeSignature(secret, params2)
	if sig1 != sig2 {
		t.Fatalf("sign param should be excluded: sig1=%s, sig2=%s", sig1, sig2)
	}
}

func TestComputeSignature_SortedKeys(t *testing.T) {
	secret := "test_secret"
	// Different insertion order should produce same signature
	params1 := map[string]string{"a": "1", "b": "2", "c": "3"}
	params2 := map[string]string{"c": "3", "a": "1", "b": "2"}

	sig1 := ComputeSignature(secret, params1)
	sig2 := ComputeSignature(secret, params2)
	if sig1 != sig2 {
		t.Fatalf("signature should be order-independent: sig1=%s, sig2=%s", sig1, sig2)
	}
}

func TestVerifySignature_DifferentSecret(t *testing.T) {
	params := map[string]string{"method": "test"}

	sig := ComputeSignature("secret1", params)
	if VerifySignature("secret2", params, sig) {
		t.Fatal("expected signature with different secret to fail")
	}
}
