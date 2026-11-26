package survey

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kefu/unica/pkg/model"
)

// readOutbound decodes every message the handler published, oldest first.
func readOutbound(t *testing.T, h *Handler) []model.StandardMessage {
	t.Helper()
	entries, err := h.rdb.XRange(context.Background(), OutboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("read outbound stream: %v", err)
	}
	out := make([]model.StandardMessage, 0, len(entries))
	for _, e := range entries {
		payload, _ := e.Values["payload"].(string)
		var msg model.StandardMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			t.Fatalf("decode outbound payload: %v", err)
		}
		out = append(out, msg)
	}
	return out
}

// Every channel adapter addresses an outbound message by the customer's
// platform user id and refuses one without it. The thank-you was built from the
// pending record alone, which never held that id, so it went to the dead letter
// queue every time the survey itself did reach the customer.
func TestThankYouIsAddressedToTheCustomerWhoRated(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	h := NewHandler(nil, rdb, newMockStateManager())
	ctx := context.Background()
	conv := &ConversationInfo{
		ID:                     "conv-recipient",
		ChannelID:              "ch-1",
		ProductLineID:          "pl-1",
		CustomerID:             "cust-1",
		CustomerPlatformUserID: "openid-abc",
	}
	if err := h.SendSurvey(ctx, conv, nil); err != nil {
		t.Fatalf("SendSurvey: %v", err)
	}
	handled, err := h.HandleSurveyReply(ctx, conv.ID, "5")
	if err != nil || !handled {
		t.Fatalf("HandleSurveyReply = %v, %v; want handled", handled, err)
	}

	msgs := readOutbound(t, h)
	if len(msgs) != 2 {
		t.Fatalf("published %d messages, want survey + thank-you", len(msgs))
	}
	for i, name := range []string{"survey", "thank-you"} {
		d := msgs[i].Data
		if d.PlatformMeta.PlatformUserID != "openid-abc" {
			t.Errorf("%s platform_user_id = %q, want openid-abc", name, d.PlatformMeta.PlatformUserID)
		}
		if d.ChannelID != "ch-1" || d.CustomerID != "cust-1" || d.ProductLineID != "pl-1" {
			t.Errorf("%s addressed to channel %q customer %q line %q", name, d.ChannelID, d.CustomerID, d.ProductLineID)
		}
	}
}

// A survey sent by the previous version left a pending record without the
// recipient. Its thank-you must still not lose the product line the record
// cannot supply when there is no database to ask.
func TestThankYouFromAnOlderPendingRecordStillPublishes(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	h := NewHandler(nil, rdb, newMockStateManager())
	ctx := context.Background()
	old, _ := json.Marshal(map[string]string{"sent_at": "2026-10-01T00:00:00Z", "channel_id": "ch-1", "customer_id": "cust-1"})
	rdb.Set(ctx, surveyPendingKeyPrefix+"conv-old", string(old), 0)

	handled, err := h.HandleSurveyReply(ctx, "conv-old", "4")
	if err != nil || !handled {
		t.Fatalf("HandleSurveyReply = %v, %v; want handled", handled, err)
	}
	msgs := readOutbound(t, h)
	if len(msgs) != 1 || msgs[0].Data.ChannelID != "ch-1" {
		t.Fatalf("want one thank-you on ch-1, got %+v", msgs)
	}
}

// Customers answer on a phone. A Chinese input method produces full-width
// digits, and "5分" / "5星" is how a score is said aloud; all of these used to
// fall through to the AI as an ordinary question while the rating was lost.
func TestParseRating(t *testing.T) {
	valid := map[string]int{
		"5": 5, " 4 ": 4, "1": 1,
		"５": 5, "１": 1, "　３　": 3,
		"5分": 5, "4 分": 4, "５分": 5, "3星": 3, "２ 星": 2,
	}
	for in, want := range valid {
		got, ok := parseRating(in)
		if !ok || got != want {
			t.Errorf("parseRating(%q) = %d, %v; want %d, true", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", "0", "6", "-1", "3.5", "10", "10分", "０", "６", "5分钟", "5分星", "分", "五", "5 stars", "hello", "5!",
	} {
		if got, ok := parseRating(in); ok {
			t.Errorf("parseRating(%q) = %d, true; want not a rating", in, got)
		}
	}
}
