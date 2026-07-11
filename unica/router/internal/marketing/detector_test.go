package marketing

import (
	"testing"
)

func TestDetectIntents_NoTags(t *testing.T) {
	result := DetectIntents("This product costs 299 yuan.")
	if result.CleanedAnswer != "This product costs 299 yuan." {
		t.Errorf("expected unchanged answer, got %q", result.CleanedAnswer)
	}
	if len(result.Intents) != 0 {
		t.Errorf("expected no intents, got %v", result.Intents)
	}
}

func TestDetectIntents_SingleTag(t *testing.T) {
	result := DetectIntents("[INTENT:price_inquiry] This product costs 299 yuan.")
	if result.CleanedAnswer != "This product costs 299 yuan." {
		t.Errorf("expected cleaned answer, got %q", result.CleanedAnswer)
	}
	if len(result.Intents) != 1 || result.Intents[0] != "price_inquiry" {
		t.Errorf("expected [price_inquiry], got %v", result.Intents)
	}
}

func TestDetectIntents_MultipleTags(t *testing.T) {
	result := DetectIntents("[INTENT:price_inquiry][INTENT:comparison] Our product is better and cheaper.")
	if len(result.Intents) != 2 {
		t.Fatalf("expected 2 intents, got %v", result.Intents)
	}
	if result.Intents[0] != "price_inquiry" || result.Intents[1] != "comparison" {
		t.Errorf("expected [price_inquiry, comparison], got %v", result.Intents)
	}
	if result.CleanedAnswer != "Our product is better and cheaper." {
		t.Errorf("unexpected cleaned answer: %q", result.CleanedAnswer)
	}
}

func TestDetectIntents_DuplicateTags(t *testing.T) {
	result := DetectIntents("[INTENT:price_inquiry] Price is 100. [INTENT:price_inquiry] Special offer!")
	if len(result.Intents) != 1 {
		t.Errorf("expected 1 deduplicated intent, got %v", result.Intents)
	}
}

func TestDetectIntents_InvalidTag(t *testing.T) {
	result := DetectIntents("[INTENT:unknown_signal] Hello")
	if len(result.Intents) != 0 {
		t.Errorf("expected no valid intents for unknown signal, got %v", result.Intents)
	}
	// Tag should still be stripped from customer-facing message
	if result.CleanedAnswer != "Hello" {
		t.Errorf("expected tag stripped even if invalid, got %q", result.CleanedAnswer)
	}
}

func TestDetectIntents_TagInMiddle(t *testing.T) {
	result := DetectIntents("The price is 299. [INTENT:purchase_intent] Would you like to order?")
	if len(result.Intents) != 1 || result.Intents[0] != "purchase_intent" {
		t.Errorf("expected [purchase_intent], got %v", result.Intents)
	}
	if result.CleanedAnswer != "The price is 299. Would you like to order?" {
		t.Errorf("unexpected cleaned answer: %q", result.CleanedAnswer)
	}
}

func TestDetectIntents_AllValidTypes(t *testing.T) {
	answer := "[INTENT:price_inquiry][INTENT:comparison][INTENT:feature_question][INTENT:purchase_intent][INTENT:complaint] Response"
	result := DetectIntents(answer)
	if len(result.Intents) != 5 {
		t.Errorf("expected 5 intents, got %d: %v", len(result.Intents), result.Intents)
	}
}

func TestDetectIntents_EmptyAnswer(t *testing.T) {
	result := DetectIntents("")
	if result.CleanedAnswer != "" {
		t.Errorf("expected empty answer, got %q", result.CleanedAnswer)
	}
	if len(result.Intents) != 0 {
		t.Errorf("expected no intents, got %v", result.Intents)
	}
}

func TestDetectIntents_OnlyTags(t *testing.T) {
	result := DetectIntents("[INTENT:price_inquiry]")
	if result.CleanedAnswer != "" {
		t.Errorf("expected empty cleaned answer, got %q", result.CleanedAnswer)
	}
	if len(result.Intents) != 1 {
		t.Errorf("expected 1 intent, got %v", result.Intents)
	}
}
