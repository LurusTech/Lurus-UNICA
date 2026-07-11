package auth

import (
	"testing"
)

func TestHashPassword(t *testing.T) {
	// Use low cost for test speed
	hash, err := HashPassword("testpassword123", 4)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned empty hash")
	}
	if hash == "testpassword123" {
		t.Fatal("hash should not equal plaintext password")
	}
}

func TestHashPasswordEmpty(t *testing.T) {
	_, err := HashPassword("", 4)
	if err == nil {
		t.Fatal("expected error for empty password")
	}
}

func TestVerifyPassword(t *testing.T) {
	password := "secure-password-2024"
	hash, err := HashPassword(password, 4)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	// Correct password should pass
	if err := VerifyPassword(hash, password); err != nil {
		t.Fatalf("VerifyPassword failed for correct password: %v", err)
	}

	// Wrong password should fail
	if err := VerifyPassword(hash, "wrong-password"); err == nil {
		t.Fatal("VerifyPassword should fail for wrong password")
	}
}

func TestDifferentHashesForSamePassword(t *testing.T) {
	// bcrypt should produce different hashes for the same password (due to salt)
	hash1, _ := HashPassword("same-password", 4)
	hash2, _ := HashPassword("same-password", 4)
	if hash1 == hash2 {
		t.Fatal("bcrypt should produce different hashes due to random salt")
	}
}
