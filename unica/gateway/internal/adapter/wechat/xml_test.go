package wechat

import (
	"encoding/json"
	"encoding/xml"
	"testing"
)

func TestInboundMsg_XMLUnmarshal(t *testing.T) {
	raw := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[oUser123]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[text]]></MsgType>
<Content><![CDATA[Hello World]]></Content>
<MsgId>123456789</MsgId>
</xml>`

	var msg InboundMsg
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if msg.ToUserName != "gh_account" {
		t.Errorf("ToUserName = %q", msg.ToUserName)
	}
	if msg.FromUserName != "oUser123" {
		t.Errorf("FromUserName = %q", msg.FromUserName)
	}
	if msg.CreateTime != 1709625600 {
		t.Errorf("CreateTime = %d", msg.CreateTime)
	}
	if msg.MsgType != "text" {
		t.Errorf("MsgType = %q", msg.MsgType)
	}
	if msg.Content != "Hello World" {
		t.Errorf("Content = %q", msg.Content)
	}
	if msg.MsgId != 123456789 {
		t.Errorf("MsgId = %d", msg.MsgId)
	}
}

func TestInboundMsg_XMLMarshalRoundTrip(t *testing.T) {
	original := InboundMsg{
		ToUserName:   "gh_test",
		FromUserName: "user_abc",
		CreateTime:   1000000,
		MsgType:      "image",
		PicUrl:       "https://example.com/pic.jpg",
		MediaId:      "media_xyz",
		MsgId:        99887766,
	}

	data, err := xml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed InboundMsg
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.ToUserName != original.ToUserName {
		t.Errorf("ToUserName = %q, want %q", parsed.ToUserName, original.ToUserName)
	}
	if parsed.PicUrl != original.PicUrl {
		t.Errorf("PicUrl = %q, want %q", parsed.PicUrl, original.PicUrl)
	}
	if parsed.MediaId != original.MediaId {
		t.Errorf("MediaId = %q, want %q", parsed.MediaId, original.MediaId)
	}
}

func TestInboundMsg_XMLUnmarshal_LinkMessage(t *testing.T) {
	raw := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[link]]></MsgType>
<Title><![CDATA[Test Link]]></Title>
<Description><![CDATA[Link Description]]></Description>
<Url><![CDATA[https://example.com]]></Url>
<MsgId>10010</MsgId>
</xml>`

	var msg InboundMsg
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if msg.Title != "Test Link" {
		t.Errorf("Title = %q", msg.Title)
	}
	if msg.Description != "Link Description" {
		t.Errorf("Description = %q", msg.Description)
	}
	if msg.Url != "https://example.com" {
		t.Errorf("Url = %q", msg.Url)
	}
}

func TestInboundMsg_XMLUnmarshal_EventMessage(t *testing.T) {
	raw := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[event]]></MsgType>
<Event><![CDATA[subscribe]]></Event>
<EventKey><![CDATA[qrscene_123]]></EventKey>
</xml>`

	var msg InboundMsg
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if msg.Event != "subscribe" {
		t.Errorf("Event = %q", msg.Event)
	}
	if msg.EventKey != "qrscene_123" {
		t.Errorf("EventKey = %q", msg.EventKey)
	}
}

func TestEncryptedMsg_XMLUnmarshal(t *testing.T) {
	raw := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<Encrypt><![CDATA[encrypted_data_here]]></Encrypt>
</xml>`

	var msg EncryptedMsg
	if err := xml.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if msg.ToUserName != "gh_account" {
		t.Errorf("ToUserName = %q", msg.ToUserName)
	}
	if msg.Encrypt != "encrypted_data_here" {
		t.Errorf("Encrypt = %q", msg.Encrypt)
	}
}

func TestCustomerServiceMsg_JSONMarshal_TextMessage(t *testing.T) {
	msg := CustomerServiceMsg{
		ToUser:  "oUserOpenID",
		MsgType: "text",
		Text:    &TextMsg{Content: "Hello from bot"},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify JSON structure
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	// touser field
	var touser string
	json.Unmarshal(raw["touser"], &touser)
	if touser != "oUserOpenID" {
		t.Errorf("touser = %q", touser)
	}

	// msgtype field
	var msgtype string
	json.Unmarshal(raw["msgtype"], &msgtype)
	if msgtype != "text" {
		t.Errorf("msgtype = %q", msgtype)
	}

	// text field present
	if _, ok := raw["text"]; !ok {
		t.Error("text field missing")
	}

	// image, voice, video should be omitted
	if _, ok := raw["image"]; ok {
		t.Error("image field should be omitted")
	}
	if _, ok := raw["voice"]; ok {
		t.Error("voice field should be omitted")
	}
	if _, ok := raw["video"]; ok {
		t.Error("video field should be omitted")
	}
}

func TestCustomerServiceMsg_JSONMarshal_ImageMessage(t *testing.T) {
	msg := CustomerServiceMsg{
		ToUser:  "oUser",
		MsgType: "image",
		Image:   &MediaMsg{MediaId: "media_abc123"},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed CustomerServiceMsg
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.Image == nil {
		t.Fatal("image should not be nil")
	}
	if parsed.Image.MediaId != "media_abc123" {
		t.Errorf("image.media_id = %q", parsed.Image.MediaId)
	}
	if parsed.Text != nil {
		t.Error("text should be nil for image message")
	}
}

func TestCustomerServiceMsg_JSONMarshal_VideoMessage(t *testing.T) {
	msg := CustomerServiceMsg{
		ToUser:  "oUser",
		MsgType: "video",
		Video: &VideoMsg{
			MediaId:      "vid_media",
			ThumbMediaId: "thumb_media",
			Title:        "Video Title",
			Description:  "A video",
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed CustomerServiceMsg
	json.Unmarshal(data, &parsed)

	if parsed.Video == nil {
		t.Fatal("video should not be nil")
	}
	if parsed.Video.MediaId != "vid_media" {
		t.Errorf("video.media_id = %q", parsed.Video.MediaId)
	}
	if parsed.Video.Title != "Video Title" {
		t.Errorf("video.title = %q", parsed.Video.Title)
	}
}
