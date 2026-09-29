package replay

import (
	"regexp"
	"sort"
)

// Redaction runs on the customer's machine, before an export leaves it. It is
// deliberately pattern-based and over-eager: a masked order number costs the
// evolution loop nothing, a leaked phone number costs the customer's trust.
//
// Two layers precede this one and are not repeated here. The export query
// never selects the customers table, conversation metadata or Chatwoot
// contacts, and it replaces each conversation's own customer display name
// before any text leaves the database. What remains is free text a customer
// or agent typed, which is what these rules cover.
//
// Placeholders use 〔〕, which no rule matches, so redaction is idempotent and
// a placeholder is never mistaken for data by a later rule.

type redactRule struct {
	name string
	re   *regexp.Regexp
	// keep preserves the first capture group verbatim — the label in
	// "订单号：123" — so the text still says what was masked.
	keep bool
	mask string
	// strict rules also run as the residual check after redaction; contextual
	// rules would re-fire on their own placeholders' labels and are
	// redaction-only.
	strict bool
}

var redactRules = []redactRule{
	{name: "email", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		mask: "〔邮箱〕", strict: true},
	{name: "id_card", re: regexp.MustCompile(`\b\d{17}[\dXx]\b`), mask: "〔身份证〕", strict: true},
	{name: "mobile", re: regexp.MustCompile(`(?:\+86[- ]?|\b(?:86[- ]?)?)1[3-9]\d{9}\b`), mask: "〔手机号〕", strict: true},
	{name: "mobile_spaced", re: regexp.MustCompile(`\b1[3-9]\d[- ]\d{4}[- ]\d{4}\b`), mask: "〔手机号〕", strict: true},
	{name: "order_labelled",
		re:   regexp.MustCompile(`((?:订单|运单|快递单|物流单|售后单)(?:号|编号)?[:：\s]*)([A-Za-z0-9][A-Za-z0-9-]{5,})`),
		keep: true, mask: "〔订单号〕"},
	{name: "bank_card", re: regexp.MustCompile(`\b\d{16,19}\b`), mask: "〔卡号〕", strict: true},
	{name: "long_number", re: regexp.MustCompile(`\b[A-Za-z]{0,4}\d{10,}\b`), mask: "〔单号〕", strict: true},
	{name: "landline", re: regexp.MustCompile(`\b0\d{2,3}[- ]?\d{7,8}\b`), mask: "〔电话〕", strict: true},
	{name: "address_labelled",
		re:   regexp.MustCompile(`((?:收货地址|收件地址|详细地址|地址|住址|寄到|送到)[:：\s]*)([^\s，。,；;！!？?〔]{4,40})`),
		keep: true, mask: "〔地址〕"},
	{name: "address",
		re:   regexp.MustCompile(`\p{Han}{2,8}(?:省|市|自治区)\p{Han}{0,12}(?:区|县|镇|乡|街道)[\p{Han}\d\-]{0,20}(?:路|街|道|巷|弄|村|号|栋|幢|单元|室|楼)[\p{Han}\d\-号栋幢单元室楼]*`),
		mask: "〔地址〕"},
	{name: "street",
		re:   regexp.MustCompile(`\p{Han}{1,10}(?:路|街|大道|巷|弄)\d+号[\p{Han}\d\-号栋幢单元室楼]*`),
		mask: "〔地址〕"},
	{name: "name_labelled",
		re:   regexp.MustCompile(`((?:我叫|本人姓名|姓名|收货人|收件人|联系人|户名)[:：\s]*)(\p{Han}{2,3})`),
		keep: true, mask: "〔姓名〕"},
	{name: "honorific",
		re:   regexp.MustCompile(`\p{Han}(先生|女士|小姐)`),
		mask: "〔姓〕$1"},
	{name: "im_handle",
		re:   regexp.MustCompile(`((?:微信号?|vx|VX|wx|WX)[:：\s]*)([A-Za-z][-_A-Za-z0-9]{5,19})`),
		keep: true, mask: "〔微信号〕"},
	{name: "qq",
		re:   regexp.MustCompile(`((?:QQ|qq)号?[:：\s]*)(\d{5,11})`),
		keep: true, mask: "〔QQ号〕"},
}

// RedactStats counts how many times each rule fired.
type RedactStats map[string]int

// RedactText masks personal data in one string.
func RedactText(s string, stats RedactStats) string {
	if s == "" {
		return s
	}
	for _, r := range redactRules {
		n := 0
		if r.keep {
			s = r.re.ReplaceAllStringFunc(s, func(m string) string {
				n++
				sub := r.re.FindStringSubmatch(m)
				return sub[1] + r.mask
			})
		} else {
			s = r.re.ReplaceAllStringFunc(s, func(m string) string {
				n++
				return r.re.ReplaceAllString(m, r.mask)
			})
		}
		if stats != nil && n > 0 {
			stats[r.name] += n
		}
	}
	return s
}

// Residual lists what the strict rules still find in s. After RedactText it
// must be empty; a non-empty result means a rule regressed and the export must
// not leave the machine.
func Residual(s string) []string {
	var out []string
	for _, r := range redactRules {
		if !r.strict {
			continue
		}
		for _, m := range r.re.FindAllString(s, -1) {
			out = append(out, r.name+": "+m)
		}
	}
	return out
}

// Redact masks every free-text field of a conversation in place.
func Redact(c *Conversation, stats RedactStats) {
	for i := range c.Messages {
		c.Messages[i].Text = RedactText(c.Messages[i].Text, stats)
	}
	for i := range c.Handoffs {
		h := &c.Handoffs[i]
		h.Detail = RedactText(h.Detail, stats)
		h.CustomerMessage = RedactText(h.CustomerMessage, stats)
		h.SuppressedAnswer = RedactText(h.SuppressedAnswer, stats)
	}
	for i := range c.Traces {
		t := &c.Traces[i]
		t.CustomerMessage = RedactText(t.CustomerMessage, stats)
		t.RawAnswer = RedactText(t.RawAnswer, stats)
		for j := range t.Violations {
			v := &t.Violations[j]
			v.Got = RedactText(v.Got, stats)
			v.Want = RedactText(v.Want, stats)
			v.Message = RedactText(v.Message, stats)
			v.Evidence = RedactText(v.Evidence, stats)
		}
	}
}

// ConversationResidual runs Residual over every free-text field and returns
// the findings, prefixed with where they were found.
func ConversationResidual(c Conversation) []string {
	var out []string
	add := func(where, s string) {
		for _, r := range Residual(s) {
			out = append(out, c.ConversationID+" "+where+" "+r)
		}
	}
	for _, m := range c.Messages {
		add("message", m.Text)
	}
	for _, h := range c.Handoffs {
		add("handoff.detail", h.Detail)
		add("handoff.customer_message", h.CustomerMessage)
		add("handoff.suppressed_answer", h.SuppressedAnswer)
	}
	for _, t := range c.Traces {
		add("trace.customer_message", t.CustomerMessage)
		add("trace.raw_answer", t.RawAnswer)
		for _, v := range t.Violations {
			add("trace.violation", v.Got+" "+v.Want+" "+v.Message+" "+v.Evidence)
		}
	}
	return out
}

// Names returns the rule names in a stable order, for reporting.
func (s RedactStats) Names() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
