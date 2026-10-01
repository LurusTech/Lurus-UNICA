package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/kefu/unica/pkg/compliance"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/intent"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

// messageHistory is the one extra read the repeat check needs. It is asserted
// from the conversation store rather than added to conversationStore so that a
// store without it — every test fake written before this check existed — simply
// skips the check, which is what "off" already does.
type messageHistory interface {
	GetMessages(ctx context.Context, convID string, limit int) ([]state.Message, error)
}

// The production store must keep satisfying it: the check is found by a type
// assertion, so a signature drift would not fail the build, it would silently
// switch the repeat check off everywhere.
var _ messageHistory = (*state.Manager)(nil)

// repeatHistoryLimit bounds the history read for the repeat check. The store
// returns a conversation's messages oldest first, so the read has to be long
// enough to reach the current message; a conversation longer than this is
// detected by the current message not being last, and the check is skipped
// for it rather than run against the wrong stretch of history.
const repeatHistoryLimit = 200

// accessSignal is one human-access check that fired.
type accessSignal struct {
	reason string
	detail string
}

// humanAccessHandoff runs the human-access checks before triage and before the
// model, and reports whether it handed the conversation to a person.
//
// Off is a single config read and nothing else. Shadow counts what would have
// happened and lets the message carry on exactly as it would have. On hands
// off through the same path triage uses, so the customer gets the holding
// message, the agent gets the briefing, and handoff_events records the reason.
func (r *Router) humanAccessHandoff(ctx context.Context, config *RouteConfig, msg *model.StandardMessage, convID string, workerID int) bool {
	cfg := compliance.LoadHumanAccess(config.ConfigJSON)
	if !cfg.Detects() {
		return false
	}
	sig, ok := r.detectHumanAccess(ctx, cfg, msg, convID, workerID)
	if !ok {
		return false
	}
	metrics.HumanAccessSignalsTotal.WithLabelValues(sig.reason, string(cfg.Mode)).Inc()
	if !cfg.Decides() {
		log.Printf("[router] worker %d: human-access signal for conversation %s (reason=%s, mode=shadow, not acted on): %s",
			workerID, convID, sig.reason, sig.detail)
		return false
	}
	log.Printf("[router] worker %d: human-access handoff for conversation %s (reason=%s): %s",
		workerID, convID, sig.reason, sig.detail)
	r.handoffWithoutAnswer(ctx, config, msg, convID, workerID, sig.reason, sig.detail)
	return true
}

// detectHumanAccess returns the first check that fires, cheapest first: the
// two text checks need nothing but the message, the repeat check reads the
// conversation history.
func (r *Router) detectHumanAccess(ctx context.Context, cfg compliance.HumanAccessConfig, msg *model.StandardMessage, convID string, workerID int) (accessSignal, bool) {
	query := msg.Data.Content.Text
	if entry, ok := intent.QuickEntry(query, cfg.QuickEntries); ok {
		return accessSignal{
			reason: compliance.ReasonQuickEntry,
			detail: fmt.Sprintf("客户发送快捷入口“%s”要求人工", entry),
		}, true
	}
	if cfg.VulnerablePriority {
		if said, ok := intent.Vulnerable(query); ok {
			// The handoff note puts this first, after 【优先接待】, so the
			// agent reads the customer's own words rather than a category.
			return accessSignal{reason: compliance.ReasonVulnerable, detail: "客户自述：" + said}, true
		}
	}
	prior, answered, ok := r.repeatHistory(ctx, msg, convID, cfg.RepeatWindow, workerID)
	if !ok {
		return accessSignal{}, false
	}
	asked := intent.RepeatCount(query, prior, answered) + 1
	if asked >= cfg.RepeatThreshold {
		return accessSignal{
			reason: compliance.ReasonRepeatUnresolved,
			detail: fmt.Sprintf("客户同一问题在 AI 答复后再次提出（连同本条共 %d 次），按重复追问自动转人工", asked),
		}, true
	}
	return accessSignal{}, false
}

// repeatHistory reads the customer's earlier messages in this conversation,
// newest window of them, each flagged with whether an AI answer followed it.
// ok is false when there is no history to read or it cannot be trusted.
func (r *Router) repeatHistory(ctx context.Context, msg *model.StandardMessage, convID string, window, workerID int) ([]string, []bool, bool) {
	h, ok := r.stateManager.(messageHistory)
	if !ok {
		return nil, nil, false
	}
	msgs, err := h.GetMessages(ctx, convID, repeatHistoryLimit)
	if err != nil {
		log.Printf("[router] worker %d: repeat check skipped for conversation %s: %v", workerID, convID, err)
		return nil, nil, false
	}
	return splitHistory(msgs, msg, window)
}

// splitHistory turns a conversation's stored messages into the repeat check's
// input. The current message was stored before routing, so it is the last
// customer message; it is dropped, and if the last customer message is not it
// the history is not the one this message belongs to and nothing is returned.
//
// A customer message counts as answered when an AI message follows it before
// the customer's next message: two customer messages answered by one reply
// are one exchange, not two.
func splitHistory(msgs []state.Message, current *model.StandardMessage, window int) ([]string, []bool, bool) {
	last := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].SenderType == "customer" {
			last = i
			break
		}
	}
	if last < 0 || !isCurrentMessage(msgs[last], current) {
		return nil, nil, false
	}
	var prior []string
	var answered []bool
	for _, m := range msgs[:last] {
		switch m.SenderType {
		case "customer":
			prior = append(prior, messageText(m.ContentJSON))
			answered = append(answered, false)
		case "ai":
			if n := len(answered); n > 0 {
				answered[n-1] = true
			}
		}
	}
	if window > 0 && len(prior) > window {
		prior = prior[len(prior)-window:]
		answered = answered[len(answered)-window:]
	}
	return prior, answered, true
}

// isCurrentMessage matches a stored row to the message being routed, by
// correlation id when both carry one and by text otherwise.
func isCurrentMessage(m state.Message, current *model.StandardMessage) bool {
	if m.CorrelationID != nil && *m.CorrelationID != "" && current.Data.CorrelationID != "" {
		return *m.CorrelationID == current.Data.CorrelationID
	}
	return messageText(m.ContentJSON) == current.Data.Content.Text
}

// messageText reads the text of a stored message's content.
func messageText(raw json.RawMessage) string {
	var c model.MessageContent
	if err := json.Unmarshal(raw, &c); err != nil {
		return ""
	}
	return c.Text
}
