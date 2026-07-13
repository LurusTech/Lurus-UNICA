package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newAcestTestServer builds a fake kb-server asserting auth and returning canned responses.
func newAcestTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	requireAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
			return false
		}
		return true
	}

	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok","version":"test"}`))
	})

	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		if req["query"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{
			"query": "q",
			"results": [
				{"id":"b1","section":"TroubleshootingAndPitfalls","content":"退款需在订单页发起","score":0.92,"tags":["refund"],"updated_at":"2026-07-13T00:00:00Z"}
			],
			"total_found": 1,
			"search_type": "hybrid"
		}`))
	})

	mux.HandleFunc("/api/v1/kb/search", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		w.Write([]byte(`{
			"query": "q",
			"total_found": 1,
			"results": [
				{"source_id":"docs","doc_id":"d1","title":"售后政策","text":"七天无理由退货","score":0.88}
			]
		}`))
	})

	mux.HandleFunc("/api/v2/experiences", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		var exp Experience
		if err := json.NewDecoder(r.Body).Decode(&exp); err != nil || exp.UserQuery == "" || exp.AssistantResponse == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":"invalid_request","message":"missing fields"}}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"task_id":"task-123"}`))
	})

	return httptest.NewServer(mux)
}

func TestAcestClient_PlaybookSearch(t *testing.T) {
	srv := newAcestTestServer(t, "tok")
	defer srv.Close()

	c := NewAcestClient()
	cfg := AcestConfig{BaseURL: srv.URL, Token: "tok"}

	results, err := c.PlaybookSearch(context.Background(), cfg, "退款", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != "退款需在订单页发起" || results[0].Section != "TroubleshootingAndPitfalls" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestAcestClient_KBSearch(t *testing.T) {
	srv := newAcestTestServer(t, "tok")
	defer srv.Close()

	c := NewAcestClient()
	cfg := AcestConfig{BaseURL: srv.URL, Token: "tok"}

	results, err := c.KBSearch(context.Background(), cfg, "退货", nil, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Text != "七天无理由退货" || results[0].Title != "售后政策" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestAcestClient_SubmitExperience(t *testing.T) {
	srv := newAcestTestServer(t, "tok")
	defer srv.Close()

	c := NewAcestClient()
	cfg := AcestConfig{BaseURL: srv.URL, Token: "tok"}

	taskID, err := c.SubmitExperience(context.Background(), cfg, Experience{
		UserQuery:         "怎么退款",
		AssistantResponse: "在订单页发起退款",
		Success:           true,
		SessionID:         "conv-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if taskID != "task-123" {
		t.Errorf("expected task-123, got %s", taskID)
	}
}

func TestAcestClient_AuthFailure(t *testing.T) {
	srv := newAcestTestServer(t, "tok")
	defer srv.Close()

	c := NewAcestClient()
	cfg := AcestConfig{BaseURL: srv.URL, Token: "wrong"}

	if _, err := c.PlaybookSearch(context.Background(), cfg, "q", 3); err == nil {
		t.Fatal("expected auth error, got nil")
	}
}

func TestAcestClient_Health(t *testing.T) {
	srv := newAcestTestServer(t, "tok")
	defer srv.Close()

	c := NewAcestClient()
	if err := c.Health(context.Background(), AcestConfig{BaseURL: srv.URL}); err != nil {
		t.Fatalf("unexpected health error: %v", err)
	}
}

func TestAcestClient_ContextTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer slow.Close()

	c := NewAcestClient()
	cfg := AcestConfig{BaseURL: slow.URL, Token: "tok"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := c.PlaybookSearch(ctx, cfg, "q", 3); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
