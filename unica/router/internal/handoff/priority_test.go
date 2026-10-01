package handoff

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/router/internal/bridge"
)

type priorityCall struct {
	method, path, body, token string
}

func newPriorityServer(t *testing.T, status int) (*httptest.Server, func() []priorityCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []priorityCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, priorityCall{r.Method, r.URL.Path, string(body), r.Header.Get("api_access_token")})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []priorityCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]priorityCall(nil), calls...)
	}
}

// A vulnerable_priority handoff raises the Chatwoot conversation to urgent.
func TestPrioritise_VulnerableRaisesToUrgent(t *testing.T) {
	srv, calls := newPriorityServer(t, http.StatusOK)
	h := &HandoffHandler{chatwootClient: bridge.NewChatwootClient()}
	cfg := &bridge.ChatwootConfig{BaseURL: srv.URL, AccountID: 7, APIToken: "tok"}

	h.prioritise(context.Background(), cfg, 100, &HandoffEvent{
		Reason: compliance.ReasonVulnerable,
		Detail: "客户自述：我是老人",
	})

	got := calls()
	if len(got) != 1 {
		t.Fatalf("got %d Chatwoot calls, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.method != http.MethodPost || c.path != "/api/v1/accounts/7/conversations/100/toggle_priority" {
		t.Errorf("call = %s %s", c.method, c.path)
	}
	if !strings.Contains(c.body, `"priority":"urgent"`) {
		t.Errorf("body = %s, want priority urgent", c.body)
	}
	if c.token != "tok" {
		t.Errorf("api token = %q", c.token)
	}
}

// Every other reason leaves the priority alone.
func TestPrioritise_OtherReasonsDoNothing(t *testing.T) {
	srv, calls := newPriorityServer(t, http.StatusOK)
	h := &HandoffHandler{chatwootClient: bridge.NewChatwootClient()}
	cfg := &bridge.ChatwootConfig{BaseURL: srv.URL, AccountID: 7, APIToken: "tok"}

	for _, reason := range []string{
		compliance.ReasonQuickEntry, compliance.ReasonRepeatUnresolved,
		"keyword_match", "intent_emotional", "low_confidence", "",
	} {
		h.prioritise(context.Background(), cfg, 100, &HandoffEvent{Reason: reason})
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("non-priority reasons reached Chatwoot: %+v", got)
	}
}

// A Chatwoot failure is logged and swallowed; the handoff is not undone.
func TestPrioritise_FailureIsBestEffort(t *testing.T) {
	srv, calls := newPriorityServer(t, http.StatusInternalServerError)
	h := &HandoffHandler{chatwootClient: bridge.NewChatwootClient()}
	cfg := &bridge.ChatwootConfig{BaseURL: srv.URL, AccountID: 7, APIToken: "tok"}

	h.prioritise(context.Background(), cfg, 100, &HandoffEvent{Reason: compliance.ReasonVulnerable})
	if got := calls(); len(got) != 1 {
		t.Errorf("got %d calls, want the one attempt", len(got))
	}
}

// The briefing of a priority handoff opens with 【优先接待】 and the customer's
// own words, and does not repeat them as a violation further down.
func TestBuildHandoffNote_PriorityFirstLine(t *testing.T) {
	note := buildHandoffNote("客户咨询退货流程", &HandoffEvent{
		Reason: compliance.ReasonVulnerable,
		Detail: "客户自述：我是老人",
	})
	first := strings.SplitN(note, "\n", 2)[0]
	if first != "【优先接待】客户自述：我是老人" {
		t.Errorf("first line = %q", first)
	}
	if strings.Contains(note, "违规明细") {
		t.Errorf("a priority handoff is not a violation:\n%s", note)
	}
	if !strings.Contains(note, "客户咨询退货流程") || !strings.Contains(note, compliance.ReasonVulnerable) {
		t.Errorf("summary or reason missing:\n%s", note)
	}

	// Without a summary or a draft the note still carries the priority line.
	if note := buildHandoffNote("", &HandoffEvent{Reason: compliance.ReasonVulnerable}); !strings.HasPrefix(note, "【优先接待】") {
		t.Errorf("bare priority note = %q", note)
	}

	// Other reasons never open with it, and human-access details are not
	// called violations.
	note = buildHandoffNote("客户要求人工", &HandoffEvent{
		Reason: compliance.ReasonRepeatUnresolved,
		Detail: "客户同一问题在 AI 答复后再次提出（连同本条共 2 次），按重复追问自动转人工",
	})
	if strings.Contains(note, "【优先接待】") || strings.Contains(note, "违规明细") || !strings.Contains(note, "说明：") {
		t.Errorf("repeat handoff note malformed:\n%s", note)
	}
}
