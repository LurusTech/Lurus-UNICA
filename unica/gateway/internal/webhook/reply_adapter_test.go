package webhook

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kefu/unica/gateway/internal/adapter/wechat"
	"github.com/kefu/unica/pkg/model"
)

// The agent reply this handler publishes is what the outbound consumer hands
// to a channel adapter. It must be something an adapter can send: each one
// addresses the message by PlatformMeta.PlatformUserID and refuses it
// without one, which every agent reply used to lack.
func TestChatwootWebhook_AgentReplyIsSendableByAnAdapter(t *testing.T) {
	h, rdb := newHandler(t)
	post(t, h, fixture(t, messageFixture, nil))

	entries, err := rdb.XRange(context.Background(), outboundStream, "-", "+").Result()
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbound entries = %d, err %v", len(entries), err)
	}
	var msg model.StandardMessage
	if err := json.Unmarshal([]byte(entries[0].Values["payload"].(string)), &msg); err != nil {
		t.Fatal(err)
	}
	ad := wechat.NewAdapter(wechat.Config{}, msg.Data.ChannelID, func(context.Context) (string, error) { return "token", nil })
	body, err := ad.FormatOutbound(context.Background(), &msg)
	if err != nil {
		t.Fatalf("the WeChat adapter refused the agent reply: %v", err)
	}
	var cs struct {
		ToUser  string `json:"touser"`
		MsgType string `json:"msgtype"`
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal(body, &cs); err != nil {
		t.Fatal(err)
	}
	if cs.ToUser != "oWx-openid-001" || cs.MsgType != "text" || cs.Text.Content != "您好，这单可以按坏损退款处理，请上传照片。" {
		t.Errorf("customer service message = %+v", cs)
	}
}
