package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// chatwootStub is the slice of the Chatwoot platform API the agent calls use.
type chatwootStub struct {
	server *httptest.Server

	userCalls  int
	linkCalls  int
	loginCalls int

	createdPassword string
	linkedRole      string
	failLink        bool
	loginURL        string
}

func newChatwootStub(t *testing.T) *chatwootStub {
	t.Helper()
	s := &chatwootStub{loginURL: "https://chat.test/app/login?sso_auth_token=abc"}
	mux := http.NewServeMux()

	mux.HandleFunc("/platform/api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		s.userCalls++
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		s.createdPassword, _ = body["password"].(string)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": 11, "email": body["email"], "access_token": "cw-user-token",
		})
	})

	mux.HandleFunc("/platform/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/account_users") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.linkCalls++
		if s.failLink {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"already a member"}`))
			return
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		s.linkedRole, _ = body["role"].(string)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 3})
	})

	mux.HandleFunc("/platform/api/v1/users/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/login") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.loginCalls++
		fmt.Fprintf(w, `{"url":%q}`, s.loginURL)
	})

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *chatwootStub) client() *ChatwootClient {
	return NewChatwootClient(ChatwootConfig{BaseURL: s.server.URL, PlatformToken: "platform-token"})
}

// TestEnsureAgent_CreatesAndLinks pins the provisioning sequence: one user, one
// membership, and a password the caller never had to invent.
func TestEnsureAgent_CreatesAndLinks(t *testing.T) {
	stub := newChatwootStub(t)

	agent, err := stub.client().EnsureAgent(context.Background(), AgentSpec{
		AccountID: 42, Name: "Ada", Email: "ada@acme.test",
	})
	if err != nil {
		t.Fatalf("EnsureAgent: %v", err)
	}
	if agent.ID != 11 || !agent.Created || agent.AccessToken != "cw-user-token" {
		t.Errorf("agent = %+v", agent)
	}
	if agent.Password == "" || agent.Password != stub.createdPassword {
		t.Errorf("the generated password must be the one sent to chatwoot: %q vs %q",
			agent.Password, stub.createdPassword)
	}
	if stub.linkedRole != ChatwootRoleAgent {
		t.Errorf("linked role = %q, want the default agent role", stub.linkedRole)
	}
}

// TestEnsureAgent_KnownUserIsNotReprovisioned is the re-entrancy that matters:
// a recorded agent cannot be created again (the email is taken and the token
// cannot be read back), so a spec carrying one must not call Chatwoot at all.
func TestEnsureAgent_KnownUserIsNotReprovisioned(t *testing.T) {
	stub := newChatwootStub(t)

	agent, err := stub.client().EnsureAgent(context.Background(), AgentSpec{
		AccountID: 42, UserID: 7, Name: "Ada", Email: "ada@acme.test",
	})
	if err != nil {
		t.Fatalf("EnsureAgent: %v", err)
	}
	if agent.ID != 7 || agent.Created {
		t.Errorf("agent = %+v, want the recorded id and no creation", agent)
	}
	if stub.userCalls != 0 || stub.linkCalls != 0 {
		t.Errorf("chatwoot was called: users=%d links=%d", stub.userCalls, stub.linkCalls)
	}
}

// TestEnsureAgent_FailedLinkStillReportsTheAgent pins that a half-finished run
// still hands back the created agent: its token is offered exactly once, and
// dropping it here would lose it for good.
func TestEnsureAgent_FailedLinkStillReportsTheAgent(t *testing.T) {
	stub := newChatwootStub(t)
	stub.failLink = true

	agent, err := stub.client().EnsureAgent(context.Background(), AgentSpec{
		AccountID: 42, Name: "Ada", Email: "ada@acme.test", Role: ChatwootRoleAdministrator,
	})
	if err == nil {
		t.Fatal("a failed link must be reported")
	}
	if agent == nil || agent.ID != 11 || agent.AccessToken != "cw-user-token" {
		t.Fatalf("agent = %+v, want the created agent alongside the error", agent)
	}
}

func TestLoginURL(t *testing.T) {
	stub := newChatwootStub(t)

	url, err := stub.client().LoginURL(context.Background(), 11)
	if err != nil {
		t.Fatalf("LoginURL: %v", err)
	}
	if url != stub.loginURL {
		t.Errorf("url = %q, want %q", url, stub.loginURL)
	}
	if stub.loginCalls != 1 {
		t.Errorf("login calls = %d, want 1", stub.loginCalls)
	}
}

// TestLoginURL_EmptyURLIsAnError pins that a response without a link is a
// failure rather than a blank link handed to a browser.
func TestLoginURL_EmptyURLIsAnError(t *testing.T) {
	stub := newChatwootStub(t)
	stub.loginURL = ""

	if _, err := stub.client().LoginURL(context.Background(), 11); err == nil {
		t.Fatal("a response carrying no url must be an error")
	}
}
