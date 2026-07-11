# STORY-007: WeChat Adapter

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 8
**Status:** Done
**Sprint:** 2
**Created:** 2026-03-05

---

## User Story

As a customer on WeChat, I want my messages received and replies delivered through the official account, so that I can get customer service on my preferred platform.

---

## Description

### Background
WeChat (微信) is the primary customer service channel for most Chinese businesses. The WeChat Official Account platform provides a messaging API that allows businesses to receive and respond to customer messages. This adapter translates between WeChat's XML-based message protocol and UNICA's StandardMessage JSON format.

WeChat uses a unique combination of XML message bodies, SHA1 signature verification, and optional AES message encryption. The adapter must handle all of these while maintaining the clean adapter interface defined in STORY-006.

### Scope
**In scope:**
- HTTP webhook endpoint for WeChat message push callbacks
- SHA1 signature verification (token + timestamp + nonce)
- AES-256-CBC message encryption/decryption (encrypted mode)
- XML parsing for inbound message types: text, image, voice, video, link, event
- Conversion from WeChat XML to StandardMessage JSON
- Conversion from StandardMessage JSON to WeChat XML/JSON reply format
- Outbound reply via WeChat Customer Service API (客服消息接口)
- Media file handling (image/voice/video download URL mapping)
- Webhook verification endpoint (GET request for WeChat server verification)

**Out of scope:**
- WeChat Pay integration
- WeChat Mini Program messages
- Template messages / subscription messages
- Menu configuration
- Token management (handled by STORY-010)

### User Flow
1. Customer sends message in WeChat Official Account chat
2. WeChat server pushes message to adapter webhook endpoint
3. Adapter verifies SHA1 signature
4. Adapter decrypts message (if encrypted mode enabled)
5. Adapter parses XML and converts to StandardMessage
6. StandardMessage forwarded to Gateway for stream publishing
7. When reply arrives from outbound stream:
8. Adapter converts StandardMessage to WeChat format
9. Adapter calls WeChat Customer Service API to send reply
10. Customer sees reply in WeChat chat

---

## Acceptance Criteria

- [ ] GET `/webhook/wechat` returns echostr for WeChat server verification
- [ ] POST `/webhook/wechat` receives and processes WeChat XML messages
- [ ] SHA1 signature verification rejects invalid signatures with 403
- [ ] AES-256-CBC encryption/decryption works for encrypted mode
- [ ] Text messages parsed correctly (content, from, to, timestamp)
- [ ] Image messages parsed correctly (PicUrl, MediaId extracted)
- [ ] Voice messages parsed correctly (MediaId, Recognition if speech-to-text enabled)
- [ ] Video messages parsed correctly (MediaId, ThumbMediaId)
- [ ] Link messages parsed correctly (Title, Description, Url)
- [ ] Event messages handled (subscribe, unsubscribe, CLICK)
- [ ] All inbound messages converted to valid StandardMessage JSON
- [ ] Outbound text replies sent via Customer Service API successfully
- [ ] Outbound image replies sent via Customer Service API successfully
- [ ] Media files referenced by URL (not re-uploaded unless necessary)
- [ ] Invalid/unsupported message types logged and gracefully skipped
- [ ] Integration test with WeChat sandbox environment passes

---

## Technical Notes

### WeChat Message Format (Inbound XML)
```xml
<xml>
  <ToUserName><![CDATA[gh_xxx]]></ToUserName>
  <FromUserName><![CDATA[oXxx_user_openid]]></FromUserName>
  <CreateTime>1348831860</CreateTime>
  <MsgType><![CDATA[text]]></MsgType>
  <Content><![CDATA[Hello]]></Content>
  <MsgId>1234567890123456</MsgId>
</xml>
```

### WeChat Customer Service API (Outbound JSON)
```json
POST https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=TOKEN
{
  "touser": "oXxx_user_openid",
  "msgtype": "text",
  "text": { "content": "Reply message" }
}
```

### SHA1 Signature Verification
```go
func verifySignature(token, timestamp, nonce, signature string) bool {
    params := []string{token, timestamp, nonce}
    sort.Strings(params)
    raw := strings.Join(params, "")
    hash := sha1.Sum([]byte(raw))
    return fmt.Sprintf("%x", hash) == signature
}
```

### AES Encryption (Encrypted Mode)
- Algorithm: AES-256-CBC
- Key: Base64-decoded EncodingAESKey (43 chars + "=" padding = 32 bytes)
- IV: First 16 bytes of AES key
- Padding: PKCS#7
- Encrypted message format: `random(16) + msg_len(4, network byte order) + msg + appid`

### Components
- `gateway/internal/adapter/wechat/adapter.go` — ChannelAdapter interface implementation
- `gateway/internal/adapter/wechat/crypto.go` — AES encryption/decryption
- `gateway/internal/adapter/wechat/xml.go` — XML message parsing/formatting
- `gateway/internal/adapter/wechat/handler.go` — HTTP webhook handler

### Dependencies (Go modules)
```
encoding/xml (stdlib)        # XML parsing
crypto/sha1 (stdlib)         # Signature verification
crypto/aes (stdlib)          # AES encryption
```

### Configuration (environment variables)
```
WECHAT_APP_ID=wxXXXXXXXX
WECHAT_APP_SECRET=xxxxxxxxxxxxxxxx
WECHAT_TOKEN=your_webhook_token
WECHAT_ENCODING_AES_KEY=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
WECHAT_ENCRYPTED_MODE=true
```

### Edge Cases
- WeChat retries failed webhook callbacks (up to 3 times) — dedup via MsgId (handled by STORY-009)
- Some message types have no MsgId (events) — use FromUserName+CreateTime as dedup key
- Voice messages may include speech recognition text (Recognition field) — include in StandardMessage if present
- WeChat requires 5-second webhook response — adapter must respond quickly, processing happens async
- Customer Service API requires valid access_token — obtained from STORY-010 token management

---

## Dependencies

**Prerequisite:**
- STORY-005 (Gateway Core — Redis Streams integration)
- STORY-006 (Standard Message Format + Adapter Interface)
- STORY-010 (Token Auto-Management — for access_token)

**Blocks:**
- End-to-end WeChat message flow validation
- Sprint 2 deliverable: "WeChat messages flow from platform to gateway to router"

**External Dependencies:**
- WeChat Official Account registered and verified
- WeChat sandbox environment access for testing
- Server IP whitelisted in WeChat admin panel

---

## Definition of Done

- [ ] WeChat adapter implements ChannelAdapter interface
- [ ] Webhook verification (GET) works with WeChat server
- [ ] Inbound message parsing handles all required message types
- [ ] SHA1 signature verification passes for valid, rejects invalid
- [ ] AES encryption/decryption works correctly
- [ ] Outbound messages sent via Customer Service API
- [ ] Unit tests for XML parsing, signature verification, encryption (>=80% coverage)
- [ ] Integration test with WeChat sandbox passes
- [ ] Code committed to `gateway/internal/adapter/wechat/`

---

## Story Points Breakdown

- **XML parsing + message conversion:** 2 points
- **SHA1 signature + AES crypto:** 2 points
- **Customer Service API outbound:** 2 points
- **Testing (unit + integration):** 2 points
- **Total:** 8 points

**Rationale:** WeChat's XML format, encryption layer, and multiple message types make this the most complex first adapter. Subsequent adapters (JSON-based) will be simpler.
