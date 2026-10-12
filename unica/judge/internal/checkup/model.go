// Package checkup turns historical conversations into one report, on the
// machine they were exported to, with nothing deployed and nothing sent
// anywhere. Every reply a person or a model gave is put through the same
// gate the router runs, in shadow, and the report lists what enforcement
// would have held, which phrases hit an industry redline, which facts the
// ontology contradicts, and — never with a value — which facts the business
// keeps promising without having written down. It is what a prospect reads
// before anything is installed.
//
// Rules the report keeps: no rate below judge.MinShadowSamples, no invented
// fact, no corrected figure. A checkup that said "你的客服 23% 的回复有问题"
// from thirteen conversations would be the kind of claim the gate exists to
// stop.
package checkup

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kefu/unica/pkg/redact"
)

// Conversation is one dialogue. The JSON shape is a subset of the UNICA
// export (scripts/export_redacted.sql): the same file loads here with its
// handoffs and traces ignored, because the checkup judges the words that
// were actually exchanged and nothing the live system concluded about them.
type Conversation struct {
	ConversationID string    `json:"conversation_id"`
	Messages       []Message `json:"messages"`
}

// Message is one line of dialogue. Sender is customer, ai, human or system;
// only the first three are read.
type Message struct {
	At     time.Time `json:"at"`
	Sender string    `json:"sender"`
	Text   string    `json:"text"`
}

// Speakers as the report labels them.
const (
	SenderCustomer = "customer"
	SenderAI       = "ai"
	SenderHuman    = "human"
)

// LoadJSONL reads the UNICA export format: one conversation per line.
func LoadJSONL(path string) ([]Conversation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Conversation
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		raw := sc.Bytes()
		if n == 1 {
			// 只有首行可能带 UTF-8 BOM。
			raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))
		}
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		var c Conversation
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if c.ConversationID == "" {
			return nil, fmt.Errorf("%s:%d: conversation_id is empty", path, n)
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// convAlias 为会话 ID 生成不可逆别名。导入的会话 ID 常是手机号或买家昵称，
// 不能原样进入报告；盐值固定，同一 ID 在同一份导入内别名稳定，便于对照。
func convAlias(id string) string {
	sum := sha256.Sum256([]byte("unica-checkup\x00" + id))
	return "conv-" + hex.EncodeToString(sum[:])[:8]
}

// sortIfTimed 仅当全部消息都带时间戳时才按时间稳定排序；
// 只要有一条缺时间就保持文件顺序，避免零值时间把对话顺序颠倒。
func sortIfTimed(msgs []Message) {
	for _, m := range msgs {
		if m.At.IsZero() {
			return
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].At.Before(msgs[j].At) })
}

// Redact masks every message in place and returns what the strict rules
// still find afterwards, prefixed with where. A report quotes sentences and
// is made to be forwarded, so this runs before anything is judged.
// 会话 ID 同时被替换为别名，三种导入路径一致。
func Redact(convs []Conversation, stats redact.Stats) (residual []string) {
	for i := range convs {
		c := &convs[i]
		c.ConversationID = convAlias(c.ConversationID)
		for j := range c.Messages {
			c.Messages[j].Text = redact.Text(c.Messages[j].Text, stats)
			for _, r := range redact.Residual(c.Messages[j].Text) {
				residual = append(residual, c.ConversationID+" message "+r)
			}
		}
	}
	return residual
}
