# STORY-021: Router-Dify Integration - Message Flow

**Epic:** EPIC-003 (AI Smart Response & Knowledge Base)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 3
**Created:** 2026-03-05

---

## User Story

As a customer, I want AI responses powered by product knowledge, so that I receive accurate answers to my product questions.

---

## Description

### Background
STORY-016 established the basic Router→Dify call path (simple chat API call). This story deepens that integration to leverage Dify's RAG capabilities: the Router sends customer messages with conversation context, Dify retrieves relevant knowledge chunks, generates a grounded response with metadata (confidence, token usage, retrieval sources), and the Router processes the response for downstream handling.

This is the core of UNICA's AI intelligence — without proper RAG integration, the AI is just a generic chatbot.

### Scope
**In scope:**
- Enhanced Dify chat API integration with conversation context
- RAG retrieval metadata extraction (sources, relevance scores)
- Conversation history management (Dify conversation_id tracking)
- Response metadata processing (confidence, tokens, sources)
- Streaming response support (optional, for future real-time delivery)
- Full round-trip latency target: customer message → AI response < 3s
- Retry logic for Dify API failures
- Prometheus metrics for AI response quality

**Out of scope:**
- Confidence-based handoff decisions (STORY-022)
- Proactive marketing logic (STORY-023)
- Knowledge base management (STORY-020)

### Enhanced Message Flow
```
1. Router receives inbound message from Redis Stream
2. Lookup conversation context:
   - Dify conversation_id from Redis session cache
   - Recent message history (last N messages for context window)
3. Call Dify Chat API:
   POST /v1/chat-messages
   {
     "inputs": {
       "customer_name": "...",
       "channel": "wechat",
       "product_line": "..."
     },
     "query": "customer message text",
     "user": "customer_id",
     "conversation_id": "dify_conv_id or empty for new",
     "response_mode": "blocking"
   }
4. Process Dify response:
   - Extract answer text
   - Extract metadata: confidence, token usage, retrieval sources
   - Store Dify conversation_id in session cache (for continuity)
5. Store AI response as message in DB:
   - content_json includes answer + metadata
   - confidence_score stored for STORY-022
6. Publish to unica:outbound stream
7. Track metrics: response time, token usage, retrieval hit/miss
```

---

## Acceptance Criteria

- [ ] Router calls Dify chat API with customer message + conversation context inputs
- [ ] Dify retrieves relevant knowledge chunks via RAG (verified in response metadata)
- [ ] AI response returned with confidence score extracted from metadata
- [ ] Dify conversation_id tracked in Redis session for multi-turn conversations
- [ ] Response published to `unica:outbound` stream with correct routing info
- [ ] Full round-trip: customer message → AI response < 3s (P95)
- [ ] Retry on Dify API failure: 2 retries with 1s/2s backoff
- [ ] After 3 failures: conversation state set to pending, alert event emitted
- [ ] RAG retrieval sources logged for debugging/auditing
- [ ] Prometheus metrics: dify response time, token usage, retrieval hit rate
- [ ] Multi-turn conversation works (Dify maintains context across messages)

---

## Technical Notes

### Enhanced Dify Client
```go
// unica/router/internal/bridge/dify.go (enhanced from STORY-016)

type DifyChatRequest struct {
    Inputs         map[string]string `json:"inputs"`
    Query          string            `json:"query"`
    User           string            `json:"user"`
    ConversationID string            `json:"conversation_id,omitempty"`
    ResponseMode   string            `json:"response_mode"`
}

type DifyChatResponse struct {
    Answer         string            `json:"answer"`
    ConversationID string            `json:"conversation_id"`
    MessageID      string            `json:"message_id"`
    Metadata       DifyMetadata      `json:"metadata"`
    RetrieverResources []RetrieverResource `json:"retriever_resources"`
}

type DifyMetadata struct {
    Usage struct {
        TotalTokens      int `json:"total_tokens"`
        PromptTokens     int `json:"prompt_tokens"`
        CompletionTokens int `json:"completion_tokens"`
    } `json:"usage"`
}

type RetrieverResource struct {
    DatasetID   string  `json:"dataset_id"`
    DatasetName string  `json:"dataset_name"`
    DocumentID  string  `json:"document_id"`
    DocumentName string `json:"document_name"`
    Score       float64 `json:"score"`
    Content     string  `json:"content"`
}
```

### Conversation Context in Redis
```
Key:   dify_conv:{conversation_id}
Value: {
    "dify_conversation_id": "...",
    "product_line_id": "...",
    "last_active": "timestamp",
    "message_count": 5
}
TTL:   24h (conversation context expires after inactivity)
```

### Confidence Score Extraction
Dify doesn't natively return a single "confidence" score. Strategy:
1. **Primary:** Check retriever_resources — if no resources returned, confidence = 0.3 (no knowledge match)
2. **Secondary:** Average retriever score across top resources
3. **Fallback:** If retriever_resources empty and answer contains uncertainty phrases ("I'm not sure", "I don't have information"), confidence = 0.4
4. Store as `confidence_score` in messages table for STORY-022 to evaluate

```go
func calculateConfidence(resp *DifyChatResponse) float64 {
    if len(resp.RetrieverResources) == 0 {
        return 0.3 // No knowledge base match
    }
    totalScore := 0.0
    for _, r := range resp.RetrieverResources {
        totalScore += r.Score
    }
    avgScore := totalScore / float64(len(resp.RetrieverResources))
    // Normalize to 0-1 range (Dify scores are typically 0-1)
    return math.Min(avgScore, 1.0)
}
```

### Metrics
```
router_dify_response_seconds        histogram  End-to-end Dify API response time
router_dify_tokens_total            counter    {type=prompt|completion} Token usage
router_dify_retrieval_hit_total     counter    Responses with retriever_resources > 0
router_dify_retrieval_miss_total    counter    Responses with no retriever_resources
router_dify_confidence_score        histogram  Distribution of confidence scores
router_dify_errors_total            counter    {error_type} API errors
```

### Components
- `unica/router/internal/bridge/dify.go` — Enhanced Dify client with RAG metadata
- `unica/router/internal/routing/router.go` — Enhanced routing logic with context inputs
- `unica/router/internal/routing/confidence.go` — Confidence score calculation

---

## Dependencies

**Prerequisite:**
- STORY-016 (Intelligent Routing — basic Dify call path)
- STORY-019 (Dify Multi-Workspace — workspace API keys)
- STORY-020 (RAG Knowledge Base — populated knowledge for testing)

**Blocks:**
- STORY-022 (Confidence Scoring — uses confidence data from this story)
- STORY-017 (AI→Human Handoff — handoff triggered by confidence evaluation)

---

## Definition of Done

- [ ] Dify chat API called with conversation context and customer inputs
- [ ] RAG retrieval metadata extracted and logged
- [ ] Confidence score calculated and stored in messages table
- [ ] Dify conversation_id tracked for multi-turn conversations
- [ ] AI response published to outbound stream
- [ ] Full round-trip latency < 3s (P95) verified
- [ ] Retry logic working (2 retries on Dify failure)
- [ ] Prometheus metrics exposed
- [ ] Unit tests for confidence calculation, Dify client (>=80% coverage)
- [ ] Integration test: send message → verify RAG retrieval → verify response with metadata
- [ ] Code committed to `unica/router/`

---

## Story Points Breakdown

- **Enhanced Dify client + RAG metadata:** 2 points
- **Confidence scoring logic:** 1 point
- **Conversation context management:** 1 point
- **Testing + metrics:** 1 point
- **Total:** 5 points

**Rationale:** Moderate complexity — builds on existing STORY-016 Dify client, but adds significant depth: RAG metadata processing, confidence calculation, multi-turn context management, and quality metrics.
