package bridge

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestUpdateAppConfig_AgainstLiveDify verifies the request this client builds is
// one a real Dify accepts. The unit tests pin the shape against a fake server,
// which is exactly what the previous implementation would also have passed: it
// sent a well-formed body to an endpoint that does not do what it claimed, and
// only a live console answered 400.
//
// Skipped unless all three variables are set. It snapshots the app's
// configuration and restores it on the way out, so it can be pointed at a real
// app without changing it:
//
//	DIFY_ADMIN_URL=http://localhost:3402/console/api \
//	DIFY_ADMIN_TOKEN=<console access token from POST /console/api/login> \
//	DIFY_TEST_APP_ID=<app id> \
//	go test ./internal/bridge/ -run LiveDify -v
func TestUpdateAppConfig_AgainstLiveDify(t *testing.T) {
	baseURL := os.Getenv("DIFY_ADMIN_URL")
	token := os.Getenv("DIFY_ADMIN_TOKEN")
	appID := os.Getenv("DIFY_TEST_APP_ID")
	if baseURL == "" || token == "" || appID == "" {
		t.Skip("DIFY_ADMIN_URL / DIFY_ADMIN_TOKEN / DIFY_TEST_APP_ID not all set; skipping live Dify test")
	}

	client := NewDifyAdminClient(DifyAdminConfig{BaseURL: baseURL, AdminToken: token})
	ctx := context.Background()

	before, err := client.GetAppConfig(ctx, appID)
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	t.Cleanup(func() {
		if err := client.PutAppConfig(context.Background(), appID, before); err != nil {
			t.Errorf("failed to restore the app configuration: %v", err)
		}
	})

	prompt := DefaultSystemPrompt("LiveProbe")
	if err := client.UpdateAppConfig(ctx, appID, prompt); err != nil {
		t.Fatalf("update app config: %v", err)
	}

	after, err := client.GetAppConfig(ctx, appID)
	if err != nil {
		t.Fatalf("read back app config: %v", err)
	}
	if got, _ := after["pre_prompt"].(string); got != prompt {
		t.Errorf("prompt was not persisted; got %q", got)
	}

	declared := map[string]bool{}
	form, _ := after["user_input_form"].([]interface{})
	for _, item := range form {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		for _, spec := range entry {
			if field, ok := spec.(map[string]interface{}); ok {
				if name, ok := field["variable"].(string); ok {
					declared[name] = true
				}
			}
		}
	}
	for _, v := range ContextVariables {
		if !declared[v.Name] {
			t.Errorf("Dify did not persist the %q variable; the router's input would be dropped", v.Name)
		}
	}

	// The model binding is what a partial write would have destroyed.
	if after["model"] == nil {
		t.Error("the model binding did not survive the update")
	}
	if mode, _ := after["prompt_type"].(string); !strings.EqualFold(mode, "simple") {
		t.Errorf("prompt_type = %q, want simple", mode)
	}
}
