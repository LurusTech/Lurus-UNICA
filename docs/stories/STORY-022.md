# STORY-022: Confidence Scoring + Auto-Handoff Logic

**Epic:** EPIC-003 (AI Smart Response & Knowledge Base)
**Priority:** Must Have
**Story Points:** 3
**Status:** Not Started
**Sprint:** 3
**Created:** 2026-03-05

---

## User Story

As a system, I want AI responses evaluated for confidence and automatically handed off to humans when uncertain, so that customers never receive incorrect answers.

---

## Description

### Background
Not every AI response is reliable. When the knowledge base doesn't cover a topic, or the customer's question is ambiguous, the AI may generate a low-quality answer. This story implements the guardrail layer: confidence scoring evaluates each AI response, and when confidence falls below a configurable threshold, the system auto-triggers handoff to a human agent instead of sending the potentially incorrect AI response to the customer.

Additionally, certain topics (refunds, complaints, legal issues) should always route to humans regardless of AI confidence.

### Scope
**In scope:**
- Confidence threshold evaluation per product line (configurable, default 0.7)
- Auto-handoff trigger when confidence < threshold
- Keyword-based handoff triggers (e.g., "转人工", "投诉", "退款")
- Sensitive topic detection and AI response blocking
- Guardrail rules configurable per product line (stored in DB)
- Integration with STORY-017 handoff mechanism

**Out of scope:**
- Human agent routing and distribution (STORY-018)
- Chatwoot conversation creation (STORY-024)
- Admin UI for guardrail configuration (STORY-033)

### Decision Flow
```
1. AI response received from Dify (STORY-021)
2. Evaluate confidence score:
   a. confidence >= threshold → SEND AI response to customer
   b. confidence < threshold → TRIGGER handoff
3. Check customer message for handoff keywords:
   - If match → TRIGGER handoff (regardless of confidence)
4. Check topic against blocklist:
   - If sensitive topic detected → BLOCK AI response → TRIGGER handoff
5. If handoff triggered:
   a. Do NOT send AI response to customer
   b. Send "正在为您转接人工客服..." holding message
   c. Publish handoff event to unica:handoff stream
   d. Transition conversation state to human_processing
6. Log decision: confidence score, decision (send/handoff), reason
```

---

## Acceptance Criteria

- [ ] Confidence score extracted from Dify response metadata (per STORY-021)
- [ ] Configurable threshold per product line (default 0.7, stored in product_lines.config_json)
- [ ] Below threshold: AI response blocked, handoff triggered automatically
- [ ] Keyword detection: customer message scanned for handoff keywords (configurable list)
- [ ] Keywords matched: handoff triggered regardless of confidence score
- [ ] Sensitive topic blocklist: configurable per product line
- [ ] Blocked topic: AI response suppressed, handoff triggered
- [ ] Holding message sent to customer during handoff ("正在为您转接人工客服，请稍候...")
- [ ] Handoff event published to `unica:handoff` stream with reason
- [ ] Conversation state transitioned to `human_processing`
- [ ] Decision logged with: confidence_score, decision, reason, timestamp
- [ ] Prometheus metrics: handoff rate by reason (low_confidence, keyword, blocked_topic)

---

## Technical Notes

### Guardrail Configuration (per product line)
```go
type GuardrailConfig struct {
    ConfidenceThreshold float64  `json:"confidence_threshold"` // default 0.7
    HandoffKeywords     []string `json:"handoff_keywords"`     // ["转人工", "人工客服", "投诉", "退款"]
    BlockedTopics       []string `json:"blocked_topics"`       // ["legal", "refund_policy_exception"]
    HoldingMessage      string   `json:"holding_message"`      // "正在为您转接人工客服，请稍候..."
}
```

Stored in `product_lines.config_json`:
```json
{
  "guardrail": {
    "confidence_threshold": 0.7,
    "handoff_keywords": ["转人工", "人工客服", "投诉", "退款", "找人工"],
    "blocked_topics": [],
    "holding_message": "正在为您转接人工客服，请稍候..."
  }
}
```

### Confidence Evaluator
```go
// unica/router/internal/guardrail/evaluator.go

type Decision string
const (
    DecisionSend    Decision = "send"
    DecisionHandoff Decision = "handoff"
)

type EvalResult struct {
    Decision   Decision
    Reason     string   // "confidence_ok", "low_confidence", "keyword_match", "blocked_topic"
    Confidence float64
    MatchedKeyword string
}

type Evaluator struct {
    configCache map[string]*GuardrailConfig // product_line_id → config
}

func (e *Evaluator) Evaluate(ctx context.Context, productLineID string, customerMsg string, confidence float64) *EvalResult {
    config := e.getConfig(productLineID)

    // 1. Keyword check (highest priority)
    for _, kw := range config.HandoffKeywords {
        if strings.Contains(customerMsg, kw) {
            return &EvalResult{Decision: DecisionHandoff, Reason: "keyword_match", MatchedKeyword: kw, Confidence: confidence}
        }
    }

    // 2. Confidence check
    if confidence < config.ConfidenceThreshold {
        return &EvalResult{Decision: DecisionHandoff, Reason: "low_confidence", Confidence: confidence}
    }

    // 3. Pass
    return &EvalResult{Decision: DecisionSend, Reason: "confidence_ok", Confidence: confidence}
}
```

### Integration with Router
```go
// In router message processing loop:
aiResp := difyClient.Chat(ctx, config, msg, convID)
confidence := calculateConfidence(aiResp)

evalResult := evaluator.Evaluate(ctx, productLineID, customerMessage, confidence)

switch evalResult.Decision {
case DecisionSend:
    // Publish AI response to outbound stream
    publishOutbound(aiResp.Answer)
case DecisionHandoff:
    // Send holding message
    publishOutbound(guardrailConfig.HoldingMessage)
    // Trigger handoff (STORY-017)
    publishHandoff(conversationID, evalResult.Reason, aiResp)
    // Transition state
    stateManager.Transition(conversationID, StateHumanProcessing)
}
```

### Handoff Event Format
```json
{
  "type": "conversation.handoff",
  "conversation_id": "...",
  "product_line_id": "...",
  "reason": "low_confidence",
  "confidence_score": 0.45,
  "ai_response_suppressed": "The AI response that was NOT sent to customer",
  "customer_message": "Original customer message",
  "intent_summary": "Customer asking about refund for defective product",
  "timestamp": "2026-03-05T10:30:00Z"
}
```

### Metrics
```
router_guardrail_decisions_total     counter  {decision=send|handoff, reason=confidence_ok|low_confidence|keyword_match|blocked_topic}
router_guardrail_confidence_scores   histogram  Confidence score distribution at decision time
router_handoff_rate                  gauge     Rolling handoff rate per product line
```

### Components
- `unica/router/internal/guardrail/evaluator.go` — Confidence + keyword + topic evaluation
- `unica/router/internal/guardrail/config.go` — Guardrail config loading and caching
- `unica/router/internal/routing/router.go` — Integration point (post-Dify response)

---

## Dependencies

**Prerequisite:**
- STORY-021 (Router-Dify Integration — provides confidence scores)

**Blocks:**
- STORY-017 (AI→Human Handoff — consumes handoff events from this story)

---

## Definition of Done

- [ ] Confidence threshold configurable per product line
- [ ] Below-threshold responses trigger handoff automatically
- [ ] Keyword detection triggers handoff on match
- [ ] Holding message sent to customer on handoff
- [ ] Handoff event published with reason and context
- [ ] Conversation state transitions to human_processing
- [ ] Prometheus metrics for handoff decisions exposed
- [ ] Unit tests for Evaluator: threshold, keywords, edge cases (>=80% coverage)
- [ ] Integration test: low-confidence response → handoff triggered → state changed
- [ ] Code committed to `unica/router/`

---

## Story Points Breakdown

- **Evaluator logic + config:** 1.5 points
- **Router integration + handoff event:** 0.5 points
- **Testing:** 1 point
- **Total:** 3 points

**Rationale:** Low-moderate complexity — the evaluation logic is straightforward (threshold + keyword match), but requires careful integration with the Router's message processing flow and correct handoff event emission.
