// Package marketing provides intent detection and proactive marketing logic
// for the router service. It parses intent signals from Dify AI responses and
// strips them from customer-facing messages.
package marketing

import (
	"regexp"
	"strings"

	"github.com/kefu/unica/pkg/domain"
)

// intentNamePattern is what a recognised intent value looks like once the tag
// itself has been found. Tag recognition is domain.ScanTags, shared with the
// [FACT:] and [HANDOFF:] protocols, so case, full-width brackets, a full-width
// colon and stray spaces are tolerated here exactly as they are there (D20).
//
// The value is lowercased before it is looked up. A case-sensitive match left
// [INTENT:PRICE_INQUIRY] in the customer-facing text — the tag protocols exist
// so customers never see internal markup, and case is not something a model
// reproduces reliably.
var intentNamePattern = regexp.MustCompile(`^[a-z_]+$`)

// ValidIntents defines the set of recognised intent signal names.
var ValidIntents = map[string]bool{
	"price_inquiry":    true,
	"comparison":       true,
	"feature_question": true,
	"purchase_intent":  true,
	"complaint":        true,
}

// DetectResult holds the output of intent detection on an AI response.
type DetectResult struct {
	// CleanedAnswer is the AI response with all [INTENT:xxx] tags removed.
	CleanedAnswer string
	// Intents is the deduplicated list of valid intent signals found.
	Intents []string
}

// DetectIntents parses [INTENT:xxx] tags from a Dify AI response, strips them
// from the customer-facing message, and returns the cleaned answer along with
// any valid intents detected. Unknown intent names are silently ignored.
func DetectIntents(answer string) DetectResult {
	tags, cleaned := domain.ScanTags(answer, domain.TagIntent)

	seen := make(map[string]bool)
	var intents []string
	for _, tag := range tags {
		signal := strings.ToLower(tag.Value)
		if !tag.Separated || !intentNamePattern.MatchString(signal) {
			continue
		}
		if !ValidIntents[signal] {
			continue
		}
		if seen[signal] {
			continue
		}
		seen[signal] = true
		intents = append(intents, signal)
	}

	// Every intent tag is stripped, including invalid and malformed ones: a
	// tag that names nothing we track is still markup the customer must not see.
	// Collapse extra whitespace left after tag removal
	cleaned = strings.TrimSpace(cleaned)
	// Remove double spaces
	for strings.Contains(cleaned, "  ") {
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
	}

	return DetectResult{
		CleanedAnswer: cleaned,
		Intents:       intents,
	}
}
