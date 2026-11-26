package handoff

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/state"
)

// The name written on a contact the handoff found or made. The platform's own
// name for the customer wins; failing that, a contact still named by its raw
// identifier — what earlier versions wrote — is given the readable name; and a
// name an agent typed in is never overwritten.
func TestContactRename(t *testing.T) {
	const readable = "微信公众号 客户 d-0005"
	legacy := bridge.ContactRecord{ID: 6, Name: "0687c46c", Identifier: "0687c46c"}
	for name, tc := range map[string]struct {
		displayName string
		contact     bridge.ContactRecord
		want        string
	}{
		"platform name wins":        {"张三", legacy, "张三"},
		"legacy identifier name":    {"", legacy, readable},
		"unnamed contact":           {"", bridge.ContactRecord{ID: 6, Identifier: "0687c46c"}, readable},
		"agent chose a name":        {"", bridge.ContactRecord{ID: 6, Name: "王女士", Identifier: "0687c46c"}, ""},
		"just created, already set": {"", bridge.ContactRecord{ID: 6, Name: readable, Identifier: "0687c46c", Created: true}, ""},
	} {
		if got := contactRename(tc.displayName, tc.contact, readable); got != tc.want {
			t.Errorf("%s: contactRename = %q, want %q", name, got, tc.want)
		}
	}
}

// contactsServer is a Chatwoot that answers searches with found (empty for no
// match) and records what was searched, created and updated.
type contactsServer struct {
	mu       sync.Mutex
	found    []map[string]interface{}
	searches []string
	creates  []bridge.CWCreateContactRequest
	updates  []map[string]interface{}
}

func (s *contactsServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contacts/search"):
			q, _ := url.QueryUnescape(r.URL.Query().Get("q"))
			s.searches = append(s.searches, q)
			json.NewEncoder(w).Encode(map[string]interface{}{"meta": map[string]int{"count": len(s.found)}, "payload": s.found})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contacts"):
			var req bridge.CWCreateContactRequest
			json.NewDecoder(r.Body).Decode(&req)
			s.creates = append(s.creates, req)
			json.NewEncoder(w).Encode(map[string]interface{}{"payload": map[string]interface{}{
				"contact": map[string]interface{}{"id": 31, "name": req.Name, "identifier": req.Identifier},
			}})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/contacts/"):
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			s.updates = append(s.updates, body)
			w.Write([]byte(`{"payload":{"id":31}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func handoffTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("ROUTER_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_POSTGRES_URL not set; skipping database-backed test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// One customer is one Chatwoot contact: the handoff keys it by the customer
// UUID — the same key the forwarder now uses — and names it after the channel
// and the platform user id instead of showing the UUID.
func TestEnsureContact_KeysByCustomerAndNamesReadably(t *testing.T) {
	db := handoffTestDB(t)
	ctx := context.Background()

	var lineID, customerID, convID string
	if err := db.QueryRowContext(ctx, `INSERT INTO product_lines (name) VALUES ($1) RETURNING id`,
		"contact-"+uuid.NewString()[:8]).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	channelID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO channels (id, platform, name, product_line_id) VALUES ($1, 'wechat', '微信公众号', $2)`,
		channelID, lineID); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	openID := "oContact-" + uuid.NewString()[:6] + "-a1b2c3"
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (platform_identity, channel_id, display_name, notes) VALUES ($1, $2, '', '') RETURNING id`,
		openID, channelID).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO conversations (channel_id, product_line_id, customer_id, state) VALUES ($1, $2, $3, 'human_processing') RETURNING id`,
		channelID, lineID, customerID).Scan(&convID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		db.Exec(`DELETE FROM customers WHERE id = $1`, customerID)
		db.Exec(`DELETE FROM channels WHERE id = $1`, channelID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	sm := state.NewManager(state.NewRepository(db), state.NewCache(rdb), state.ManagerConfig{IdleTimeout: time.Hour, CheckInterval: time.Minute})
	conv, err := sm.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	const readable = "微信公众号 客户 a1b2c3"

	t.Run("new customer", func(t *testing.T) {
		cw := &contactsServer{}
		h := NewHandoffHandler(db, rdb, sm, nil, bridge.NewChatwootClient())
		cfg := &bridge.ChatwootConfig{BaseURL: cw.start(t), AccountID: 1, InboxID: 1, APIToken: "t"}

		contactID, platformUserID, err := h.ensureContact(ctx, cfg, conv, "low_confidence")
		if err != nil {
			t.Fatalf("ensureContact: %v", err)
		}
		if contactID != 31 || platformUserID != openID {
			t.Errorf("ensureContact = %d, %q; want 31, %q", contactID, platformUserID, openID)
		}
		if len(cw.searches) != 1 || cw.searches[0] != customerID {
			t.Errorf("searched %q, want the customer UUID", cw.searches)
		}
		if len(cw.creates) != 1 || cw.creates[0].Identifier != customerID || cw.creates[0].Name != readable {
			t.Errorf("created %+v, want identifier %s named %q", cw.creates, customerID, readable)
		}
		for _, u := range cw.updates {
			if _, renamed := u["name"]; renamed {
				t.Errorf("a contact created with its name was renamed: %v", u)
			}
		}
	})

	t.Run("contact named by its UUID", func(t *testing.T) {
		cw := &contactsServer{found: []map[string]interface{}{{"id": 31, "name": customerID, "identifier": customerID}}}
		h := NewHandoffHandler(db, rdb, sm, nil, bridge.NewChatwootClient())
		cfg := &bridge.ChatwootConfig{BaseURL: cw.start(t), AccountID: 1, InboxID: 1, APIToken: "t"}

		if _, _, err := h.ensureContact(ctx, cfg, conv, "low_confidence"); err != nil {
			t.Fatalf("ensureContact: %v", err)
		}
		if len(cw.creates) != 0 {
			t.Errorf("created a second contact: %+v", cw.creates)
		}
		if len(cw.updates) != 1 || cw.updates[0]["name"] != readable {
			t.Errorf("updates = %v, want the readable name", cw.updates)
		}
	})

	t.Run("contact an agent renamed", func(t *testing.T) {
		cw := &contactsServer{found: []map[string]interface{}{{"id": 31, "name": "王女士", "identifier": customerID}}}
		h := NewHandoffHandler(db, rdb, sm, nil, bridge.NewChatwootClient())
		cfg := &bridge.ChatwootConfig{BaseURL: cw.start(t), AccountID: 1, InboxID: 1, APIToken: "t"}

		if _, _, err := h.ensureContact(ctx, cfg, conv, "low_confidence"); err != nil {
			t.Fatalf("ensureContact: %v", err)
		}
		for _, u := range cw.updates {
			if _, renamed := u["name"]; renamed {
				t.Errorf("the agent's name was overwritten: %v", u)
			}
		}
	})
}
