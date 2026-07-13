package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/marketing"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
	"github.com/kefu/unica/router/internal/survey"
)

const (
	// InboundStream is the Redis Stream key for inbound messages.
	InboundStream = "unica:inbound"
	// OutboundStream is the Redis Stream key for outbound messages.
	OutboundStream = "unica:outbound"
	// HandoffStream is the Redis Stream key for handoff events.
	HandoffStream = "unica:handoff"
)

// RouterConfig holds configuration for the Router.
type RouterConfig struct {
	ConsumerGroup string
	ConsumerName  string
	Workers       int
}

// Router consumes inbound messages, resolves routes, calls AI, and publishes responses.
type Router struct {
	rdb                *redis.Client
	stateManager       *state.Manager
	difyClient         *bridge.DifyClient
	routeCache         *RouteCache
	convLock           *ConvLock
	acest              *AcestIntegration
	chatwootForwarder  *ChatwootForwarder
	evaluator          *guardrail.Evaluator
	marketingTracker   *marketing.Tracker
	surveyHandler      *survey.Handler
	consumerGroup      string
	consumerName       string
	workers            int
	stopCh             chan struct{}
	wg                 sync.WaitGroup
}

// NewRouter creates a new Router with the given dependencies.
func NewRouter(rdb *redis.Client, db *sql.DB, stateManager *state.Manager, difyClient *bridge.DifyClient, routeCache *RouteCache, config RouterConfig) *Router {
	// Create Chatwoot forwarder for human_processing state
	cwClient := bridge.NewChatwootClient()
	cwForwarder := NewChatwootForwarder(cwClient, rdb, db)

	// Create survey handler and register OnClose callback
	var surveyH *survey.Handler
	if stateManager != nil && rdb != nil {
		surveyH = survey.NewHandler(db, rdb, stateManager)

		// Register OnClose callback to send survey when conversation closes
		stateManager.SetOnClose(func(ctx context.Context, conv *state.Conversation) {
			shouldSend, err := surveyH.ShouldSendSurvey(ctx, conv.ID, conv.ProductLineID)
			if err != nil {
				log.Printf("[router] survey check error for conversation %s: %v", conv.ID, err)
				return
			}
			if !shouldSend {
				return
			}
			convInfo := &survey.ConversationInfo{
				ID:            conv.ID,
				ChannelID:     conv.ChannelID,
				ProductLineID: conv.ProductLineID,
				CustomerID:    conv.CustomerID,
			}
			if err := surveyH.SendSurvey(ctx, convInfo); err != nil {
				log.Printf("[router] failed to send survey for conversation %s: %v", conv.ID, err)
			}
		})
	}

	// Create marketing tracker for intent detection
	var mktTracker *marketing.Tracker
	if stateManager != nil {
		mktTracker = marketing.NewTracker(stateManager)
	}

	return &Router{
		rdb:               rdb,
		stateManager:      stateManager,
		difyClient:        difyClient,
		routeCache:        routeCache,
		convLock:          NewConvLock(rdb),
		chatwootForwarder: cwForwarder,
		evaluator:         guardrail.NewEvaluator(),
		marketingTracker:  mktTracker,
		surveyHandler:     surveyH,
		consumerGroup:     config.ConsumerGroup,
		consumerName:      config.ConsumerName,
		workers:           config.Workers,
		stopCh:            make(chan struct{}),
	}
}

// Start begins consuming inbound messages with the configured number of workers.
func (r *Router) Start(ctx context.Context) {
	// Ensure the consumer group exists
	if err := r.ensureConsumerGroup(ctx); err != nil {
		log.Printf("[router] warning: failed to create consumer group: %v", err)
	}

	// Recover this consumer's pending entries (delivered but not acked before
	// a previous crash/restart) exactly once, before workers start. All
	// workers share one consumer name, so draining per-worker would process
	// the same PEL entries multiple times; and the main loops read only new
	// (">") entries, so without this pass un-acked messages stay stranded.
	r.drainPending(ctx, 0)

	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.readLoop(ctx, i)
	}
	log.Printf("[router] started %d workers on %s (group=%s, consumer=%s)",
		r.workers, InboundStream, r.consumerGroup, r.consumerName)
}

// Stop signals all workers to stop and waits for them to finish.
func (r *Router) Stop() {
	close(r.stopCh)
	r.wg.Wait()
	log.Printf("[router] all workers stopped")
}

// ensureConsumerGroup creates the consumer group if it does not exist.
func (r *Router) ensureConsumerGroup(ctx context.Context) error {
	err := r.rdb.XGroupCreateMkStream(ctx, InboundStream, r.consumerGroup, "0").Err()
	if err != nil {
		// BUSYGROUP means the group already exists
		if err.Error() == "BUSYGROUP Consumer Group name already exists" {
			return nil
		}
		return fmt.Errorf("create consumer group %s on %s: %w", r.consumerGroup, InboundStream, err)
	}
	log.Printf("[router] created consumer group %s on %s", r.consumerGroup, InboundStream)
	return nil
}

// readLoop reads new messages from the inbound stream.
func (r *Router) readLoop(ctx context.Context, workerID int) {
	defer r.wg.Done()

	for {
		select {
		case <-r.stopCh:
			log.Printf("[router] worker %d shutting down", workerID)
			return
		default:
		}

		result, err := r.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.consumerGroup,
			Consumer: r.consumerName,
			Streams:  []string{InboundStream, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("[router] worker %d XREADGROUP error: %v", workerID, err)
			time.Sleep(time.Second)
			continue
		}

		for _, stream := range result {
			for _, entry := range stream.Messages {
				r.processMessage(ctx, entry, workerID)
			}
		}
	}
}

// drainPending processes this consumer's previously delivered but
// unacknowledged entries (its PEL) until none remain. The read cursor is
// advanced past each batch so an entry that fails again (and stays un-acked)
// cannot cause an infinite loop; it will be retried on the next restart.
func (r *Router) drainPending(ctx context.Context, workerID int) {
	cursor := "0"
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}

		result, err := r.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.consumerGroup,
			Consumer: r.consumerName,
			Streams:  []string{InboundStream, cursor},
			Count:    10,
		}).Result()
		if err != nil {
			if err != redis.Nil {
				log.Printf("[router] worker %d: pending drain error: %v", workerID, err)
			}
			return
		}

		total := 0
		for _, stream := range result {
			total += len(stream.Messages)
			for _, entry := range stream.Messages {
				r.processMessage(ctx, entry, workerID)
				cursor = entry.ID
			}
		}
		if total == 0 {
			return
		}
		log.Printf("[router] worker %d: recovered %d pending message(s)", workerID, total)
	}
}

// processMessage handles a single inbound stream message.
func (r *Router) processMessage(ctx context.Context, entry redis.XMessage, workerID int) {
	start := time.Now()

	// Extract payload
	payload, ok := entry.Values["payload"].(string)
	if !ok {
		log.Printf("[router] worker %d: message %s has no payload, acking", workerID, entry.ID)
		r.ack(ctx, entry.ID)
		return
	}

	var msg model.StandardMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		log.Printf("[router] worker %d: failed to unmarshal message %s: %v, acking", workerID, entry.ID, err)
		r.ack(ctx, entry.ID)
		return
	}

	// Resolve route for channel
	channelID := msg.Data.ChannelID
	routeConfig, err := r.routeCache.GetRoute(ctx, channelID)
	if err != nil {
		log.Printf("[router] worker %d: failed to resolve route for channel %s: %v, acking", workerID, channelID, err)
		r.ack(ctx, entry.ID)
		return
	}

	// Set product_line_id on the message for state manager
	msg.Data.ProductLineID = routeConfig.ProductLineID

	// Serialize processing per customer+channel across workers and instances.
	// Without this, two quick messages from the same customer can both read
	// state "pending", both transition, and both trigger a Dify call; it also
	// guards conversation creation and the dify_conv session read-modify-write.
	// If the lock cannot be obtained within the wait budget, the message is
	// requeued to the back of the stream and the original entry acked, so it
	// is retried without blocking this worker or being stranded in the PEL.
	lockID := msg.Data.ChannelID + ":" + msg.Data.PlatformMeta.PlatformUserID
	if msg.Data.PlatformMeta.PlatformUserID == "" {
		lockID = msg.Data.ChannelID + ":" + msg.Data.CustomerID
	}
	release, err := r.convLock.Acquire(ctx, lockID)
	if err != nil {
		log.Printf("[router] worker %d: could not lock %s for message %s: %v (requeueing)",
			workerID, lockID, entry.ID, err)
		if r.requeue(ctx, entry) {
			r.ack(ctx, entry.ID)
		}
		return
	}
	defer release()

	// Handle inbound message via state manager (creates/finds customer + conversation, stores message)
	convID, err := r.stateManager.HandleInboundMessage(ctx, &msg)
	if err != nil {
		log.Printf("[router] worker %d: state manager error for message %s: %v", workerID, entry.ID, err)
		r.ack(ctx, entry.ID)
		return
	}

	// Get conversation state to decide action
	convState, err := r.getConversationState(ctx, convID)
	if err != nil {
		log.Printf("[router] worker %d: failed to get conversation state for %s: %v", workerID, convID, err)
		r.ack(ctx, entry.ID)
		return
	}

	routeType := "ai"
	switch convState {
	case state.StatePending:
		// Transition to ai_processing, then call Dify
		if err := r.stateManager.TransitionState(ctx, convID, state.StateAIProcessing, "router"); err != nil {
			log.Printf("[router] worker %d: failed to transition %s to ai_processing: %v", workerID, convID, err)
			r.ack(ctx, entry.ID)
			return
		}
		r.callDifyAndPublish(ctx, routeConfig, &msg, convID, workerID)

	case state.StateAIProcessing:
		// Already in AI processing, call Dify
		r.callDifyAndPublish(ctx, routeConfig, &msg, convID, workerID)

	case state.StateHumanProcessing:
		// Forward to Chatwoot for human agent handling
		routeType = "human"
		if err := r.chatwootForwarder.ForwardToChatwoot(ctx, &msg, convID, routeConfig); err != nil {
			log.Printf("[router] worker %d: failed to forward to Chatwoot for conversation %s: %v", workerID, convID, err)
		} else {
			log.Printf("[router] worker %d: forwarded message to Chatwoot for conversation %s", workerID, convID)
		}

	case state.StateClosed:
		// Check if this is a survey reply before reopening
		if r.surveyHandler != nil {
			handled, err := r.surveyHandler.HandleSurveyReply(ctx, convID, msg.Data.Content.Text)
			if err != nil {
				log.Printf("[router] worker %d: survey reply check error for conversation %s: %v", workerID, convID, err)
			}
			if handled {
				log.Printf("[router] worker %d: handled survey reply for conversation %s", workerID, convID)
				routeType = "survey"
				break
			}
		}
		// Not a survey reply: reopen and transition to ai_processing, then call Dify
		if err := r.stateManager.TransitionState(ctx, convID, state.StateAIProcessing, "router"); err != nil {
			log.Printf("[router] worker %d: failed to reopen conversation %s: %v", workerID, convID, err)
			r.ack(ctx, entry.ID)
			return
		}
		r.callDifyAndPublish(ctx, routeConfig, &msg, convID, workerID)
	}

	// Record metrics
	metrics.MessagesRoutedTotal.WithLabelValues(routeConfig.ProductLineID, routeType).Inc()
	metrics.RoutingDuration.Observe(time.Since(start).Seconds())

	// ACK the inbound message
	r.ack(ctx, entry.ID)

	log.Printf("[router] worker %d: processed message %s for conversation %s (route=%s, took=%s)",
		workerID, entry.ID, convID, routeType, time.Since(start))
}

// difyConvKeyPrefix is the Redis key prefix for tracking Dify conversation IDs.
const difyConvKeyPrefix = "dify_conv:"

// difyConvTTL is the TTL for Dify conversation tracking keys in Redis.
const difyConvTTL = 24 * time.Hour

// difyConvSession holds the Dify conversation tracking data stored in Redis.
type difyConvSession struct {
	DifyConversationID string `json:"dify_conversation_id"`
	MessageCount       int    `json:"message_count"`
	LastActive         string `json:"last_active"`
}

// getDifyConvID retrieves the Dify conversation ID from Redis for multi-turn context.
func (r *Router) getDifyConvID(ctx context.Context, convID string) string {
	key := difyConvKeyPrefix + convID
	val, err := r.rdb.Get(ctx, key).Result()
	if err != nil {
		return ""
	}
	var session difyConvSession
	if err := json.Unmarshal([]byte(val), &session); err != nil {
		return ""
	}
	return session.DifyConversationID
}

// setDifyConvID stores the Dify conversation ID in Redis for multi-turn context.
func (r *Router) setDifyConvID(ctx context.Context, convID string, difyConvID string, msgCount int) {
	key := difyConvKeyPrefix + convID
	session := difyConvSession{
		DifyConversationID: difyConvID,
		MessageCount:       msgCount,
		LastActive:         time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(session)
	if err != nil {
		log.Printf("[router] failed to marshal dify conv session: %v", err)
		return
	}
	if err := r.rdb.Set(ctx, key, string(data), difyConvTTL).Err(); err != nil {
		log.Printf("[router] failed to store dify conv ID for %s: %v", convID, err)
	}
}

// callDifyAndPublish calls the Dify AI API and publishes the response to the outbound stream.
func (r *Router) callDifyAndPublish(ctx context.Context, config *RouteConfig, msg *model.StandardMessage, convID string, workerID int) {
	// Extract text query from the message content
	query := msg.Data.Content.Text
	if query == "" {
		log.Printf("[router] worker %d: message has no text content for AI, skipping Dify call", workerID)
		return
	}

	userID := msg.Data.PlatformMeta.PlatformUserID
	if userID == "" {
		userID = msg.Data.CustomerID
	}

	// Look up existing Dify conversation ID for multi-turn context
	difyConvID := r.getDifyConvID(ctx, convID)

	// Build conversation inputs for Dify
	inputs := map[string]string{
		"customer_name": msg.Data.PlatformMeta.PlatformUserID,
		"channel":       msg.Data.ChannelID,
		"product_line":  msg.Data.ProductLineID,
	}

	// Recall experience/knowledge context from the acest kb-server and inject
	// it as Dify app variables. Fail-open: on error or timeout the inputs are
	// simply absent and the Dify app proceeds without them.
	if r.acest != nil {
		experienceCtx, knowledgeCtx := r.recallKnowledge(ctx, query)
		if experienceCtx != "" {
			inputs["experience_context"] = experienceCtx
		}
		if knowledgeCtx != "" {
			inputs["knowledge_context"] = knowledgeCtx
		}
	}

	// Call Dify API
	difyStart := time.Now()
	difyCfg := bridge.DifyConfig{
		BaseURL: config.DifyBaseURL,
		APIKey:  config.DifyAPIKey,
	}
	difyResp, err := r.difyClient.Chat(ctx, difyCfg, query, userID, difyConvID, inputs)
	difyDuration := time.Since(difyStart)
	metrics.DifyCallDuration.Observe(difyDuration.Seconds())

	if err != nil {
		log.Printf("[router] worker %d: Dify API error for conversation %s: %v", workerID, convID, err)
		metrics.DifyErrorsTotal.WithLabelValues("api_call").Inc()
		return
	}

	if difyResp.Answer == "" {
		log.Printf("[router] worker %d: Dify returned empty answer for conversation %s", workerID, convID)
		metrics.DifyErrorsTotal.WithLabelValues("empty_answer").Inc()
		return
	}

	// Marketing intent detection: parse [INTENT:xxx] tags and strip from customer-facing answer
	detectResult := marketing.DetectIntents(difyResp.Answer)
	difyResp.Answer = detectResult.CleanedAnswer

	// Track detected intents in conversation metadata (non-blocking)
	if len(detectResult.Intents) > 0 && r.marketingTracker != nil {
		if err := r.marketingTracker.TrackIntents(ctx, convID, config.ProductLineID, detectResult.Intents); err != nil {
			log.Printf("[router] worker %d: failed to track marketing intents for %s: %v", workerID, convID, err)
		}
	}

	// Store Dify conversation ID for multi-turn context
	if difyResp.ConversationID != "" {
		// Increment message count (simple approach: read current + 1)
		existingConvID := r.getDifyConvID(ctx, convID)
		msgCount := 1
		if existingConvID != "" {
			// Already had a session, increment
			key := difyConvKeyPrefix + convID
			val, _ := r.rdb.Get(ctx, key).Result()
			var session difyConvSession
			if json.Unmarshal([]byte(val), &session) == nil {
				msgCount = session.MessageCount + 1
			}
		}
		r.setDifyConvID(ctx, convID, difyResp.ConversationID, msgCount)
	}

	// Calculate confidence score
	confidence := calculateConfidence(difyResp)
	confidenceF32 := float32(confidence)

	// Track retrieval metrics
	if len(difyResp.RetrieverResources) > 0 {
		metrics.DifyRetrievalHitTotal.Inc()
		// Persist the hit flag into conversation metadata so the reporter's
		// knowledge-base hit-rate query has data to read (the Prometheus
		// counter alone is not queryable per-conversation).
		if r.stateManager != nil {
			if err := r.stateManager.UpdateConversationMetadata(ctx, convID, json.RawMessage(`{"dify_retrieval_hit":true}`)); err != nil {
				log.Printf("[router] worker %d: failed to persist dify_retrieval_hit for %s: %v", workerID, convID, err)
			}
		}
		// Log retriever resources for debugging/auditing
		for i, res := range difyResp.RetrieverResources {
			log.Printf("[router] worker %d: RAG source %d for conversation %s: dataset=%s doc=%s score=%.3f",
				workerID, i+1, convID, res.DatasetName, res.DocumentName, res.Score)
		}
	} else {
		metrics.DifyRetrievalMissTotal.Inc()
	}

	// Track token usage metrics
	metrics.DifyTokensTotal.WithLabelValues("prompt").Add(float64(difyResp.Metadata.Usage.PromptTokens))
	metrics.DifyTokensTotal.WithLabelValues("completion").Add(float64(difyResp.Metadata.Usage.CompletionTokens))
	metrics.DifyConfidenceScore.Observe(confidence)

	// Guardrail evaluation: decide whether to send AI response or trigger handoff
	guardrailCfg := guardrail.LoadGuardrailConfig(config.ConfigJSON)
	evalResult := r.evaluator.Evaluate(query, confidence, guardrailCfg)
	metrics.GuardrailDecisionsTotal.WithLabelValues(string(evalResult.Decision), evalResult.Reason).Inc()

	switch evalResult.Decision {
	case guardrail.DecisionSend:
		// Normal flow: publish AI response to outbound stream
		r.publishAIResponse(ctx, config, msg, convID, workerID, difyResp.Answer, confidenceF32, difyResp.Metadata.Usage.TotalTokens, len(difyResp.RetrieverResources), difyDuration)

		// Feed the delivered answer back into the experience KB as a success
		// sample (asynchronous, lossy by design).
		r.submitExperience(bridge.Experience{
			UserQuery:         query,
			AssistantResponse: difyResp.Answer,
			Success:           true,
			ToolsUsed:         retrievalDatasets(difyResp),
			SessionID:         convID,
		})

	case guardrail.DecisionHandoff:
		// Handoff flow: send holding message, publish handoff event, transition state
		log.Printf("[router] worker %d: guardrail triggered handoff for conversation %s (reason=%s, confidence=%.3f, keyword=%s)",
			workerID, convID, evalResult.Reason, evalResult.Confidence, evalResult.MatchedKeyword)

		r.publishHoldingMessage(ctx, config, msg, convID, workerID, guardrailCfg.HoldingMessage)
		r.publishHandoffEvent(ctx, msg, convID, config.ProductLineID, evalResult, difyResp.Answer)

		// Transition conversation to human_processing
		if err := r.stateManager.TransitionState(ctx, convID, state.StateHumanProcessing, "guardrail"); err != nil {
			log.Printf("[router] worker %d: failed to transition conversation %s to human_processing: %v", workerID, convID, err)
		}

		// Feed the suppressed answer back as a failure sample so the
		// experience KB learns which questions the AI could not handle.
		r.submitExperience(bridge.Experience{
			UserQuery:         query,
			AssistantResponse: difyResp.Answer,
			Success:           false,
			Error:             fmt.Sprintf("handoff: %s (confidence=%.3f)", evalResult.Reason, evalResult.Confidence),
			ToolsUsed:         retrievalDatasets(difyResp),
			SessionID:         convID,
		})
	}
}

// retrievalDatasets returns the deduplicated RAG dataset names that
// contributed to a Dify answer, for experience tools_used attribution.
func retrievalDatasets(difyResp *bridge.DifyResponse) []string {
	if len(difyResp.RetrieverResources) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(difyResp.RetrieverResources))
	var names []string
	for _, res := range difyResp.RetrieverResources {
		if res.DatasetName == "" || seen[res.DatasetName] {
			continue
		}
		seen[res.DatasetName] = true
		names = append(names, "dataset:"+res.DatasetName)
	}
	return names
}

// getConversationState retrieves the current state of a conversation.
func (r *Router) getConversationState(ctx context.Context, convID string) (state.ConversationState, error) {
	conv, err := r.stateManager.GetConversation(ctx, convID)
	if err != nil {
		return "", err
	}
	return conv.State, nil
}

// ack acknowledges a message in the consumer group.
func (r *Router) ack(ctx context.Context, id string) {
	if err := r.rdb.XAck(ctx, InboundStream, r.consumerGroup, id).Err(); err != nil {
		log.Printf("[router] XACK error for %s: %v", id, err)
	}
}

// requeue re-adds a stream entry's values to the back of the inbound stream so
// it is retried later. Used when the conversation lock cannot be obtained.
// Returns false when the requeue failed; the caller must then leave the entry
// un-acked so it stays in the pending list and is recovered by the startup
// pending drain.
func (r *Router) requeue(ctx context.Context, entry redis.XMessage) bool {
	if err := r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: InboundStream,
		Values: entry.Values,
	}).Err(); err != nil {
		log.Printf("[router] failed to requeue message %s (stays pending): %v", entry.ID, err)
		return false
	}
	return true
}

// publishAIResponse builds and publishes the AI response to the outbound stream and stores it in DB.
func (r *Router) publishAIResponse(ctx context.Context, config *RouteConfig, msg *model.StandardMessage, convID string, workerID int, answer string, confidenceF32 float32, totalTokens int, ragSources int, difyDuration time.Duration) {
	outMsg := model.StandardMessage{
		ID:      uuid.New().String(),
		Type:    model.MessageTypeOutbound,
		Source:  "router",
		Subject: "conversation:" + convID,
		Time:    time.Now(),
		Data: model.MessageData{
			ConversationID: convID,
			ChannelID:      msg.Data.ChannelID,
			ProductLineID:  msg.Data.ProductLineID,
			CustomerID:     msg.Data.CustomerID,
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: answer,
			},
			PlatformMeta:  msg.Data.PlatformMeta,
			CorrelationID: msg.Data.CorrelationID,
		},
	}

	outJSON, err := json.Marshal(outMsg)
	if err != nil {
		log.Printf("[router] worker %d: failed to marshal outbound message: %v", workerID, err)
		return
	}

	_, err = r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: OutboundStream,
		Values: map[string]interface{}{
			"payload":        string(outJSON),
			"type":           outMsg.Type,
			"source":         "router",
			"correlation_id": outMsg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		log.Printf("[router] worker %d: failed to publish outbound message: %v", workerID, err)
		return
	}

	// Store AI response message in DB with confidence score
	contentJSON, _ := json.Marshal(outMsg.Data.Content)
	aiMsg := &state.Message{
		ConversationID:  convID,
		Direction:       "outbound",
		SenderType:      "ai",
		ContentJSON:     contentJSON,
		ConfidenceScore: &confidenceF32,
		CorrelationID:   strPtr(msg.Data.CorrelationID),
	}
	if _, err := r.stateManager.CreateMessage(ctx, aiMsg); err != nil {
		log.Printf("[router] worker %d: failed to store AI response message: %v", workerID, err)
	}

	log.Printf("[router] worker %d: AI response published for conversation %s (tokens=%d, confidence=%.3f, rag_sources=%d, took=%s)",
		workerID, convID, totalTokens, confidenceF32, ragSources, difyDuration)
}

// publishHoldingMessage sends a holding message to the customer during handoff.
func (r *Router) publishHoldingMessage(ctx context.Context, config *RouteConfig, msg *model.StandardMessage, convID string, workerID int, holdingText string) {
	holdMsg := model.StandardMessage{
		ID:      uuid.New().String(),
		Type:    model.MessageTypeOutbound,
		Source:  "router",
		Subject: "conversation:" + convID,
		Time:    time.Now(),
		Data: model.MessageData{
			ConversationID: convID,
			ChannelID:      msg.Data.ChannelID,
			ProductLineID:  msg.Data.ProductLineID,
			CustomerID:     msg.Data.CustomerID,
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: holdingText,
			},
			PlatformMeta:  msg.Data.PlatformMeta,
			CorrelationID: msg.Data.CorrelationID,
		},
	}

	holdJSON, err := json.Marshal(holdMsg)
	if err != nil {
		log.Printf("[router] worker %d: failed to marshal holding message: %v", workerID, err)
		return
	}

	_, err = r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: OutboundStream,
		Values: map[string]interface{}{
			"payload":        string(holdJSON),
			"type":           holdMsg.Type,
			"source":         "router",
			"correlation_id": holdMsg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		log.Printf("[router] worker %d: failed to publish holding message: %v", workerID, err)
		return
	}

	log.Printf("[router] worker %d: holding message sent for conversation %s", workerID, convID)
}

// handoffEvent is the payload published to the handoff stream.
type handoffEvent struct {
	Type                  string  `json:"type"`
	ConversationID        string  `json:"conversation_id"`
	ProductLineID         string  `json:"product_line_id"`
	Reason                string  `json:"reason"`
	ConfidenceScore       float64 `json:"confidence_score"`
	AIResponseSuppressed  string  `json:"ai_response_suppressed"`
	CustomerMessage       string  `json:"customer_message"`
	Timestamp             string  `json:"timestamp"`
}

// publishHandoffEvent publishes a handoff event to the unica:handoff stream.
func (r *Router) publishHandoffEvent(ctx context.Context, msg *model.StandardMessage, convID, productLineID string, evalResult *guardrail.EvalResult, suppressedAnswer string) {
	event := handoffEvent{
		Type:                 model.MessageTypeHandoff,
		ConversationID:      convID,
		ProductLineID:       productLineID,
		Reason:              evalResult.Reason,
		ConfidenceScore:     evalResult.Confidence,
		AIResponseSuppressed: suppressedAnswer,
		CustomerMessage:     msg.Data.Content.Text,
		Timestamp:           time.Now().UTC().Format(time.RFC3339),
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		log.Printf("[router] failed to marshal handoff event: %v", err)
		return
	}

	_, err = r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: HandoffStream,
		Values: map[string]interface{}{
			"payload":         string(eventJSON),
			"type":            model.MessageTypeHandoff,
			"conversation_id": convID,
		},
	}).Result()
	if err != nil {
		log.Printf("[router] failed to publish handoff event for conversation %s: %v", convID, err)
		return
	}

	log.Printf("[router] handoff event published for conversation %s (reason=%s)", convID, evalResult.Reason)
}

// strPtr returns a pointer to the given string, or nil if empty.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
