package domain

import (
	"strings"
	"testing"
)

// The widths and spacings a model produces through a Chinese input method. Each
// must be recognised by all three protocols alike: the point of a shared scanner
// is that no protocol can accept a variant another one rejects (D20).
var tagVariants = []struct {
	name             string
	open, sep, close string
	// pad is placed around the name, the separator and the value.
	pad string
}{
	{"half-width", "[", ":", "]", ""},
	{"full-width lenticular brackets", "【", ":", "】", ""},
	{"full-width square brackets", "［", ":", "］", ""},
	{"full-width colon", "[", "：", "]", ""},
	{"all full-width", "【", "：", "】", ""},
	{"mixed brackets", "【", ":", "]", ""},
	{"mixed brackets reversed", "[", "：", "】", ""},
	{"ascii spaces", "[", ":", "]", " "},
	{"ideographic spaces", "【", "：", "】", "　"},
}

func buildTag(open, name, sep, value, close, pad string) string {
	return open + pad + name + pad + sep + pad + value + pad + close
}

func TestScanTags_AllProtocolsAcceptEveryVariant(t *testing.T) {
	for _, v := range tagVariants {
		for _, tc := range []struct{ name, value string }{
			{TagHandoff, "payout"},
			{TagIntent, "price_inquiry"},
			{TagFact, "return_window_days=15"},
		} {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				answer := "前文。" + buildTag(v.open, tc.name, v.sep, tc.value, v.close, v.pad) + "后文。"
				tags, cleaned := ScanTags(answer, tc.name)
				if len(tags) != 1 {
					t.Fatalf("got %d tags from %q, want 1", len(tags), answer)
				}
				if !tags[0].Separated || tags[0].Value != tc.value || tags[0].Name != tc.name {
					t.Errorf("tag = %+v, want separated %s with value %q", tags[0], tc.name, tc.value)
				}
				if cleaned != "前文。后文。" {
					t.Errorf("cleaned = %q, want the tag removed and nothing else touched", cleaned)
				}
			})
		}
	}
}

func TestScanTags_CaseInsensitiveName(t *testing.T) {
	tags, cleaned := ScanTags("好的【handoff：Payout】", TagHandoff)
	if len(tags) != 1 || tags[0].Value != "Payout" || tags[0].Name != TagHandoff {
		t.Fatalf("tags = %+v", tags)
	}
	if cleaned != "好的" {
		t.Errorf("cleaned = %q", cleaned)
	}
}

// Text that merely resembles a tag is not one. Stripping it would damage a
// correct answer, which is the opposite failure to the one D20 is about.
func TestScanTags_LeavesNonTagsAlone(t *testing.T) {
	for _, answer := range []string{
		"【温馨提示：请保留小票】",
		"[FACTORY] 出厂设置",
		"【FACTS】",
		"[HANDOFF_X:payout]",
		"[INTENTIONAL]",
		"请看第【3】条",
	} {
		for _, name := range []string{TagFact, TagIntent, TagHandoff} {
			tags, cleaned := ScanTags(answer, name)
			if len(tags) != 0 || cleaned != answer {
				t.Errorf("ScanTags(%q, %s) = %+v, %q; want untouched", answer, name, tags, cleaned)
			}
		}
	}
}

// Malformed shapes are still stripped; whether they mean anything is reported
// through Separated and Value.
func TestScanTags_MalformedShapesAreStripped(t *testing.T) {
	for _, tc := range []struct {
		answer    string
		separated bool
		value     string
	}{
		{"好的。[HANDOFF]", false, ""},
		{"好的。【HANDOFF】", false, ""},
		{"好的。[HANDOFF:]", true, ""},
		{"好的。【HANDOFF：】", true, ""},
		{"好的。[HANDOFF payout]", false, ""},
		{"好的。【 HANDOFF 】", false, ""},
	} {
		tags, cleaned := ScanTags(tc.answer, TagHandoff)
		if len(tags) != 1 {
			t.Errorf("%q: got %d tags, want 1", tc.answer, len(tags))
			continue
		}
		if tags[0].Separated != tc.separated || tags[0].Value != tc.value {
			t.Errorf("%q: tag = %+v", tc.answer, tags[0])
		}
		if cleaned != "好的。" {
			t.Errorf("%q: cleaned = %q", tc.answer, cleaned)
		}
	}
}

// Each protocol strips only its own tags; the others are left for their parsers.
func TestScanTags_OnlyNamedProtocol(t *testing.T) {
	answer := "[FACT:a=1]【INTENT：complaint】[HANDOFF:payout]"
	_, cleaned := ScanTags(answer, TagFact)
	if cleaned != "【INTENT：complaint】[HANDOFF:payout]" {
		t.Errorf("cleaned = %q", cleaned)
	}
}

// An unclosed tag of one protocol must not hide a well-formed tag of another
// that follows it — the reason the pattern is per protocol.
func TestScanTags_UnclosedTagDoesNotSwallowAnother(t *testing.T) {
	tags, _ := ScanTags("【INTENT:price 然后 [FACT:a=1]", TagFact)
	if len(tags) != 1 || tags[0].Value != "a=1" {
		t.Errorf("tags = %+v, want the FACT tag found", tags)
	}
}

func TestScanTags_UnknownProtocolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for an unknown protocol")
		}
	}()
	ScanTags("x", "NOPE")
}

func TestSplitTagAssignment(t *testing.T) {
	for _, tc := range []struct {
		in, key, val string
		ok           bool
	}{
		{"a=1", "a", "1", true},
		{"a＝1", "a", "1", true},
		{" 签约前.退费比例 ＝ 80 ", "签约前.退费比例", "80", true},
		{"a=b=c", "a", "b=c", true},
		{"no-equals", "", "", false},
	} {
		key, val, ok := SplitTagAssignment(tc.in)
		if key != tc.key || val != tc.val || ok != tc.ok {
			t.Errorf("SplitTagAssignment(%q) = %q, %q, %v", tc.in, key, val, ok)
		}
	}
}

// The protocol-level parsers built on ScanTags: the D20 shapes now parse to
// the same result as the documented form, and nothing reaches the customer.
func TestParseEscalation_FullWidthForms(t *testing.T) {
	for _, answer := range []string{
		"退款需人工核定。【HANDOFF:payout】",
		"退款需人工核定。[HANDOFF：payout]",
		"退款需人工核定。【HANDOFF：payout】",
		"退款需人工核定。［HANDOFF:payout］",
		"退款需人工核定。【 HANDOFF ： payout 】",
		"退款需人工核定。[handoff：PAYOUT】",
	} {
		got := ParseEscalation(answer)
		if !got.Requested || got.Reason != EscalatePayout {
			t.Errorf("%q: Requested=%v Reason=%q, want payout", answer, got.Requested, got.Reason)
		}
		if got.CleanedAnswer != "退款需人工核定。" {
			t.Errorf("%q: CleanedAnswer = %q", answer, got.CleanedAnswer)
		}
	}
}

func TestParseClaims_FullWidthForms(t *testing.T) {
	for _, answer := range []string{
		"支持15天退货。【FACT:return_window_days=15】",
		"支持15天退货。[FACT：return_window_days=15]",
		"支持15天退货。[FACT:return_window_days＝15]",
		"支持15天退货。【FACT：return_window_days＝15】",
		"支持15天退货。[ FACT : return_window_days = 15 ]",
	} {
		got := ParseClaims(answer)
		if len(got.Claims) != 1 || got.Claims[0].Property != "return_window_days" || got.Claims[0].Value != "15" {
			t.Errorf("%q: claims = %+v", answer, got.Claims)
		}
		if got.CleanedAnswer != "支持15天退货。" {
			t.Errorf("%q: CleanedAnswer = %q", answer, got.CleanedAnswer)
		}
	}
}

func TestParseClaims_FullWidthMalformedStillStripped(t *testing.T) {
	got := ParseClaims("学区以教育局审核为准。【FACT：学位免责】")
	if len(got.Claims) != 0 {
		t.Errorf("claims = %+v, want none from a valueless tag", got.Claims)
	}
	if strings.Contains(got.CleanedAnswer, "FACT") {
		t.Errorf("markup reached the customer: %q", got.CleanedAnswer)
	}
}

// An unclosed tag runs on to the next closer. Its own scan must not strip a
// well-formed tag that follows: intents are parsed before escalations, so an
// unclosed INTENT that swallowed the HANDOFF after it would drop a payout
// escalation without a trace.
func TestScanTags_UnclosedTagDoesNotStripFollower(t *testing.T) {
	for _, tc := range []struct{ answer, name, follower string }{
		{"【INTENT:price_inquiry 好的 [HANDOFF:payout]", TagIntent, "[HANDOFF:payout]"},
		{"[INTENT：complaint 稍等【 handoff ：payout】", TagIntent, "【 handoff ：payout】"},
		{"[FACT:a=1 然后 [HANDOFF]", TagFact, "[HANDOFF]"},
	} {
		tags, cleaned := ScanTags(tc.answer, tc.name)
		if len(tags) != 0 {
			t.Errorf("%q: tags = %+v, want none from an unclosed tag", tc.answer, tags)
		}
		if !strings.Contains(cleaned, tc.follower) {
			t.Errorf("%q: cleaned = %q, follower %q was stripped", tc.answer, cleaned, tc.follower)
		}
	}

	// Same protocol: the closed inner tag is still found and stripped.
	tags, cleaned := ScanTags("[FACT:x 和 [FACT:a=1]", TagFact)
	if len(tags) != 1 || tags[0].Value != "a=1" {
		t.Errorf("tags = %+v, want the closed inner FACT", tags)
	}
	if cleaned != "[FACT:x 和 " {
		t.Errorf("cleaned = %q", cleaned)
	}

	// A bracket in a value that is not a tag keeps the old behaviour.
	tags, _ = ScanTags("[FACT:note=[a]]", TagFact)
	if len(tags) != 1 || tags[0].Value != "note=[a" {
		t.Errorf("tags = %+v", tags)
	}
}

func TestParseEscalation_SurvivesUnclosedIntentBeforeIt(t *testing.T) {
	answer := "补偿需人工核定。【INTENT:complaint [HANDOFF:payout]"
	intents, cleaned := ScanTags(answer, TagIntent)
	if len(intents) != 0 {
		t.Fatalf("intents = %+v", intents)
	}
	got := ParseEscalation(cleaned)
	if !got.Requested || got.Reason != EscalatePayout {
		t.Errorf("Requested=%v Reason=%q, want payout", got.Requested, got.Reason)
	}
}
