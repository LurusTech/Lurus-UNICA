package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	yaml := `
product_lines:
  - name: "TestBrand"
    chatwoot_account_id: 1
    templates:
      - short_code: "greeting"
        content: "Hello!"
      - short_code: "farewell"
        content: "Goodbye!"
  - name: "AnotherBrand"
    chatwoot_account_id: 2
    templates:
      - short_code: "thanks"
        content: "Thank you!"
`
	path := writeTempFile(t, yaml)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if len(cfg.ProductLines) != 2 {
		t.Fatalf("expected 2 product lines, got %d", len(cfg.ProductLines))
	}

	pl := cfg.ProductLines[0]
	if pl.Name != "TestBrand" {
		t.Errorf("expected name TestBrand, got %s", pl.Name)
	}
	if pl.ChatwootAccountID != 1 {
		t.Errorf("expected account_id 1, got %d", pl.ChatwootAccountID)
	}
	if len(pl.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(pl.Templates))
	}
	if pl.Templates[0].ShortCode != "greeting" {
		t.Errorf("expected short_code greeting, got %s", pl.Templates[0].ShortCode)
	}
	if pl.Templates[0].Content != "Hello!" {
		t.Errorf("expected content Hello!, got %s", pl.Templates[0].Content)
	}
}

func TestLoadConfig_MissingName(t *testing.T) {
	yaml := `
product_lines:
  - chatwoot_account_id: 1
    templates:
      - short_code: "greeting"
        content: "Hello!"
`
	path := writeTempFile(t, yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
}

func TestLoadConfig_InvalidAccountID(t *testing.T) {
	yaml := `
product_lines:
  - name: "TestBrand"
    chatwoot_account_id: 0
    templates:
      - short_code: "greeting"
        content: "Hello!"
`
	path := writeTempFile(t, yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid account_id, got nil")
	}
}

func TestLoadConfig_EmptyShortCode(t *testing.T) {
	yaml := `
product_lines:
  - name: "TestBrand"
    chatwoot_account_id: 1
    templates:
      - short_code: ""
        content: "Hello!"
`
	path := writeTempFile(t, yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty short_code, got nil")
	}
}

func TestLoadConfig_EmptyContent(t *testing.T) {
	yaml := `
product_lines:
  - name: "TestBrand"
    chatwoot_account_id: 1
    templates:
      - short_code: "greeting"
        content: ""
`
	path := writeTempFile(t, yaml)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	// Use content that parses as YAML but cannot be unmarshalled into the expected struct
	path := writeTempFile(t, "product_lines: \"not_a_list\"")

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML structure, got nil")
	}
}

func TestLoadConfig_RealConfig(t *testing.T) {
	// Test against the actual config file if it exists
	realPath := filepath.Join("..", "deploy", "config", "canned_responses.yaml")
	if _, err := os.Stat(realPath); os.IsNotExist(err) {
		// Also try the absolute path
		realPath = filepath.Join("..", "..", "deploy", "config", "canned_responses.yaml")
		if _, err := os.Stat(realPath); os.IsNotExist(err) {
			t.Skip("real config file not found, skipping")
		}
	}

	cfg, err := LoadConfig(realPath)
	if err != nil {
		t.Fatalf("LoadConfig on real config returned error: %v", err)
	}

	if len(cfg.ProductLines) < 3 {
		t.Errorf("expected at least 3 product lines, got %d", len(cfg.ProductLines))
	}

	for _, pl := range cfg.ProductLines {
		if len(pl.Templates) < 10 {
			t.Errorf("product line %s has %d templates, expected at least 10", pl.Name, len(pl.Templates))
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

// writeTempFile creates a temporary YAML file for testing.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test_config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}
