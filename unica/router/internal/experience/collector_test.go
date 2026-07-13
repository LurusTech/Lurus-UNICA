package experience

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kefu/unica/router/internal/bridge"
)

func TestCollector_SubmitAndDeliver(t *testing.T) {
	var received int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var exp bridge.Experience
		if err := json.NewDecoder(r.Body).Decode(&exp); err != nil {
			t.Errorf("bad body: %v", err)
		}
		if exp.UserQuery != "问题" {
			t.Errorf("unexpected query: %s", exp.UserQuery)
		}
		atomic.AddInt64(&received, 1)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"task_id":"t1"}`))
	}))
	defer srv.Close()

	c := NewCollector(bridge.NewAcestClient(), bridge.AcestConfig{BaseURL: srv.URL, Token: "tok"}, 8)
	c.Start()
	defer c.Stop()

	if !c.Submit(bridge.Experience{UserQuery: "问题", AssistantResponse: "答复", Success: true}) {
		t.Fatal("submit rejected on empty queue")
	}

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt64(&received) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("experience never delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollector_FullQueueDrops(t *testing.T) {
	// A server that blocks keeps the worker busy so the queue fills up.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"task_id":"t"}`))
	}))
	defer srv.Close()

	c := NewCollector(bridge.NewAcestClient(), bridge.AcestConfig{BaseURL: srv.URL, Token: "tok"}, 1)
	c.Start()
	// Unblock the server before stopping so Stop doesn't wait out the HTTP timeout.
	defer c.Stop()
	defer close(block)

	exp := bridge.Experience{UserQuery: "q", AssistantResponse: "a", Success: true}

	// First submit is picked up by the worker (blocked in delivery), second
	// fills the queue slot; eventually one must be rejected.
	dropped := false
	for i := 0; i < 5; i++ {
		if !c.Submit(exp) {
			dropped = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !dropped {
		t.Fatal("expected a drop on full queue, all submits accepted")
	}
}
