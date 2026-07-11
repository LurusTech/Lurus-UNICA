package wechat

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/model"
)

// helper: build a signed HTTP request with the given query params and body
func newSignedRequest(t *testing.T, method, token string, body string) *http.Request {
	t.Helper()
	timestamp := "1709625600"
	nonce := "testnonce"

	params := []string{token, timestamp, nonce}
	sort.Strings(params)
	hash := sha1.Sum([]byte(strings.Join(params, "")))
	signature := fmt.Sprintf("%x", hash)

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, fmt.Sprintf("/webhook/wechat?signature=%s&timestamp=%s&nonce=%s", signature, timestamp, nonce), bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	return req
}

func newTestAdapter() *Adapter {
	return NewAdapter(Config{
		AppID:         "wx_test_appid",
		AppSecret:     "secret",
		Token:         "testtoken",
		EncryptedMode: false,
	}, "ch_wechat_001", func(ctx context.Context) (string, error) {
		return "mock_access_token", nil
	})
}

// --- VerifyWebhook tests ---

func TestVerifyWebhook_Valid(t *testing.T) {
	a := newTestAdapter()
	req := newSignedRequest(t, http.MethodGet, "testtoken", "")
	if err := a.VerifyWebhook(context.Background(), req); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestVerifyWebhook_Invalid(t *testing.T) {
	a := newTestAdapter()
	req := httptest.NewRequest(http.MethodGet, "/webhook/wechat?signature=bad&timestamp=123&nonce=abc", nil)
	if err := a.VerifyWebhook(context.Background(), req); err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

// --- ParseInbound tests ---

func textXML(from, to, content string, msgID int64, createTime int64) string {
	return fmt.Sprintf(`<xml>
<ToUserName><![CDATA[%s]]></ToUserName>
<FromUserName><![CDATA[%s]]></FromUserName>
<CreateTime>%d</CreateTime>
<MsgType><![CDATA[text]]></MsgType>
<Content><![CDATA[%s]]></Content>
<MsgId>%d</MsgId>
</xml>`, to, from, createTime, content, msgID)
}

func TestParseInbound_Text(t *testing.T) {
	a := newTestAdapter()
	xmlBody := textXML("user_openid", "gh_account", "Hello", 12345, 1709625600)
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeText {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeText)
	}
	if msg.Data.Content.Text != "Hello" {
		t.Errorf("content text = %q, want %q", msg.Data.Content.Text, "Hello")
	}
	if msg.Data.PlatformMeta.PlatformUserID != "user_openid" {
		t.Errorf("platform_user_id = %q, want %q", msg.Data.PlatformMeta.PlatformUserID, "user_openid")
	}
	if msg.Data.PlatformMsgID != "12345" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "12345")
	}
	if msg.Type != model.MessageTypeInbound {
		t.Errorf("type = %q, want %q", msg.Type, model.MessageTypeInbound)
	}
}

func TestParseInbound_Image(t *testing.T) {
	a := newTestAdapter()
	xmlBody := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[image]]></MsgType>
<PicUrl><![CDATA[https://example.com/pic.jpg]]></PicUrl>
<MediaId><![CDATA[media_123]]></MediaId>
<MsgId>10001</MsgId>
</xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeImage {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeImage)
	}
	if msg.Data.Content.URL != "https://example.com/pic.jpg" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
}

func TestParseInbound_Voice(t *testing.T) {
	a := newTestAdapter()
	xmlBody := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[voice]]></MsgType>
<MediaId><![CDATA[voice_media]]></MediaId>
<Format><![CDATA[amr]]></Format>
<Recognition><![CDATA[hello world]]></Recognition>
<MsgId>10002</MsgId>
</xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeText {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeText)
	}
	if msg.Data.Content.Text != "hello world" {
		t.Errorf("text = %q, want %q", msg.Data.Content.Text, "hello world")
	}
}

func TestParseInbound_VoiceNoRecognition(t *testing.T) {
	a := newTestAdapter()
	xmlBody := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[voice]]></MsgType>
<MediaId><![CDATA[voice_media]]></MediaId>
<Format><![CDATA[amr]]></Format>
<MsgId>10003</MsgId>
</xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Text != "[voice message]" {
		t.Errorf("text = %q, want %q", msg.Data.Content.Text, "[voice message]")
	}
}

func TestParseInbound_Video(t *testing.T) {
	a := newTestAdapter()
	xmlBody := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[video]]></MsgType>
<MediaId><![CDATA[video_media_id]]></MediaId>
<ThumbMediaId><![CDATA[thumb_media_id]]></ThumbMediaId>
<MsgId>10004</MsgId>
</xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeVideo {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeVideo)
	}
	if msg.Data.Content.URL != "video_media_id" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
}

func TestParseInbound_Link(t *testing.T) {
	a := newTestAdapter()
	xmlBody := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[link]]></MsgType>
<Title><![CDATA[My Link]]></Title>
<Description><![CDATA[A description]]></Description>
<Url><![CDATA[https://example.com]]></Url>
<MsgId>10005</MsgId>
</xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeLink {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeLink)
	}
	if msg.Data.Content.URL != "https://example.com" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
	if msg.Data.Content.Title != "My Link" {
		t.Errorf("title = %q", msg.Data.Content.Title)
	}
	if msg.Data.Content.Desc != "A description" {
		t.Errorf("desc = %q", msg.Data.Content.Desc)
	}
}

func TestParseInbound_Event(t *testing.T) {
	a := newTestAdapter()
	xmlBody := `<xml>
<ToUserName><![CDATA[gh_account]]></ToUserName>
<FromUserName><![CDATA[user1]]></FromUserName>
<CreateTime>1709625600</CreateTime>
<MsgType><![CDATA[event]]></MsgType>
<Event><![CDATA[subscribe]]></Event>
<EventKey><![CDATA[]]></EventKey>
</xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeEvent {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeEvent)
	}
	if msg.Data.Content.Event != "subscribe" {
		t.Errorf("event = %q, want %q", msg.Data.Content.Event, "subscribe")
	}
	// Events have MsgId=0, so platformMsgID should be "user1:1709625600"
	if msg.Data.PlatformMsgID != "user1:1709625600" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "user1:1709625600")
	}
}

// --- ParseInbound encrypted mode ---

// encryptForTest encrypts a plaintext XML using WeChat's AES-256-CBC format.
func encryptForTest(t *testing.T, encodingAESKey, appID, plainXML string) string {
	t.Helper()
	aesKey, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}

	// Build plaintext: random(16) + msgLen(4) + msg + appID
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	msgBytes := []byte(plainXML)
	appIDBytes := []byte(appID)

	msgLen := make([]byte, 4)
	binary.BigEndian.PutUint32(msgLen, uint32(len(msgBytes)))

	plain := make([]byte, 0, 16+4+len(msgBytes)+len(appIDBytes))
	plain = append(plain, randomBytes...)
	plain = append(plain, msgLen...)
	plain = append(plain, msgBytes...)
	plain = append(plain, appIDBytes...)

	// PKCS#7 pad to block size
	padLen := aes.BlockSize - (len(plain) % aes.BlockSize)
	padding := make([]byte, padLen)
	for i := range padding {
		padding[i] = byte(padLen)
	}
	plain = append(plain, padding...)

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	iv := aesKey[:16]
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, plain)

	return base64.StdEncoding.EncodeToString(ct)
}

func TestParseInbound_Encrypted(t *testing.T) {
	// 43-char base64 encoding AES key (without trailing '=')
	encodingAESKey := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	appID := "wx_test_appid"

	a := NewAdapter(Config{
		AppID:          appID,
		AppSecret:      "secret",
		Token:          "testtoken",
		EncodingAESKey: encodingAESKey,
		EncryptedMode:  true,
	}, "ch_wechat_001", func(ctx context.Context) (string, error) {
		return "token", nil
	})

	innerXML := `<xml><ToUserName><![CDATA[gh_account]]></ToUserName><FromUserName><![CDATA[enc_user]]></FromUserName><CreateTime>1709625600</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[encrypted hello]]></Content><MsgId>99999</MsgId></xml>`

	encrypted := encryptForTest(t, encodingAESKey, appID, innerXML)
	envelopeXML := fmt.Sprintf(`<xml><ToUserName><![CDATA[gh_account]]></ToUserName><Encrypt><![CDATA[%s]]></Encrypt></xml>`, encrypted)

	req := newSignedRequest(t, http.MethodPost, "testtoken", envelopeXML)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound encrypted: %v", err)
	}
	if msg.Data.Content.Text != "encrypted hello" {
		t.Errorf("text = %q, want %q", msg.Data.Content.Text, "encrypted hello")
	}
	if msg.Data.PlatformMeta.PlatformUserID != "enc_user" {
		t.Errorf("platform_user_id = %q", msg.Data.PlatformMeta.PlatformUserID)
	}
}

func TestParseInbound_Encrypted_AppIDMismatch(t *testing.T) {
	encodingAESKey := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

	a := NewAdapter(Config{
		AppID:          "wx_expected_appid",
		AppSecret:      "secret",
		Token:          "testtoken",
		EncodingAESKey: encodingAESKey,
		EncryptedMode:  true,
	}, "ch_wechat_001", func(ctx context.Context) (string, error) {
		return "token", nil
	})

	innerXML := `<xml><ToUserName><![CDATA[gh]]></ToUserName><FromUserName><![CDATA[u]]></FromUserName><CreateTime>1</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[x]]></Content><MsgId>1</MsgId></xml>`

	encrypted := encryptForTest(t, encodingAESKey, "wx_wrong_appid", innerXML)
	envelopeXML := fmt.Sprintf(`<xml><ToUserName><![CDATA[gh]]></ToUserName><Encrypt><![CDATA[%s]]></Encrypt></xml>`, encrypted)

	req := newSignedRequest(t, http.MethodPost, "testtoken", envelopeXML)
	_, err := a.ParseInbound(context.Background(), req)
	if err == nil {
		t.Fatal("expected appid mismatch error")
	}
	if !strings.Contains(err.Error(), "appid mismatch") {
		t.Errorf("error = %q, want to contain 'appid mismatch'", err.Error())
	}
}

func TestParseInbound_InvalidXML(t *testing.T) {
	a := newTestAdapter()
	req := newSignedRequest(t, http.MethodPost, "testtoken", "not xml at all <<<")
	_, err := a.ParseInbound(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid XML")
	}
}

func TestParseInbound_EncryptedDecryptionFailure(t *testing.T) {
	a := NewAdapter(Config{
		AppID:          "wx_test_appid",
		Token:          "testtoken",
		EncodingAESKey: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		EncryptedMode:  true,
	}, "ch_wechat_001", func(ctx context.Context) (string, error) {
		return "token", nil
	})

	// Encrypt field contains invalid base64
	envelopeXML := `<xml><ToUserName><![CDATA[gh]]></ToUserName><Encrypt><![CDATA[not_valid_base64!@#$]]></Encrypt></xml>`
	req := newSignedRequest(t, http.MethodPost, "testtoken", envelopeXML)

	_, err := a.ParseInbound(context.Background(), req)
	if err == nil {
		t.Fatal("expected decryption error")
	}
}

// --- FormatOutbound tests ---

func TestFormatOutbound_Text(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-001",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "Reply text",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "target_user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var csMsg CustomerServiceMsg
	if err := json.Unmarshal(payload, &csMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if csMsg.ToUser != "target_user" {
		t.Errorf("touser = %q", csMsg.ToUser)
	}
	if csMsg.MsgType != "text" {
		t.Errorf("msgtype = %q", csMsg.MsgType)
	}
	if csMsg.Text == nil || csMsg.Text.Content != "Reply text" {
		t.Errorf("text content = %+v", csMsg.Text)
	}
}

func TestFormatOutbound_Image(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-002",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeImage,
				URL:  "media_id_abc",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "target_user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var csMsg CustomerServiceMsg
	if err := json.Unmarshal(payload, &csMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if csMsg.MsgType != "image" {
		t.Errorf("msgtype = %q", csMsg.MsgType)
	}
	if csMsg.Image == nil || csMsg.Image.MediaId != "media_id_abc" {
		t.Errorf("image = %+v", csMsg.Image)
	}
}

func TestFormatOutbound_MissingPlatformUserID(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-003",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "Hi",
			},
		},
	}

	_, err := a.FormatOutbound(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for missing platform_user_id")
	}
	if !strings.Contains(err.Error(), "platform_user_id") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestFormatOutbound_FallbackUnsupportedType(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-004",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeVideo,
				URL:  "video_media",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var csMsg CustomerServiceMsg
	json.Unmarshal(payload, &csMsg)
	if csMsg.MsgType != "text" {
		t.Errorf("expected fallback to text, got %q", csMsg.MsgType)
	}
}

// --- SendMessage tests ---

func TestSendMessage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify token in URL
		token := r.URL.Query().Get("access_token")
		if token != "mock_access_token" {
			t.Errorf("access_token = %q", token)
		}
		// Read and verify JSON body
		body, _ := io.ReadAll(r.Body)
		var csMsg CustomerServiceMsg
		if err := json.Unmarshal(body, &csMsg); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	a := newTestAdapter()
	// Override httpClient to point at test server
	a.httpClient = server.Client()

	// Build payload
	payload, _ := json.Marshal(CustomerServiceMsg{
		ToUser:  "user1",
		MsgType: "text",
		Text:    &TextMsg{Content: "hello"},
	})

	// Override the URL by replacing the send method's URL construction
	// We need to mock the URL - create a custom adapter that uses the test server URL
	a2 := &Adapter{
		cfg:        a.cfg,
		channelID:  a.channelID,
		httpClient: server.Client(),
		getToken: func(ctx context.Context) (string, error) {
			return "mock_access_token", nil
		},
	}
	// Patch: we'll call the test server directly since SendMessage builds URL from weixin.qq.com
	// Instead, set up a transport that redirects
	originalTransport := http.DefaultTransport
	_ = originalTransport

	// Better approach: just test with a RoundTripper that intercepts
	a2.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// Redirect to test server
			req.URL.Scheme = "http"
			req.URL.Host = strings.TrimPrefix(server.URL, "http://")
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	err := a2.SendMessage(context.Background(), "ch_wechat_001", payload)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// roundTripFunc is an adapter to use ordinary functions as http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSendMessage_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errcode":40001,"errmsg":"invalid credential"}`))
	}))
	defer server.Close()

	a := &Adapter{
		cfg:       Config{Token: "t"},
		channelID: "ch1",
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				req.URL.Scheme = "http"
				req.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return http.DefaultTransport.RoundTrip(req)
			}),
		},
		getToken: func(ctx context.Context) (string, error) {
			return "bad_token", nil
		},
	}

	err := a.SendMessage(context.Background(), "ch1", []byte(`{"touser":"u","msgtype":"text","text":{"content":"hi"}}`))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "40001") {
		t.Errorf("error = %q, want to contain '40001'", err.Error())
	}
}

func TestSendMessage_TokenError(t *testing.T) {
	a := &Adapter{
		cfg:        Config{Token: "t"},
		channelID:  "ch1",
		httpClient: &http.Client{},
		getToken: func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("token refresh failed")
		},
	}

	err := a.SendMessage(context.Background(), "ch1", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error when token getter fails")
	}
	if !strings.Contains(err.Error(), "get token") {
		t.Errorf("error = %q", err.Error())
	}
}

// --- parseContent tests ---

func TestParseContent_UnsupportedType(t *testing.T) {
	msg := &InboundMsg{MsgType: "location"}
	c := parseContent(msg)
	if c.Type != model.ContentTypeText {
		t.Errorf("type = %q, want text", c.Type)
	}
	if !strings.Contains(c.Text, "unsupported") {
		t.Errorf("text = %q", c.Text)
	}
}

// Verify XML round-trip for InboundMsg to ensure all fields are captured
func TestParseInbound_AllFieldsPreserved(t *testing.T) {
	a := newTestAdapter()
	xmlBody := textXML("from_user", "to_account", "test msg", 55555, 1709625600)
	req := newSignedRequest(t, http.MethodPost, "testtoken", xmlBody)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}

	if msg.Data.PlatformMeta.AccountID != "to_account" {
		t.Errorf("account_id = %q, want %q", msg.Data.PlatformMeta.AccountID, "to_account")
	}
	if msg.Data.PlatformMeta.RawType != "text" {
		t.Errorf("raw_type = %q, want %q", msg.Data.PlatformMeta.RawType, "text")
	}
	if msg.Data.ChannelID != "ch_wechat_001" {
		t.Errorf("channel_id = %q", msg.Data.ChannelID)
	}
	if msg.Source != "adapter.wechat" {
		t.Errorf("source = %q", msg.Source)
	}
	if msg.ID == "" {
		t.Error("message ID should be non-empty UUID")
	}
}

// Test the XML-level unmarshal for completeness
func TestInboundMsgXMLRoundTrip(t *testing.T) {
	original := InboundMsg{
		ToUserName:   "gh_account",
		FromUserName: "user_openid",
		CreateTime:   1709625600,
		MsgType:      "text",
		Content:      "Hello World",
		MsgId:        12345,
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
		t.Errorf("ToUserName = %q", parsed.ToUserName)
	}
	if parsed.Content != original.Content {
		t.Errorf("Content = %q", parsed.Content)
	}
	if parsed.MsgId != original.MsgId {
		t.Errorf("MsgId = %d", parsed.MsgId)
	}
}
