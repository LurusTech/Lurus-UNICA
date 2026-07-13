package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/router/internal/bridge"
)

func newRecallTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"query":"q","results":[
			{"id":"b1","section":"Pitfalls","content":"经验一","score":0.9,"tags":[],"updated_at":""},
			{"id":"b2","section":"Guides","content":"经验二","score":0.8,"tags":[],"updated_at":""}
		],"total_found":2,"search_type":"hybrid"}`))
	})
	mux.HandleFunc("/api/v1/kb/search", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"query":"q","total_found":1,"results":[
			{"source_id":"docs","doc_id":"d1","title":"政策","text":"知识一","score":0.7}
		]}`))
	})
	return httptest.NewServer(mux)
}

func TestRecallKnowledge_InjectsBothContexts(t *testing.T) {
	srv := newRecallTestServer(t)
	defer srv.Close()

	r := &Router{acest: &AcestIntegration{
		Client:        bridge.NewAcestClient(),
		Config:        bridge.AcestConfig{BaseURL: srv.URL, Token: "tok"},
		RecallTimeout: 2 * time.Second,
		RecallTopK:    3,
	}}

	expCtx, kbCtx := r.recallKnowledge(context.Background(), "退款")

	if !strings.Contains(expCtx, "经验一") || !strings.Contains(expCtx, "经验二") {
		t.Errorf("experience context missing hits: %q", expCtx)
	}
	if !strings.Contains(expCtx, "[Pitfalls]") {
		t.Errorf("experience context missing section label: %q", expCtx)
	}
	if !strings.Contains(kbCtx, "知识一") || !strings.Contains(kbCtx, "[政策]") {
		t.Errorf("knowledge context wrong: %q", kbCtx)
	}
}

func TestRecallKnowledge_FailOpenWhenServerDown(t *testing.T) {
	r := &Router{acest: &AcestIntegration{
		Client:        bridge.NewAcestClient(),
		Config:        bridge.AcestConfig{BaseURL: "http://127.0.0.1:1", Token: "tok"},
		RecallTimeout: 300 * time.Millisecond,
		RecallTopK:    3,
	}}

	start := time.Now()
	expCtx, kbCtx := r.recallKnowledge(context.Background(), "q")
	elapsed := time.Since(start)

	if expCtx != "" || kbCtx != "" {
		t.Errorf("expected empty contexts on failure, got %q / %q", expCtx, kbCtx)
	}
	if elapsed > 2*time.Second {
		t.Errorf("fail-open took too long: %s", elapsed)
	}
}

// fakeSink records submitted experiences.
type fakeSink struct {
	submitted []bridge.Experience
}

func (f *fakeSink) Submit(exp bridge.Experience) bool {
	f.submitted = append(f.submitted, exp)
	return true
}

func TestSubmitExperience_NoopWithoutIntegration(t *testing.T) {
	r := &Router{}
	// Must not panic with nil integration.
	r.submitExperience(bridge.Experience{UserQuery: "q", AssistantResponse: "a"})

	r.acest = &AcestIntegration{}
	// Must not panic with nil collector.
	r.submitExperience(bridge.Experience{UserQuery: "q", AssistantResponse: "a"})
}

func TestSubmitExperience_ForwardsToSink(t *testing.T) {
	sink := &fakeSink{}
	r := &Router{acest: &AcestIntegration{Collector: sink}}

	r.submitExperience(bridge.Experience{UserQuery: "q", AssistantResponse: "a", Success: false, Error: "handoff"})

	if len(sink.submitted) != 1 {
		t.Fatalf("expected 1 submission, got %d", len(sink.submitted))
	}
	if sink.submitted[0].Success || sink.submitted[0].Error != "handoff" {
		t.Errorf("unexpected experience: %+v", sink.submitted[0])
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("短文本", 10); got != "短文本" {
		t.Errorf("short string modified: %q", got)
	}
	long := strings.Repeat("长", 500)
	got := truncateRunes(long, 400)
	if len([]rune(got)) != 401 { // 400 + ellipsis
		t.Errorf("expected 401 runes, got %d", len([]rune(got)))
	}
}
