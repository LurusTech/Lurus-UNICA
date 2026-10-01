package intent

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/kefu/unica/pkg/compliance"
)

// The human-access checks: a whole-message quick entry to a person, a customer
// who says they need extra help with the channel itself, and a question asked
// again after the AI already answered it. Pure functions; whether any of them
// changes routing is the product line's human_access switch, read by the
// router.

// QuickEntry reports whether the whole message, once whitespace and
// punctuation are removed, equals one of entries. Only the whole message: "0"
// must not fire on "0元购吗", nor "人工" on "人工费多少", which are questions
// the AI should answer.
func QuickEntry(msg string, entries []string) (string, bool) {
	norm := compliance.NormalizeUtterance(msg)
	if norm == "" {
		return "", false
	}
	for _, e := range entries {
		if n := compliance.NormalizeUtterance(e); n != "" && n == norm {
			return e, true
		}
	}
	return "", false
}

// vulnerableMarkers are things a customer says about themselves that mean the
// channel is hard for them to use. Ambiguous forms were left out on purpose:
// "我是学生" (a student is not a minor), "看不清" (almost always a picture
// that will not load) and "听不懂" (as often the jargon as the person) would
// put ordinary questions at the head of the queue, and a priority lane that
// everyone is in prioritises no one.
var vulnerableMarkers = []string{
	"我是老人", "老年人", "年纪大", "岁数大", "不会用手机", "未成年",
}

// selfAge matches a stated age of the customer themselves: "我16岁",
// "我今年16岁", "我才16岁", "我今年17周岁" (the form used on forms and by
// parents, so it is common in exactly the conversations this is for). The 我
// has to be right there, so "我孩子16岁" and "我们店开了16年" do not match.
var selfAge = regexp.MustCompile(`我(?:今年)?才?(\d{1,2})周?岁`)

// adultAge is the age at which a stated age stops counting as a minor.
const adultAge = 18

// Vulnerable reports whether the customer says they are elderly, a minor, or
// unable to use the phone well, and returns what they said.
func Vulnerable(msg string) (string, bool) {
	text := foldDigits(msg)
	for _, m := range vulnerableMarkers {
		if strings.Contains(text, m) {
			return m, true
		}
	}
	for _, sub := range selfAge.FindAllStringSubmatch(text, -1) {
		if age, err := strconv.Atoi(sub[1]); err == nil && age < adultAge {
			return sub[0], true
		}
	}
	return "", false
}

// foldDigits turns full-width digits into ASCII so "我１６岁" reads like
// "我16岁"; the regexp's \d is ASCII only.
func foldDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		return r
	}, s)
}

// Repeat-check thresholds. Four characters is the shortest question whose
// character pairs say anything: below it, "好的" and "在吗" would count as the
// same question asked twice. 0.6 Jaccard over character pairs tolerates a
// changed particle or word order and not a different question.
const (
	minRepeatRunes   = 4
	repeatSimilarity = 0.6
)

// RepeatCount returns how many of the earlier customer messages ask the same
// thing as current and were answered by the AI. answeredAfter[i] says whether
// an AI answer followed priorCustomer[i] before the customer wrote again; a
// message the AI never answered is a customer typing twice, not a loop.
func RepeatCount(current string, priorCustomer []string, answeredAfter []bool) int {
	cur := compliance.NormalizeUtterance(current)
	if len([]rune(cur)) < minRepeatRunes {
		return 0
	}
	curPairs := charPairs(cur)
	count := 0
	for i, prior := range priorCustomer {
		if i >= len(answeredAfter) || !answeredAfter[i] {
			continue
		}
		p := compliance.NormalizeUtterance(prior)
		if len([]rune(p)) < minRepeatRunes {
			continue
		}
		if jaccard(curPairs, charPairs(p)) >= repeatSimilarity {
			count++
		}
	}
	return count
}

// charPairs is the set of adjacent character pairs of s.
func charPairs(s string) map[string]bool {
	runes := []rune(s)
	out := make(map[string]bool, len(runes))
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}
