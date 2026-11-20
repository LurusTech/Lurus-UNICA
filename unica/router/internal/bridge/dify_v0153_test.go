package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata/dify_v0_15_3_chat_blocking_*.json bodies were recorded from a
// Dify 0.15.3 instance on 2026-10-12 answering POST /v1/chat-messages in
// blocking mode, the request this client makes. The model behind it was a
// local fake; the envelope, the metadata and the retrieval records are Dify's
// own. The other tests in this file's package answer with hand-written JSON.

func serveDify(t *testing.T, name string, seen *map[string]interface{}) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			json.NewDecoder(r.Body).Decode(seen)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A semantic-search hit: the answer, Dify's conversation id and the retrieval
// record — score included — come through, and the request is blocking with
// the router's inputs.
func TestChat_DifyV0153SemanticHit(t *testing.T) {
	var seen map[string]interface{}
	srv := serveDify(t, "dify_v0_15_3_chat_blocking_semantic.json", &seen)

	resp, err := NewDifyClient().Chat(context.Background(), DifyConfig{BaseURL: srv.URL, APIKey: "app-test"},
		"这个订单我要退款", "oR5-openid-probe", "", map[string]string{"customer_name": "oR5-openid-probe"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.HasPrefix(resp.Answer, "好的，退款需要人工为您核实") || !strings.Contains(resp.Answer, "[HANDOFF:payout]") {
		t.Errorf("answer = %q", resp.Answer)
	}
	if resp.ConversationID == "" {
		t.Error("conversation id not read")
	}
	if len(resp.RetrieverResources) != 1 {
		t.Fatalf("retriever resources = %d, want 1", len(resp.RetrieverResources))
	}
	if r := resp.RetrieverResources[0]; r.DocumentName != "退款政策" || r.Score != 1.0 || r.DatasetName == "" {
		t.Errorf("retriever resource = %+v", r)
	}
	if resp.Metadata.Usage.TotalTokens != 59 {
		t.Errorf("total tokens = %d, want 59", resp.Metadata.Usage.TotalTokens)
	}
	if seen["response_mode"] != "blocking" {
		t.Errorf("response_mode = %v, want blocking", seen["response_mode"])
	}
}

// A keyword-search hit (a dataset indexed "economy"): Dify reports the passage
// with score 0.0, the same number as a passage that does not match at all.
// The confidence rule averages these scores, so under economy indexing every
// answer reads as confidence 0 and is handed to a person.
func TestChat_DifyV0153KeywordHitCarriesNoScore(t *testing.T) {
	srv := serveDify(t, "dify_v0_15_3_chat_blocking_keyword.json", nil)

	resp, err := NewDifyClient().Chat(context.Background(), DifyConfig{BaseURL: srv.URL, APIKey: "app-test"},
		"你们门店营业时间是几点到几点？", "oR5-openid-probe", "", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.RetrieverResources) != 1 {
		t.Fatalf("retriever resources = %d, want 1", len(resp.RetrieverResources))
	}
	if r := resp.RetrieverResources[0]; r.DocumentName != "门店信息" || r.Score != 0 {
		t.Errorf("retriever resource = %+v, want the store document with score 0", r)
	}
}
