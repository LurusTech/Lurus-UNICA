package auth

import (
	"testing"
	"time"
)

func TestGenerateAndValidateToken(t *testing.T) {
	mgr := NewJWTManager("test-secret-key", 2*time.Hour, 7*24*time.Hour)

	pair, err := mgr.GenerateTokenPair("user-123", "test@example.com", "user", "tenant-1")
	if err != nil {
		t.Fatalf("GenerateTokenPair failed: %v", err)
	}

	if pair.AccessToken == "" {
		t.Fatal("access token is empty")
	}
	if pair.RefreshToken == "" {
		t.Fatal("refresh token is empty")
	}
	if pair.ExpiresAt == 0 {
		t.Fatal("expires_at is zero")
	}

	claims, err := mgr.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}

	if claims.UserID != "user-123" {
		t.Errorf("expected user_id 'user-123', got '%s'", claims.UserID)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got '%s'", claims.Email)
	}
	if claims.Role != "user" {
		t.Errorf("expected role 'user', got '%s'", claims.Role)
	}
	if claims.TenantID != "tenant-1" {
		t.Errorf("expected tenant_id 'tenant-1', got '%s'", claims.TenantID)
	}
}

// TestGenerateToken_AdminCarriesNoTenant pins the shape of an admin token: the
// account belongs to no tenant, and an empty tenant_id is what says so.
func TestGenerateToken_AdminCarriesNoTenant(t *testing.T) {
	mgr := NewJWTManager("test-secret-key", 2*time.Hour, 7*24*time.Hour)

	pair, err := mgr.GenerateTokenPair("user-1", "root@example.com", "admin", "")
	if err != nil {
		t.Fatalf("GenerateTokenPair failed: %v", err)
	}
	claims, err := mgr.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}
	if claims.Role != "admin" {
		t.Errorf("expected role 'admin', got '%s'", claims.Role)
	}
	if claims.TenantID != "" {
		t.Errorf("expected empty tenant_id, got '%s'", claims.TenantID)
	}
}

func TestValidateTokenWithWrongSecret(t *testing.T) {
	mgr1 := NewJWTManager("secret-1", 2*time.Hour, 7*24*time.Hour)
	mgr2 := NewJWTManager("secret-2", 2*time.Hour, 7*24*time.Hour)

	pair, _ := mgr1.GenerateTokenPair("user-1", "a@b.com", "user", "tenant-1")

	_, err := mgr2.ValidateToken(pair.AccessToken)
	if err == nil {
		t.Fatal("expected error when validating with wrong secret")
	}
}

func TestExpiredToken(t *testing.T) {
	// Create a manager with very short TTL
	mgr := NewJWTManager("test-secret", 1*time.Millisecond, 7*24*time.Hour)

	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", "user", "tenant-1")

	// Wait for token to expire
	time.Sleep(10 * time.Millisecond)

	_, err := mgr.ValidateToken(pair.AccessToken)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestValidateRefreshToken(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	pair, _ := mgr.GenerateTokenPair("user-1", "a@b.com", "user", "tenant-1")

	claims, err := mgr.ValidateToken(pair.RefreshToken)
	if err != nil {
		t.Fatalf("failed to validate refresh token: %v", err)
	}
	if claims.UserID != "user-1" {
		t.Errorf("expected user_id 'user-1', got '%s'", claims.UserID)
	}
}

func TestHashToken(t *testing.T) {
	hash1 := HashToken("some-token")
	hash2 := HashToken("some-token")
	hash3 := HashToken("different-token")

	if hash1 != hash2 {
		t.Fatal("same token should produce same hash")
	}
	if hash1 == hash3 {
		t.Fatal("different tokens should produce different hashes")
	}
	if hash1 == "" {
		t.Fatal("hash should not be empty")
	}
}

func TestInvalidTokenString(t *testing.T) {
	mgr := NewJWTManager("test-secret", 2*time.Hour, 7*24*time.Hour)
	_, err := mgr.ValidateToken("not-a-valid-jwt")
	if err == nil {
		t.Fatal("expected error for invalid token string")
	}
}
