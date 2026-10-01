package routing

import (
	"context"
	"log"

	"github.com/kefu/unica/pkg/compliance"
)

// disclosedKeyPrefix marks a conversation whose first AI answer has been
// labelled, for the "first" mode. It lives as long as the model's own
// multi-turn memory: a customer back after that is starting over, and is told
// again who is answering.
const disclosedKeyPrefix = "ai_disclosed:"

// applyDisclosure returns the text to deliver for an AI answer: the answer
// itself, with the product line's AI label attached when the label is on.
//
// It is applied at delivery, after the judgement, so nothing that judges the
// answer ever sees the label, and the label is never mistaken for part of
// what the model said. The holding message does not come through here: it is
// the tenant's own fixed wording, not generated content.
func (r *Router) applyDisclosure(ctx context.Context, config *RouteConfig, convID, answer string) string {
	if config == nil {
		return answer
	}
	cfg := compliance.LoadDisclosure(config.ConfigJSON)
	switch cfg.Mode {
	case compliance.DisclosureEvery:
		return compliance.ApplyDisclosure(cfg, answer, true)
	case compliance.DisclosureFirst:
		return compliance.ApplyDisclosure(cfg, answer, r.firstDisclosure(ctx, convID))
	}
	return answer
}

// firstDisclosure claims the conversation's first label. When Redis cannot
// say whether the label was already shown, the answer is labelled anyway: a
// second label costs a few characters, a missing one is an AI answer the
// customer took for a person's.
func (r *Router) firstDisclosure(ctx context.Context, convID string) bool {
	if r.rdb == nil {
		return true
	}
	first, err := r.rdb.SetNX(ctx, disclosedKeyPrefix+convID, "1", DifyConvTTL).Result()
	if err != nil {
		log.Printf("[router] disclosure state unavailable for conversation %s, labelling this answer: %v", convID, err)
		return true
	}
	return first
}
