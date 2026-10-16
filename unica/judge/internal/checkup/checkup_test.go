package checkup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/judge"
	"github.com/kefu/unica/pkg/redact"
)

const ontologyYAML = `
product_line: Shop
classes:
  Goods:
    label: 商品
properties:
  return_window_days:
    label: 无理由退货窗口
    domain: Goods
    range: {type: integer, unit: day}
    functional: true
  warranty_months:
    label: 质保期
    domain: Goods
    range: {type: integer, unit: month}
assertions:
  - class: Goods
    values:
      return_window_days: 7
`

func ontology(t *testing.T) *domain.Ontology {
	t.Helper()
	o, err := domain.ParseYAML([]byte(ontologyYAML))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func at(min int) time.Time { return time.Date(2026, 10, 1, 9, min, 0, 0, time.UTC) }

func conv(id string, msgs ...Message) Conversation {
	return Conversation{ConversationID: id, Messages: msgs}
}

func msg(min int, sender, text string) Message {
	return Message{At: at(min), Sender: sender, Text: text}
}

func sample() []Conversation {
	return []Conversation{
		conv("c1",
			msg(1, SenderCustomer, "坏了能赔吗"),
			msg(2, SenderHuman, "可以的，我们赔付200元，全额退款。")),
		conv("c2",
			msg(1, SenderCustomer, "几天内能退"),
			msg(2, SenderAI, "15天内可以无理由退货。[FACT:return_window_days=15]")),
		conv("c3",
			msg(1, SenderCustomer, "这个质保期多久"),
			msg(2, SenderHuman, "本商品质保两年，非人为损坏免费换新。"),
			msg(3, SenderCustomer, "好的"),
			msg(4, SenderHuman, "这款是全网最低价，放心拍。")),
		// c4 documents a boundary, not a finding: 工作日 is not a unit the
		// figure extractor knows, so this promise carries no figure and is
		// not held. Writing the refund timing down as a fact is still the
		// right move; the report just cannot see this one on its own.
		conv("c4",
			msg(1, SenderCustomer, "退款多久到账"),
			msg(2, SenderHuman, "退款3个工作日内原路退回。")),
		conv("c5",
			msg(1, SenderCustomer, "你好"),
			msg(2, SenderAI, "您好，请问有什么可以帮您？"),
			msg(3, "system", "会话已分配")),
	}
}

func TestRun_FindsAndCountsWithoutRates(t *testing.T) {
	c := Run(sample(), Options{Line: "Shop", Ontology: ontology(t), Packs: []string{"common", "ecommerce"},
		Now: func() time.Time { return at(30) }})

	if c.Conversations != 5 || c.Replies != 6 || c.AIReplies != 2 || c.HumanReplies != 4 || c.CustomerTurns != 6 {
		t.Errorf("counts: conv=%d replies=%d ai=%d human=%d customer=%d", c.Conversations, c.Replies, c.AIReplies, c.HumanReplies, c.CustomerTurns)
	}
	// c1 (unbacked 200元/全额), c2 (claim conflict), c3#2 (质保两年/免费)
	if c.WouldWithhold != 3 {
		t.Errorf("would withhold %d, want 3", c.WouldWithhold)
	}
	// c3#4 全网最低价 is a flag-only redline.
	if c.Flagged != 1 {
		t.Errorf("flagged %d, want 1", c.Flagged)
	}
	if c.Sufficient || c.WouldWithholdRate != nil || c.SelfServe != nil {
		t.Errorf("no rates below %d: sufficient=%v rate=%v selfserve=%v", c.MinSamples, c.Sufficient, c.WouldWithholdRate, c.SelfServe)
	}
	if c.MinSamples != judge.MinShadowSamples {
		t.Errorf("min samples %d", c.MinSamples)
	}
	if len(c.Unbacked) != 2 {
		t.Fatalf("unbacked: %+v", c.Unbacked)
	}
	if f := c.Unbacked[0]; f.ConversationID != "c1" || f.Speaker != SenderHuman || f.Query != "坏了能赔吗" || !strings.Contains(f.Got, "200元") || !f.Blocking {
		t.Errorf("first unbacked: %+v", f)
	}
	if len(c.Violations) != 1 || c.Violations[0].ConversationID != "c2" || c.Violations[0].Kind != domain.ViolationContradicts {
		t.Errorf("violations: %+v", c.Violations)
	}
	if strings.Contains(c.Violations[0].Reply, "[FACT") {
		t.Error("tags stripped from the quoted reply")
	}
	// 全网最低价 hits two terms of the ecommerce pack; both only record.
	if len(c.Redlines) != 2 || c.Redlines[0].Got != "全网最低" || c.Redlines[1].Got != "最低价" || c.Redlines[0].Blocking || c.Redlines[0].Pack != "ecommerce" {
		t.Errorf("redlines: %+v", c.Redlines)
	}
	if len(c.PackCases) == 0 {
		t.Error("pack cases must be included, separately")
	}
	for _, pc := range c.PackCases {
		if !pc.Pass {
			t.Errorf("pack case failed: %+v", pc)
		}
	}
	if len(c.PackReports) != 2 || c.PackReports[1].ID != "ecommerce" || c.PackReports[1].Rep.Headline == "" {
		t.Errorf("pack reports: %+v", c.PackReports)
	}
}

func TestRun_MissingFactsNameCuesNeverValues(t *testing.T) {
	c := Run(sample(), Options{Ontology: ontology(t), Packs: []string{"ecommerce"}})
	if len(c.MissingFacts) == 0 {
		t.Fatal("clusters expected")
	}
	byCue := map[string]MissingFactCluster{}
	for _, m := range c.MissingFacts {
		byCue[m.Cue] = m
		if m.Conversations == 0 || len(m.Examples) == 0 {
			t.Errorf("cluster %+v", m)
		}
	}
	if m, ok := byCue["质保"]; !ok || m.Property != "warranty_months" {
		t.Errorf("质保 should map to the declared property it mentions: %+v (all: %+v)", m, c.MissingFacts)
	}
	// "赔付200元，全额退款" clusters under the promise named first, never
	// under the absolute word.
	if _, ok := byCue["赔付"]; !ok {
		t.Errorf("赔付 cluster missing: %+v", c.MissingFacts)
	}
	for cue := range byCue {
		if cue == "全额" || cue == "免费" {
			t.Errorf("absolute word used as a cluster while a promise word was present: %+v", c.MissingFacts)
		}
	}
	// warranty_months is declared, asked about, and has no assertion.
	if len(c.UnassertedProperties) != 1 || c.UnassertedProperties[0].Property != "warranty_months" || len(c.UnassertedProperties[0].Evidence) != 1 {
		t.Errorf("unasserted properties: %+v", c.UnassertedProperties)
	}
}

func TestRun_WithoutOntology(t *testing.T) {
	c := Run(sample(), Options{Packs: []string{"common"}})
	if c.HasOntology || len(c.Violations) != 0 || len(c.UnassertedProperties) != 0 {
		t.Errorf("no ontology, no facts sections: %+v", c)
	}
	if len(c.Unbacked) != 2 || c.WouldWithhold != 2 {
		t.Errorf("unbacked %d would %d", len(c.Unbacked), c.WouldWithhold)
	}
}

func TestRun_RatesAppearOnceSufficient(t *testing.T) {
	var convs []Conversation
	for i := 0; i < judge.MinShadowSamples; i++ {
		convs = append(convs, conv("k", msg(1, SenderCustomer, "无理由退货窗口是几天"), msg(2, SenderHuman, "可以的，我们赔付200元。")))
	}
	c := Run(convs, Options{Ontology: ontology(t)})
	if !c.Sufficient || c.WouldWithholdRate == nil || *c.WouldWithholdRate != 1 {
		t.Errorf("rate: %+v", c.WouldWithholdRate)
	}
	if c.SelfServe == nil || c.SelfServe.Answerable != judge.MinShadowSamples || c.SelfServe.Rate != 1 {
		t.Errorf("self serve: %+v", c.SelfServe)
	}
}

func TestHTML_RendersAndKeepsItsPromises(t *testing.T) {
	c := Run(sample(), Options{Line: "Shop", Ontology: ontology(t), Packs: []string{"common", "ecommerce"}})
	html, err := c.HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"客服对话体检报告 · Shop", "无依据的承诺（2）", "缺失事实清单", "行业红线命中（2）", "与业务事实冲突（1）",
		"样本不足", "200元", "全网最低", "对抗用例", "不告诉你该写成多少", "数据未离开本机", honestyNote[:20]} {
		if !strings.Contains(s, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(s, "<script") {
		t.Error("no script in a report that gets forwarded")
	}
	if strings.Contains(s, "%</b><span>扣下占比") {
		t.Error("rate shown below the sample floor")
	}
	plain := Run(sample(), Options{Packs: []string{"common"}})
	html, err = plain.HTML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "没有提供业务事实") || strings.Contains(string(html), "与业务事实冲突") {
		t.Error("without an ontology the report says so and omits the facts sections")
	}
}

func TestRedact_MasksAndReportsResidual(t *testing.T) {
	convs := []Conversation{conv("r", msg(1, SenderCustomer, "我手机13812345678"), msg(2, SenderHuman, "好的，已记录"))}
	stats := redact.Stats{}
	if res := Redact(convs, stats); len(res) != 0 {
		t.Errorf("residual: %v", res)
	}
	if convs[0].Messages[0].Text != "我手机〔手机号〕" || stats["mobile"] != 1 {
		t.Errorf("not masked: %+v %v", convs[0].Messages[0], stats)
	}
}

func TestParseChatwootJSON_ArrayAndWrapped(t *testing.T) {
	array := `[{"id": 12, "messages": [
	  {"content": "坏了能赔吗", "message_type": 0, "created_at": 1759300000, "sender": {"type": "contact"}},
	  {"content": "内部备注", "message_type": 1, "private": true, "created_at": 1759300010, "sender": {"type": "user"}},
	  {"content": "", "message_type": 2, "created_at": 1759300011},
	  {"content": "可以赔付200元", "message_type": 1, "created_at": "2026-10-01T06:27:00Z", "sender": {"type": "user"}},
	  {"content": "机器人答复", "message_type": "outgoing", "created_at": 1759300030, "sender": {"type": "agent_bot"}}
	]}, {"display_id": 13, "messages": []}]`
	convs, err := ParseChatwootJSON(strings.NewReader(array))
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 || convs[0].ConversationID != "cw-12" || len(convs[0].Messages) != 3 {
		t.Fatalf("%+v", convs)
	}
	m := convs[0].Messages
	if m[0].Sender != SenderCustomer || m[1].Sender != SenderHuman || m[2].Sender != SenderAI {
		t.Errorf("senders: %s %s %s", m[0].Sender, m[1].Sender, m[2].Sender)
	}
	if m[1].At.Year() != 2026 || m[0].At.Unix() != 1759300000 {
		t.Errorf("times: %v %v", m[0].At, m[1].At)
	}
	wrapped := `{"data": {"payload": ` + array + `}}`
	if convs, err = ParseChatwootJSON(strings.NewReader(wrapped)); err != nil || len(convs) != 1 {
		t.Errorf("wrapped: %v %d", err, len(convs))
	}
	if _, err := ParseChatwootJSON(strings.NewReader(`{"nope": 1}`)); err == nil {
		t.Error("unrecognised shape must error, not yield zero conversations silently")
	}
	if _, err := ParseChatwootJSON(strings.NewReader(`[{"messages":[{"content":"x","message_type":0,"created_at":"yesterday"}]}]`)); err == nil {
		t.Error("missing id must error")
	}
}

func TestParseCSV_MappedColumnsAndAliases(t *testing.T) {
	data := "\ufeff会话,时间,角色,内容\n" +
		"A,2026-10-01 09:00:00,买家,坏了能赔吗\n" +
		"A,2026-10-01 09:01:00,客服,可以赔付200元\n" +
		"B,1759300000,bot,您好\n" +
		"B,1759300001,customer,\n"
	cols, err := ParseCSVColumns("会话,时间,角色,内容")
	if err != nil {
		t.Fatal(err)
	}
	convs, err := ParseCSV(strings.NewReader(data), cols)
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 2 || convs[0].ConversationID != "csv-A" || len(convs[0].Messages) != 2 || len(convs[1].Messages) != 1 {
		t.Fatalf("%+v", convs)
	}
	if convs[0].Messages[0].Sender != SenderCustomer || convs[0].Messages[1].Sender != SenderHuman || convs[1].Messages[0].Sender != SenderAI {
		t.Errorf("senders: %+v", convs)
	}
	if _, err := ParseCSV(strings.NewReader("conversation_id,at,sender,text\n1,,老板,你好\n"), CSVColumns{}); err == nil || !strings.Contains(err.Error(), "unknown sender") {
		t.Errorf("unknown sender must be refused: %v", err)
	}
	if _, err := ParseCSV(strings.NewReader("a,b\n"), CSVColumns{}); err == nil {
		t.Error("missing columns must be refused")
	}
	if _, err := ParseCSVColumns("a,b"); err == nil {
		t.Error("column spec needs four names")
	}
	convs, err = ParseCSV(strings.NewReader("conversation_id,at,sender,text\nZ,,customer,先问\nZ,,human,后答\n"), CSVColumns{})
	if err != nil || convs[0].Messages[0].Text != "先问" || convs[0].Messages[1].Text != "后答" {
		t.Errorf("order: %v %+v", err, convs)
	}
}

func TestRedact_AliasesConversationID(t *testing.T) {
	csvData := "conversation_id,at,sender,text\n13812345678,2026-10-01 09:00:00,customer,坏了能赔吗\n13812345678,2026-10-01 09:01:00,human,可以赔付200元\n"
	convs, err := ParseCSV(strings.NewReader(csvData), CSVColumns{})
	if err != nil {
		t.Fatal(err)
	}
	if res := Redact(convs, redact.Stats{}); len(res) != 0 {
		t.Errorf("residual: %v", res)
	}
	id := convs[0].ConversationID
	if !strings.HasPrefix(id, "conv-") || len(id) != len("conv-")+8 || strings.Contains(id, "13812345678") {
		t.Fatalf("alias: %q", id)
	}
	if again := convAlias("csv-13812345678"); again != id {
		t.Errorf("alias must be stable: %q vs %q", again, id)
	}
	c := Run(convs, Options{Packs: []string{"common"}})
	html, err := c.HTML()
	if err != nil {
		t.Fatal(err)
	}
	js, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "13812345678") || strings.Contains(string(js), "13812345678") {
		t.Error("raw conversation id leaked into html or json")
	}
	if !strings.Contains(string(html), id) {
		t.Error("alias should appear in the report")
	}
	// JSONL 与 Chatwoot 路径同样走别名。
	cw := []Conversation{conv("cw-13812345678", msg(1, SenderHuman, "好的"))}
	Redact(cw, redact.Stats{})
	if strings.Contains(cw[0].ConversationID, "13812345678") {
		t.Error("alias must cover every import path")
	}
}

func TestParseCSV_BOMBeforeQuotedHeader(t *testing.T) {
	data := "\ufeff\"conversation_id\",\"at\",\"sender\",\"text\"\n\"A\",\"2026-10-01 09:00:00\",\"customer\",\"你好\"\n"
	convs, err := ParseCSV(strings.NewReader(data), CSVColumns{})
	if err != nil || len(convs) != 1 || convs[0].Messages[0].Text != "你好" {
		t.Fatalf("%v %+v", err, convs)
	}
	if _, err := ParseCSV(strings.NewReader(""), CSVColumns{}); err == nil {
		t.Error("empty file must still error")
	}
}

func TestParseChatwootJSON_BOMAndNullTime(t *testing.T) {
	data := "\ufeff" + `[{"id": 1, "messages": [
	  {"content": "先问", "message_type": 0, "created_at": null, "sender": {"type": "contact"}},
	  {"content": "后答", "message_type": 1, "created_at": 1759300000, "sender": {"type": "user"}}]}]`
	convs, err := ParseChatwootJSON(strings.NewReader(data))
	if err != nil || len(convs) != 1 || len(convs[0].Messages) != 2 {
		t.Fatalf("%v %+v", err, convs)
	}
	if !convs[0].Messages[0].At.IsZero() {
		t.Error("null created_at must stay zero, not be synthesised")
	}
}

func TestLoadJSONL_BOMOnFirstLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	body := "\ufeff" + `{"conversation_id":"a","messages":[]}` + "\n" + `{"conversation_id":"b","messages":[]}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	convs, err := LoadJSONL(p)
	if err != nil || len(convs) != 2 {
		t.Fatalf("%v %+v", err, convs)
	}
}

func TestParseCSV_MixedEmptyTimesKeepFileOrder(t *testing.T) {
	data := "conversation_id,at,sender,text\nM,2026-10-01 09:05:00,customer,第一\nM,,human,第二\nM,2026-10-01 09:01:00,customer,第三\n"
	convs, err := ParseCSV(strings.NewReader(data), CSVColumns{})
	if err != nil {
		t.Fatal(err)
	}
	m := convs[0].Messages
	if len(m) != 3 || m[0].Text != "第一" || m[1].Text != "第二" || m[2].Text != "第三" {
		t.Errorf("order: %+v", m)
	}
	if !m[1].At.IsZero() {
		t.Error("missing time must stay zero")
	}
	// Run 同样不得重排。
	c := Run(convs, Options{Packs: []string{"common"}})
	if c.Replies != 1 {
		t.Errorf("replies %d", c.Replies)
	}
}

func TestParseCSV_TrailingBlankRowsSkipped(t *testing.T) {
	data := "conversation_id,at,sender,text\nA,,customer,你好\n,,,\n , , ,\n"
	convs, err := ParseCSV(strings.NewReader(data), CSVColumns{})
	if err != nil || len(convs) != 1 {
		t.Fatalf("%v %+v", err, convs)
	}
	if _, err := ParseCSV(strings.NewReader("conversation_id,at,sender,text\n,,customer,有内容无ID\n"), CSVColumns{}); err == nil || !strings.Contains(err.Error(), "empty conversation id") {
		t.Errorf("empty id with text must still error: %v", err)
	}
}

// A promise left unbacked beside a backed one clusters under the cue nearest
// the unbacked word, and never names a property that already has a value:
// the report must not send the reader to fill in what is already written.
func TestRun_MissingFactsSkipBackedNeighbour(t *testing.T) {
	o, err := domain.ParseYAML([]byte(ontologyYAML + "      warranty_months: 12\n"))
	if err != nil {
		t.Fatal(err)
	}
	convs := []Conversation{conv("c1",
		msg(1, SenderCustomer, "保修多久？"),
		msg(2, SenderHuman, "质保12个月，期间非人为损坏免费换新。"))}
	c := Run(convs, Options{Ontology: o, Packs: []string{"ecommerce"}})
	if len(c.Unbacked) != 1 || c.Unbacked[0].Got != "免费" {
		t.Fatalf("want only 免费 unbacked: %+v", c.Unbacked)
	}
	if len(c.MissingFacts) != 1 || c.MissingFacts[0].Cue != "换新" || c.MissingFacts[0].Property != "" {
		t.Errorf("want one 换新 cluster with no property: %+v", c.MissingFacts)
	}
}

func TestCueIn_NearestToUnbackedWord(t *testing.T) {
	groups := [][]string{{"质保", "换新"}, {"赔付", "退款"}, {"全额", "免费"}}
	for _, tc := range []struct{ sentence, got, want string }{
		{"质保12个月，期间非人为损坏免费换新", "免费", "换新"},
		{"质保12个月，期间非人为损坏免费换新", "12个月", "质保"},
		{"可以的，我们赔付200元，全额退款", "200元", "赔付"},
		{"可以的，我们赔付200元，全额退款", "", "赔付"},
		{"全额返还", "全额", "全额"},
	} {
		if got := cueIn(tc.sentence, tc.got, groups); got != tc.want {
			t.Errorf("cueIn(%q, %q) = %q, want %q", tc.sentence, tc.got, got, tc.want)
		}
	}
}
