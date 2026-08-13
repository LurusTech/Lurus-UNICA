package aisettings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/redis/go-redis/v9"
)

// routeCacheKeys are the cache entries a tenant's two channels hold. Seeding
// them is how the test can tell an invalidation that ran from one that did not.
var routeCacheKeys = []string{routeCacheKeyPrefix + "ch-1", routeCacheKeyPrefix + "ch-2"}

// newGuardrailFixture wires a handler over an in-memory Redis whose cached
// routes are already populated, as a running deployment's would be.
func newGuardrailFixture(t *testing.T, configJSON string) (*Handler, *fakeProductLines, *fakeChannels, *redis.Client) {
	t.Helper()
	rdb := newRedis(t)
	ctx := context.Background()
	for _, key := range routeCacheKeys {
		if err := rdb.HSet(ctx, key, "config_json", `{"guardrail":{"confidence_threshold":0.7}}`).Err(); err != nil {
			t.Fatalf("failed to seed cached route: %v", err)
		}
	}
	// An unrelated key stands in for the rest of the keyspace, which the
	// invalidation must leave alone.
	if err := rdb.Set(ctx, "unrelated:key", "keep me", 0).Err(); err != nil {
		t.Fatal(err)
	}

	pls := &fakeProductLines{
		pl:         &repository.ProductLine{ID: "pl-1", Name: "Acme", DisplayName: "Acme"},
		configJSON: json.RawMessage(configJSON),
	}
	channels := &fakeChannels{ids: []string{"ch-1", "ch-2"}}
	h := NewHandler(Config{ProductLines: pls, Channels: channels, Redis: rdb})
	return h, pls, channels, rdb
}

// assertRoutesInvalidated checks that every cached route of the tenant is gone
// and that nothing else was touched.
func assertRoutesInvalidated(t *testing.T, rdb *redis.Client) {
	t.Helper()
	ctx := context.Background()
	for _, key := range routeCacheKeys {
		n, err := rdb.Exists(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("cached route %s survived the write: the runtime would keep the old settings", key)
		}
	}
	n, err := rdb.Exists(ctx, "unrelated:key").Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("invalidation deleted a key that is not a channel route")
	}
}

// writtenGuardrail decodes the block the handler stored, as the runtime would
// read it back.
func writtenGuardrail(t *testing.T, pls *fakeProductLines) map[string]interface{} {
	t.Helper()
	if pls.writtenKey != guardrailConfigKey {
		t.Fatalf("wrote config_json key %q, want %q — the runtime reads only the latter",
			pls.writtenKey, guardrailConfigKey)
	}
	var block map[string]interface{}
	if err := json.Unmarshal(pls.writtenValue, &block); err != nil {
		t.Fatalf("stored guardrail is not an object: %v", err)
	}
	return block
}

// TestUpdateThreshold_WritesGuardrailAndDropsRouteCache is the whole of the
// defect this module closes: the console's threshold has to land in the one
// place the runtime reads, and the runtime's cached copy has to go.
func TestUpdateThreshold_WritesGuardrailAndDropsRouteCache(t *testing.T) {
	h, pls, channels, rdb := newGuardrailFixture(t,
		`{"dify_dataset_id":"ds-1","guardrail":{"confidence_threshold":0.7,"handoff_keywords":["转人工"],"blocked_topics":["医疗"],"holding_message":"稍候"}}`)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold", `{"threshold":0.85}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	block := writtenGuardrail(t, pls)
	if block["confidence_threshold"] != 0.85 {
		t.Errorf("confidence_threshold = %v, want 0.85", block["confidence_threshold"])
	}
	// A threshold write must not blank the rest of the block.
	kw, _ := block["handoff_keywords"].([]interface{})
	if len(kw) != 1 || kw[0] != "转人工" {
		t.Errorf("handoff_keywords = %v, want the stored list preserved", block["handoff_keywords"])
	}
	topics, _ := block["blocked_topics"].([]interface{})
	if len(topics) != 1 || topics[0] != "医疗" {
		t.Errorf("blocked_topics = %v, want the stored list preserved", block["blocked_topics"])
	}
	if block["holding_message"] != "稍候" {
		t.Errorf("holding_message = %v, want the stored text preserved", block["holding_message"])
	}
	// The sibling keys of config_json belong to other modules.
	var full map[string]json.RawMessage
	if err := json.Unmarshal(pls.configJSON, &full); err != nil {
		t.Fatal(err)
	}
	if _, ok := full["dify_dataset_id"]; !ok {
		t.Error("the write disturbed another module's config_json key")
	}

	if channels.seen != "pl-1" {
		t.Errorf("looked up channels of %q, want pl-1", channels.seen)
	}
	assertRoutesInvalidated(t, rdb)

	// The response is the block as it now stands, so the console shows what the
	// runtime will read.
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["product_line_id"] != "pl-1" || resp["confidence_threshold"] != 0.85 {
		t.Errorf("unexpected response: %v", resp)
	}
}

// A tenant that has never been configured must be written as a complete block,
// not as one field beside three zero values the runtime would have to guess at.
func TestUpdateThreshold_FillsDefaultsForAnUnconfiguredTenant(t *testing.T) {
	h, pls, _, rdb := newGuardrailFixture(t, "")

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold", `{"threshold":0.5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	block := writtenGuardrail(t, pls)
	if block["confidence_threshold"] != 0.5 {
		t.Errorf("confidence_threshold = %v, want 0.5", block["confidence_threshold"])
	}
	kw, _ := block["handoff_keywords"].([]interface{})
	if len(kw) != len(defaultGuardrailConfig().HandoffKeywords) {
		t.Errorf("handoff_keywords = %v, want the runtime defaults", block["handoff_keywords"])
	}
	if block["holding_message"] != defaultGuardrailConfig().HoldingMessage {
		t.Errorf("holding_message = %v, want the runtime default", block["holding_message"])
	}
	if _, ok := block["blocked_topics"]; !ok {
		t.Error("blocked_topics missing from the stored block")
	}
	assertRoutesInvalidated(t, rdb)
}

// The runtime cannot express a zero threshold — it reads zero as "unset" and
// falls back to its default — so accepting one here would report a setting the
// running system does not have.
func TestUpdateThreshold_RejectsOutOfRange(t *testing.T) {
	for _, body := range []string{`{"threshold":0}`, `{"threshold":-0.1}`, `{"threshold":1.1}`, `not json`} {
		h, pls, _, rdb := newGuardrailFixture(t, "")
		w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, w.Code)
		}
		if pls.writtenKey != "" {
			t.Errorf("body %q: a rejected threshold was still stored", body)
		}
		n, err := rdb.Exists(context.Background(), routeCacheKeys[0]).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("body %q: a rejected write still dropped the route cache", body)
		}
	}
}

func TestUpdateHandoffRules_WritesGuardrail(t *testing.T) {
	h, pls, _, rdb := newGuardrailFixture(t,
		`{"guardrail":{"confidence_threshold":0.6,"handoff_keywords":["旧词"],"holding_message":"稍候"}}`)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/handoff-rules",
		`{"handoff_keywords":["转人工","投诉"],"blocked_topics":["医疗"],"threshold":0.9}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	block := writtenGuardrail(t, pls)
	kw, _ := block["handoff_keywords"].([]interface{})
	if len(kw) != 2 || kw[0] != "转人工" || kw[1] != "投诉" {
		t.Errorf("handoff_keywords = %v", block["handoff_keywords"])
	}
	topics, _ := block["blocked_topics"].([]interface{})
	if len(topics) != 1 || topics[0] != "医疗" {
		t.Errorf("blocked_topics = %v", block["blocked_topics"])
	}
	if block["confidence_threshold"] != 0.9 {
		t.Errorf("confidence_threshold = %v, want the supplied 0.9", block["confidence_threshold"])
	}
	if block["holding_message"] != "稍候" {
		t.Errorf("holding_message = %v, want the stored text preserved", block["holding_message"])
	}
	assertRoutesInvalidated(t, rdb)
}

// Omitting the threshold leaves the stored one alone: the two settings are
// edited on separate screens.
func TestUpdateHandoffRules_KeepsThresholdWhenAbsent(t *testing.T) {
	h, pls, _, _ := newGuardrailFixture(t, `{"guardrail":{"confidence_threshold":0.6}}`)

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/handoff-rules",
		`{"handoff_keywords":["转人工"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if block := writtenGuardrail(t, pls); block["confidence_threshold"] != 0.6 {
		t.Errorf("confidence_threshold = %v, want the stored 0.6", block["confidence_threshold"])
	}
}

func TestUpdateHandoffRules_RequiresKeywords(t *testing.T) {
	h, pls, _, _ := newGuardrailFixture(t, "")

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/handoff-rules", `{"blocked_topics":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if pls.writtenKey != "" {
		t.Error("an incomplete request was still stored")
	}
}

// A failed write must not be reported as success, and must not drop a cache
// entry that still matches the database.
func TestWriteGuardrail_StoreFailure(t *testing.T) {
	h, pls, _, rdb := newGuardrailFixture(t, "")
	pls.writeErr = errors.New("database is down")

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold", `{"threshold":0.5}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	n, err := rdb.Exists(context.Background(), routeCacheKeys[0]).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("a failed write dropped the route cache")
	}
}

// A channel lookup that fails must not fail the write: the settings are stored,
// and the stale cache entries expire on their own.
func TestWriteGuardrail_SurvivesChannelLookupFailure(t *testing.T) {
	h, pls, channels, _ := newGuardrailFixture(t, "")
	channels.err = errors.New("database is down")

	w := do(t, h, http.MethodPut, "/api/v1/tenants/pl-1/ai-settings/threshold", `{"threshold":0.5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if pls.writtenKey != guardrailConfigKey {
		t.Error("the settings were not stored")
	}
}

func TestLoadGuardrail_BackFillsLikeTheRuntime(t *testing.T) {
	defaults := defaultGuardrailConfig()

	cases := []struct {
		name string
		raw  string
		want guardrailConfig
	}{
		{"empty config", "", defaults},
		{"no guardrail key", `{"dify_dataset_id":"ds-1"}`, defaults},
		{"unparsable", `not json`, defaults},
		{
			"partial block",
			`{"guardrail":{"blocked_topics":["医疗"]}}`,
			guardrailConfig{
				ConfidenceThreshold: defaults.ConfidenceThreshold,
				HandoffKeywords:     defaults.HandoffKeywords,
				BlockedTopics:       []string{"医疗"},
				HoldingMessage:      defaults.HoldingMessage,
			},
		},
		{
			"full block",
			`{"guardrail":{"confidence_threshold":0.5,"handoff_keywords":["人工"],"blocked_topics":[],"holding_message":"请稍候"}}`,
			guardrailConfig{
				ConfidenceThreshold: 0.5,
				HandoffKeywords:     []string{"人工"},
				BlockedTopics:       []string{},
				HoldingMessage:      "请稍候",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := loadGuardrail(json.RawMessage(c.raw))
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(c.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("loadGuardrail = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// The stored shape is the runtime's contract: these names are what the reader
// looks for, and a rename on either side silently disconnects the console from
// the running system.
func TestGuardrailConfig_JSONShape(t *testing.T) {
	data, err := json.Marshal(defaultGuardrailConfig())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"confidence_threshold", "handoff_keywords", "blocked_topics", "holding_message"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("guardrail block has no %q field", name)
		}
	}
	if len(fields) != 4 {
		t.Errorf("guardrail block carries unexpected fields: %s", data)
	}
}

func TestAuditState_ReportsTheGuardrailBlock(t *testing.T) {
	pls := &fakeProductLines{
		pl:         &repository.ProductLine{ID: "pl-1"},
		configJSON: json.RawMessage(`{"guardrail":{"confidence_threshold":0.42},"chatwoot":{"account_id":7}}`),
	}
	h := NewHandler(Config{ProductLines: pls})

	state, err := h.AuditState(context.Background(), "pl-1")
	if err != nil {
		t.Fatal(err)
	}
	var block map[string]interface{}
	if err := json.Unmarshal(state, &block); err != nil {
		t.Fatal(err)
	}
	if block["confidence_threshold"] != 0.42 {
		t.Errorf("audit state = %s, want the guardrail block", state)
	}
	// Other modules' config keys are not this trail's business.
	if _, ok := block["chatwoot"]; ok {
		t.Errorf("audit state carries another module's config: %s", state)
	}
}
