package bridge

import (
	"context"
	"testing"
)

// An agent's contact list showed a customer as a raw UUID (the handoff's
// identifier) or an OpenID (the forwarder's). The name says where the customer
// wrote in and tells two customers apart, and is the same from either path.
func TestContactName(t *testing.T) {
	for _, tc := range []struct{ channel, platformUserID, want string }{
		{"微信公众号", "oE2E-openid-0005", "微信公众号 客户 d-0005"},
		{"", "oE2E-openid-0005", "客户 d-0005"},
		{"  ", "oE2E-openid-0005", "客户 d-0005"},
		{"微信公众号", "", "微信公众号 客户"},
		{"", "", "客户"},
		{"淘宝店", "tb42", "淘宝店 客户 tb42"},
		{"抖音", "用户编号一二三四五六七", "抖音 客户 二三四五六七"},
		{" 微信公众号 ", " oE2E-openid-0005 ", "微信公众号 客户 d-0005"},
	} {
		if got := ContactName(tc.channel, tc.platformUserID); got != tc.want {
			t.Errorf("ContactName(%q, %q) = %q, want %q", tc.channel, tc.platformUserID, got, tc.want)
		}
	}
}

// A contact found by search comes back with the name it carries now, which is
// what tells a name somebody chose apart from the identifier an earlier
// version wrote there. The body was recorded from Chatwoot v3.15.0.
func TestFindOrCreateContactRecord_FoundCarriesCurrentName(t *testing.T) {
	srv := serveRecorded(t, "chatwoot_v3_15_search_contacts.json")

	const id = "0687c46c-c402-4513-af87-651c4354a052"
	contact, err := NewChatwootClient().FindOrCreateContactRecord(context.Background(), testChatwootConfig(srv.URL),
		id, "微信公众号 客户 d-0005", nil)
	if err != nil {
		t.Fatalf("FindOrCreateContactRecord: %v", err)
	}
	if contact.ID != 6 || contact.Identifier != id || contact.Name != id {
		t.Errorf("contact = %+v, want id 6 named by its identifier", contact)
	}
	if contact.Created {
		t.Error("a contact found by search was reported as created")
	}
}

// A created contact reports the name it was created with.
func TestFindOrCreateContactRecord_CreatedCarriesGivenName(t *testing.T) {
	srv := serveRecorded(t, "chatwoot_v3_15_create_contact.json")

	contact, err := NewChatwootClient().FindOrCreateContactRecord(context.Background(), testChatwootConfig(srv.URL),
		"no-such-identifier", "微信公众号 客户 abc123", nil)
	if err != nil {
		t.Fatalf("FindOrCreateContactRecord: %v", err)
	}
	if !contact.Created || contact.ID == 0 || contact.Name != "微信公众号 客户 abc123" || contact.Identifier != "no-such-identifier" {
		t.Errorf("contact = %+v", contact)
	}
}
