package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The testdata/chatwoot_v3_15_*.json bodies were recorded from a Chatwoot
// v3.15.0 instance on 2026-10-12 (inbox fields trimmed, display name edited).
// The other tests in this package answer with this package's own structs,
// so they could only ever agree with the code: that is how a create-contact
// response read as contact 0 and every message write reported as failed went
// unnoticed until the client met a real Chatwoot.

func serveRecorded(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCreateContact_ChatwootV315Response(t *testing.T) {
	srv := serveRecorded(t, "chatwoot_v3_15_create_contact.json")

	id, err := NewChatwootClient().CreateContact(context.Background(), testChatwootConfig(srv.URL),
		CWCreateContactRequest{InboxID: 1, Name: "fixture-customer", Identifier: "fixture-customer-0001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 3 {
		t.Fatalf("contact id = %d, want 3 (payload.contact.id)", id)
	}
}

func TestCreateContact_ResponseWithoutIDIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"payload":{"contact_inbox":{"source_id":"x"}}}`))
	}))
	defer srv.Close()

	id, err := NewChatwootClient().CreateContact(context.Background(), testChatwootConfig(srv.URL),
		CWCreateContactRequest{InboxID: 1, Name: "n"})
	if err == nil {
		t.Fatalf("expected an error for a response without a contact id, got id %d", id)
	}
}

func TestSendMessage_ChatwootV315Response(t *testing.T) {
	cases := []struct {
		fixture string
		id      int
		kind    string
		private bool
	}{
		{"chatwoot_v3_15_create_message_private.json", 4, "outgoing", true},
		{"chatwoot_v3_15_create_message_incoming.json", 5, "incoming", false},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			srv := serveRecorded(t, tc.fixture)
			msg, err := NewChatwootClient().SendMessage(context.Background(), testChatwootConfig(srv.URL), 2,
				CWSendMessageRequest{Content: "c", MessageType: "outgoing"})
			if err != nil {
				t.Fatalf("a message Chatwoot created was reported as failed: %v", err)
			}
			if msg.ID != tc.id || string(msg.MessageType) != tc.kind || msg.Private != tc.private {
				t.Fatalf("got id=%d type=%q private=%t, want id=%d type=%q private=%t",
					msg.ID, msg.MessageType, msg.Private, tc.id, tc.kind, tc.private)
			}
		})
	}
}

func TestFindOrCreateContact_FindsContactWithNonStringAttributes(t *testing.T) {
	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/contacts/search") {
			// An agent can give a contact number or checkbox attributes.
			w.Write([]byte(`{"meta":{"count":1,"current_page":1},"payload":[{"id":7,"identifier":"cust-1",` +
				`"custom_attributes":{"vip":true,"orders":12,"channel_id":"c"}}]}`))
			return
		}
		created = true
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"Identifier has already been taken"}`))
	}))
	defer srv.Close()

	id, err := NewChatwootClient().FindOrCreateContact(context.Background(), testChatwootConfig(srv.URL), "cust-1", "cust-1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 7 || created {
		t.Fatalf("id=%d created=%t, want the existing contact 7 and no create call", id, created)
	}
}

func TestCWMessageType_DecodesNamesIntegersAndUnknownIntegers(t *testing.T) {
	for raw, want := range map[string]CWMessageType{
		`0`: "incoming", `1`: "outgoing", `2`: "activity", `3`: "template",
		`"outgoing"`: "outgoing", `9`: "9",
	} {
		var got CWMessageType
		if err := got.UnmarshalJSON([]byte(raw)); err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", raw, got, err, want)
		}
	}
}
