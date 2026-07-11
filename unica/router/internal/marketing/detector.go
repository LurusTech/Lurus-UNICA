// Package marketing provides intent detection and proactive marketing logic
// for the router service. It parses intent signals from Dify AI responses and
// strips them from customer-facing messages.
package marketing

import (
	"regexp"
	"strings"
)

// intentTagPattern matches [INTENT:signal_name] tags in Dify responses.
var intentTagPattern = regexp.MustCompile(`\[INTENT:([a-z_]+)\]`)

// ValidIntents defines the set of recognised intent signal names.
var ValidIntents = map[string]bool{
	"price_inquiry":   true,
	"comparison":      true,
	"feature_question": true,
	"purchase_intent": true,
	"complaint":       true,
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
	matches := intentTagPattern.FindAllStringSubmatch(answer, -1)

	seen := make(map[string]bool)
	var intents []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		signal := match[1]
		if !ValidIntents[signal] {
			continue
		}
		if seen[signal] {
			continue
		}
		seen[signal] = true
		intents = append(intents, signal)
	}

	// Strip all intent tags from the answer (including invalid ones)
	cleaned := intentTagPattern.ReplaceAllString(answer, "")
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
