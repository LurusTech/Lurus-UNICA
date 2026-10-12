package checkup

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Import formats the checkup reads. The native export is what
// scripts/export_redacted.sql produces; the other two are what a business
// that has never run UNICA can hand over.
const (
	FormatUnica    = "unica"
	FormatChatwoot = "chatwoot"
	FormatCSV      = "csv"
)

// Load reads a file in the named format.
func Load(path, format string, csvCols CSVColumns) ([]Conversation, error) {
	switch format {
	case FormatUnica, "":
		return LoadJSONL(path)
	case FormatChatwoot:
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return ParseChatwootJSON(f)
	case FormatCSV:
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return ParseCSV(f, csvCols)
	default:
		return nil, fmt.Errorf("unknown format %q (want unica, chatwoot or csv)", format)
	}
}

// --- Chatwoot ------------------------------------------------------------

// cwConversation is a conversation as the Chatwoot API returns it, with the
// fields the checkup needs. Chatwoot wraps lists two different ways
// (`{"data":{"payload":[...]}}` for conversations, `{"payload":[...]}` for
// messages) and a hand-made dump may be a bare array; ParseChatwootJSON
// accepts all three.
type cwConversation struct {
	ID        json.Number `json:"id"`
	DisplayID json.Number `json:"display_id"`
	Messages  []cwMessage `json:"messages"`
}

type cwMessage struct {
	Content     string          `json:"content"`
	MessageType json.RawMessage `json:"message_type"`
	Private     bool            `json:"private"`
	CreatedAt   json.RawMessage `json:"created_at"`
	Sender      struct {
		Type string `json:"type"`
	} `json:"sender"`
}

// ParseChatwootJSON reads a Chatwoot conversations dump: a JSON array of
// conversations each carrying its messages, or that array under `payload`
// or `data.payload`. Activity messages and private notes are dropped — a
// note is what an agent said to a colleague, not to the customer.
func ParseChatwootJSON(r io.Reader) ([]Conversation, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	// 去掉 UTF-8 BOM，否则 json 解析失败。
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var list []cwConversation
	if err := json.Unmarshal(data, &list); err != nil {
		var wrapped struct {
			Payload []cwConversation `json:"payload"`
			Data    struct {
				Payload []cwConversation `json:"payload"`
			} `json:"data"`
		}
		if err2 := json.Unmarshal(data, &wrapped); err2 != nil {
			return nil, fmt.Errorf("chatwoot json: %w", err)
		}
		list = wrapped.Payload
		if len(list) == 0 {
			list = wrapped.Data.Payload
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("chatwoot json: no conversations found (want an array of conversations, or one under payload / data.payload)")
		}
	}
	var out []Conversation
	for i, cw := range list {
		id := cw.ID.String()
		if id == "" || id == "0" {
			id = cw.DisplayID.String()
		}
		if id == "" || id == "0" {
			return nil, fmt.Errorf("chatwoot json: conversation %d has no id", i+1)
		}
		conv := Conversation{ConversationID: "cw-" + id}
		for n, m := range cw.Messages {
			if m.Private || strings.TrimSpace(m.Content) == "" {
				continue
			}
			sender, ok := chatwootSender(m)
			if !ok {
				continue
			}
			at, err := parseCWTime(m.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("chatwoot json: conversation %s message %d: %w", id, n+1, err)
			}
			conv.Messages = append(conv.Messages, Message{At: at, Sender: sender, Text: m.Content})
		}
		if len(conv.Messages) == 0 {
			continue
		}
		out = append(out, conv)
	}
	return out, nil
}

// chatwootSender maps message_type (0 incoming, 1 outgoing, 2 activity, or
// the same as strings) and the sender type onto the export's sender values.
func chatwootSender(m cwMessage) (string, bool) {
	t := strings.Trim(string(m.MessageType), `"`)
	switch t {
	case "0", "incoming":
		return SenderCustomer, true
	case "1", "outgoing":
		if m.Sender.Type == "agent_bot" {
			return SenderAI, true
		}
		return SenderHuman, true
	default:
		return "", false
	}
}

// parseCWTime reads created_at as unix seconds (what the API returns) or
// an RFC 3339 string (what a UI export or a hand edit produces).
func parseCWTime(raw json.RawMessage) (time.Time, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Unix(int64(f), 0).UTC(), nil
	}
	s = strings.Trim(s, `"`)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable created_at %q", s)
}

// --- CSV -----------------------------------------------------------------

// CSVColumns names the columns of a generic transcript CSV. Marketplace
// seller tools export in formats that change without notice, so instead of
// guessing one the checkup takes the four column names it needs and leaves
// the rest alone.
type CSVColumns struct {
	Conversation string
	At           string
	Sender       string
	Text         string
}

// DefaultCSVColumns are the header names used when none are given.
var DefaultCSVColumns = CSVColumns{Conversation: "conversation_id", At: "at", Sender: "sender", Text: "text"}

// ParseCSVColumns reads "conv,at,sender,text" style overrides.
func ParseCSVColumns(spec string) (CSVColumns, error) {
	if strings.TrimSpace(spec) == "" {
		return DefaultCSVColumns, nil
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 4 {
		return CSVColumns{}, fmt.Errorf("csv columns: want 4 names (conversation,at,sender,text), got %d", len(parts))
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return CSVColumns{Conversation: parts[0], At: parts[1], Sender: parts[2], Text: parts[3]}, nil
}

// senderAliases map what a seller tool calls a speaker onto the export's
// three values. Unknown values are an error, named with the row, rather
// than guessed: a buyer counted as an agent would put the customer's own
// words into the report as promises.
var senderAliases = map[string]string{
	"customer": SenderCustomer, "buyer": SenderCustomer, "contact": SenderCustomer, "user": SenderCustomer, "visitor": SenderCustomer,
	"客户": SenderCustomer, "买家": SenderCustomer, "用户": SenderCustomer, "顾客": SenderCustomer, "访客": SenderCustomer,
	"human": SenderHuman, "agent": SenderHuman, "seller": SenderHuman, "staff": SenderHuman, "operator": SenderHuman,
	"客服": SenderHuman, "卖家": SenderHuman, "坐席": SenderHuman, "人工": SenderHuman, "商家": SenderHuman, "店铺": SenderHuman,
	"ai": SenderAI, "bot": SenderAI, "assistant": SenderAI, "robot": SenderAI, "agent_bot": SenderAI,
	"机器人": SenderAI, "智能客服": SenderAI, "AI": SenderAI,
}

// ParseCSV reads a transcript CSV with a header row.
func ParseCSV(r io.Reader, cols CSVColumns) ([]Conversation, error) {
	if cols == (CSVColumns{}) {
		cols = DefaultCSVColumns
	}
	// BOM 在引号之前时 TrimPrefix 去不掉，读取前先剥掉；Peek 出错（如空文件）忽略，仍走原报错。
	br := bufio.NewReader(r)
	if head, perr := br.Peek(3); perr == nil && bytes.Equal(head, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	rd := csv.NewReader(br)
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	header, err := rd.Read()
	if err != nil {
		return nil, fmt.Errorf("csv: read header: %w", err)
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	need := map[string]string{"conversation": cols.Conversation, "at": cols.At, "sender": cols.Sender, "text": cols.Text}
	pos := map[string]int{}
	for role, name := range need {
		i, ok := idx[name]
		if !ok {
			return nil, fmt.Errorf("csv: no column %q for %s (header: %s)", name, role, strings.Join(header, ", "))
		}
		pos[role] = i
	}

	byID := map[string]*Conversation{}
	var order []string
	for n := 2; ; n++ {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv: line %d: %w", n, err)
		}
		get := func(role string) string {
			i := pos[role]
			if i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		// Excel 导出末尾常带 ",,," 空白行：四列全空直接跳过。
		id := get("conversation")
		if id == "" && get("at") == "" && get("sender") == "" && get("text") == "" {
			continue
		}
		if id == "" {
			return nil, fmt.Errorf("csv: line %d: empty conversation id", n)
		}
		sender, ok := senderAliases[get("sender")]
		if !ok {
			sender, ok = senderAliases[strings.ToLower(get("sender"))]
		}
		if !ok {
			return nil, fmt.Errorf("csv: line %d: unknown sender %q (want one of customer/human/ai or a listed alias)", n, get("sender"))
		}
		at, err := parseCSVTime(get("at"))
		if err != nil {
			return nil, fmt.Errorf("csv: line %d: %w", n, err)
		}
		text := get("text")
		if text == "" {
			continue
		}
		conv := byID[id]
		if conv == nil {
			conv = &Conversation{ConversationID: "csv-" + id}
			byID[id] = conv
			order = append(order, id)
		}
		// 缺时间戳时保持零值，不合成时间；排序由 sortIfTimed 决定。
		conv.Messages = append(conv.Messages, Message{At: at, Sender: sender, Text: text})
	}
	out := make([]Conversation, 0, len(order))
	for _, id := range order {
		conv := byID[id]
		sortIfTimed(conv.Messages)
		out = append(out, *conv)
	}
	return out, nil
}

func parseCSVTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000 // milliseconds
		}
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"2006/01/02 15:04:05", "2006-01-02 15:04", "2006/1/2 15:04:05", "2006/1/2 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable time %q", s)
}
