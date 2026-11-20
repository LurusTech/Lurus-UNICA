package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The testdata/chatwoot_v3_15_*.json bodies were recorded from a Chatwoot
// v3.15.0 instance (POST /api/v1/accounts/1/inbox_members), not written from
// this package's own structs: a stub that answers with the client's own types
// proves nothing about the real response.
func recorded(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return body
}

type inboxMembersCall struct {
	path, token string
	body        struct {
		InboxID int   `json:"inbox_id"`
		UserIDs []int `json:"user_ids"`
	}
}

func inboxMembersServer(t *testing.T, status int, body []byte, calls *[]inboxMembersCall) *ChatwootClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		c := inboxMembersCall{path: r.URL.Path, token: r.Header.Get("api_access_token")}
		json.NewDecoder(r.Body).Decode(&c.body)
		*calls = append(*calls, c)
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return NewChatwootClient(ChatwootConfig{BaseURL: srv.URL, PlatformToken: "platform-token"})
}

// The recorded answer lists every member of the inbox after the call; user 5
// was the one added.
func TestAddInboxMembers_RecordedResponse(t *testing.T) {
	var calls []inboxMembersCall
	c := inboxMembersServer(t, http.StatusOK, recorded(t, "chatwoot_v3_15_inbox_members_add.json"), &calls)

	if err := c.AddInboxMembers(context.Background(), 1, "admin-token", 1, []int{5}); err != nil {
		t.Fatalf("AddInboxMembers: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.path != "/api/v1/accounts/1/inbox_members" || got.token != "admin-token" {
		t.Errorf("request went to %s with token %q", got.path, got.token)
	}
	if got.body.InboxID != 1 || len(got.body.UserIDs) != 1 || got.body.UserIDs[0] != 5 {
		t.Errorf("request body = %+v", got.body)
	}
}

// Chatwoot answers with the account's users that are members, so an id the
// account does not hold is silently absent. That is a failure, not a success.
func TestAddInboxMembers_AbsentFromTheAnswerIsAnError(t *testing.T) {
	var calls []inboxMembersCall
	c := inboxMembersServer(t, http.StatusOK, recorded(t, "chatwoot_v3_15_inbox_members_add.json"), &calls)

	if err := c.AddInboxMembers(context.Background(), 1, "admin-token", 1, []int{6}); err == nil {
		t.Fatal("want an error when the member list does not include the agent")
	}
}

// Only an account administrator may change an inbox's members; Chatwoot answers
// 401 to an agent's token, and 404 to an inbox the account does not have.
func TestAddInboxMembers_RefusalsAreReported(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		file   string
	}{
		"not an administrator": {http.StatusUnauthorized, "chatwoot_v3_15_not_authorized.json"},
		"unknown inbox":        {http.StatusNotFound, "chatwoot_v3_15_not_found.json"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []inboxMembersCall
			c := inboxMembersServer(t, tc.status, recorded(t, tc.file), &calls)
			err := c.AddInboxMembers(context.Background(), 1, "token", 1, []int{5})
			var apiErr *ChatwootAPIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
				t.Fatalf("err = %v, want a ChatwootAPIError with status %d", err, tc.status)
			}
		})
	}
}

// Without a token, an inbox or an agent there is nothing to ask for.
func TestAddInboxMembers_MissingInputsMakeNoRequest(t *testing.T) {
	var calls []inboxMembersCall
	c := inboxMembersServer(t, http.StatusOK, recorded(t, "chatwoot_v3_15_inbox_members_add.json"), &calls)
	ctx := context.Background()
	if err := c.AddInboxMembers(ctx, 1, "", 1, []int{5}); err == nil {
		t.Error("empty token: want an error")
	}
	if err := c.AddInboxMembers(ctx, 1, "t", 0, []int{5}); err == nil {
		t.Error("no inbox: want an error")
	}
	if err := c.AddInboxMembers(ctx, 1, "t", 1, nil); err == nil {
		t.Error("no agents: want an error")
	}
	if len(calls) != 0 {
		t.Errorf("made %d requests, want none", len(calls))
	}
}
