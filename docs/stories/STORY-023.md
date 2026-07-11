# STORY-023: Proactive Marketing Conversation Logic

**Epic:** EPIC-003 (AI Smart Response & Knowledge Base)
**Priority:** Should Have
**Story Points:** 5
**Status:** Not Started
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 6

---

## User Story

As a business,
I want AI to identify acquisition intent and proactively recommend products,
So that customer inquiries convert to leads.

---

## Description

### Background
Customer service conversations often contain purchase intent signals (price inquiries, product comparisons, feature questions). Currently, the AI only answers questions reactively. This story adds intent detection and proactive marketing logic: when the AI detects acquisition intent, it can proactively recommend products, share promotions, or guide the customer toward a purchase decision. Conversion events are tracked for reporting.

### Scope
**In scope:**
- Intent signal detection via Dify prompt engineering and response metadata
- Configurable intent signal definitions per product line (price inquiry, comparison, etc.)
- Proactive marketing message templates configurable per product line in Dify prompts
- Conversion event tracking: tag conversations with detected intents
- Conversion metrics stored in conversation metadata for STORY-029 reports
- Integration with existing router pipeline (post-AI-response hook)

**Out of scope:**
- Push marketing to inactive customers (outbound campaigns)
- Discount/coupon generation
- CRM integration (future enhancement)
- A/B testing of marketing messages

### User Flow
1. Customer asks: "这款产品多少钱？" (How much is this product?)
2. AI detects "price inquiry" intent signal
3. AI responds with price info AND proactive recommendation: related products, current promotion
4. Conversation tagged with `intent:price_inquiry` in metadata
5. Customer asks: "跟XX品牌比怎么样？" (How does it compare to XX brand?)
6. AI detects "comparison" intent, responds with competitive advantages
7. Conversation tagged with `intent:comparison`
8. Supervisor sees conversion funnel metrics in STORY-029 reports

---

## Acceptance Criteria

- [ ] Intent signals configurable per product line (stored in `ai_agent_configs` table)
- [ ] Default signals: price_inquiry, comparison, feature_question, purchase_intent, complaint
- [ ] AI detects intent signals from customer messages (via Dify prompt engineering)
- [ ] Proactive marketing messages triggered when intent detected
- [ ] Marketing message templates configurable per product line in Dify system prompt
- [ ] Conversion events tracked: intent type stored in conversation metadata JSON
- [ ] Multiple intents can be tracked per conversation (array in metadata)
- [ ] Intent detection does not block or delay normal AI response flow
- [ ] Conversion event metrics queryable for reporting (STORY-029)
- [ ] No proactive marketing during human agent sessions (AI-only)

---

## Technical Notes

### Components
- **Router service:** New `marketing` package for intent detection and proactive logic
- **Dify:** Updated system prompts with intent classification instructions
- **Database:** Extend conversation metadata JSON to include `intents` array

### Architecture

Intent detection is embedded in the Dify system prompt, not a separate model call:

```
System Prompt Addition:
---
When responding, also analyze the customer's intent. If you detect any of the following signals, include them in your response metadata:
- price_inquiry: Customer asks about pricing
- comparison: Customer compares with competitors
- feature_question: Customer asks about specific features
- purchase_intent: Customer expresses desire to buy
- complaint: Customer expresses dissatisfaction

Format your intent analysis as: [INTENT:signal_name]
If you detect purchase-related intent, proactively include relevant product recommendations or current promotions in your response.
---
```

### Router Integration

```
router/internal/marketing/
  detector.go          -- Parse intent signals from Dify response
  detector_test.go
  tracker.go           -- Store intent events in conversation metadata
  tracker_test.go
```

**Post-response hook in routing pipeline:**
1. Router receives Dify response
2. Marketing detector parses `[INTENT:xxx]` tags from response
3. Tags stripped from customer-facing message
4. Intent events stored in conversation metadata
5. Metrics incremented: `marketing_intent_detected_total{intent_type, product_line}`

### Database Changes
```sql
-- Conversation metadata already supports JSONB
-- Add intent tracking:
-- conversations.metadata -> {"intents": ["price_inquiry", "comparison"], "intent_timestamps": {"price_inquiry": "2026-03-06T10:00:00Z"}}
```

### New Metrics
```go
// marketing_intent_detected_total counts detected intent signals by type and product line
MarketingIntentDetectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "marketing_intent_detected_total",
    Help: "Total marketing intent signals detected",
}, []string{"intent_type", "product_line"})

// marketing_proactive_messages_total counts proactive marketing messages sent
MarketingProactiveMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "marketing_proactive_messages_total",
    Help: "Total proactive marketing messages sent",
}, []string{"product_line"})
```

### Configuration (via STORY-033 AI Config API)
```json
{
  "marketing_intents": [
    {"signal": "price_inquiry", "enabled": true},
    {"signal": "comparison", "enabled": true},
    {"signal": "feature_question", "enabled": true},
    {"signal": "purchase_intent", "enabled": true},
    {"signal": "complaint", "enabled": false}
  ],
  "proactive_marketing_enabled": true
}
```

---

## Dependencies

**Prerequisite Stories:**
- STORY-021: Router-Dify Integration (AI response pipeline must work)
- STORY-033: AI Agent Configuration UI (for configuring intent signals, optional - can use DB directly)

**Blocked Stories:**
- None

**External Dependencies:**
- Dify system prompts updated with intent classification instructions

---

## Definition of Done

- [ ] Marketing detector parses intent signals from Dify responses
- [ ] Intent tags stripped from customer-facing messages
- [ ] Intents stored in conversation metadata JSONB
- [ ] Unit tests for detector and tracker (>80% coverage)
- [ ] Integration test: send price inquiry → verify intent detected and stored
- [ ] Proactive marketing messages appear naturally in AI responses
- [ ] No proactive marketing during human agent sessions
- [ ] Metrics emitted for intent detection events
- [ ] Dify system prompts updated for at least 2 product lines as examples
- [ ] Router service deploys with marketing package

---

## Story Points Breakdown

- **Intent detector + parser:** 1.5 points
- **Intent tracker + DB storage:** 1 point
- **Router pipeline integration:** 1 point
- **Dify prompt engineering:** 0.5 points
- **Testing:** 1 point
- **Total:** 5 points

**Rationale:** Core logic is lightweight (regex/string parsing for intent tags). Main complexity is clean integration with existing router pipeline and ensuring intent tags don't leak to customers.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created

**Actual Effort:** TBD
