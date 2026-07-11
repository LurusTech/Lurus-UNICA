package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/adapter"
	"github.com/kefu/unica/gateway/internal/adapter/kuaishou"
	"github.com/kefu/unica/gateway/internal/adapter/taobao"
	"github.com/kefu/unica/gateway/internal/adapter/wechat"
	"github.com/kefu/unica/gateway/internal/adapter/xiaohongshu"
	"github.com/kefu/unica/gateway/internal/channelcfg"
	"github.com/kefu/unica/gateway/internal/config"
	"github.com/kefu/unica/gateway/internal/dedup"
	"github.com/kefu/unica/gateway/internal/handler"
	"github.com/kefu/unica/gateway/internal/ratelimit"
	"github.com/kefu/unica/gateway/internal/stream"
	"github.com/kefu/unica/gateway/internal/token"
	"github.com/kefu/unica/gateway/internal/webhook"
	"github.com/kefu/unica/pkg/crypto"
	"github.com/kefu/unica/pkg/model"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[gateway] starting...")

	cfg := config.Load()

	// Parse Redis URL and create client
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("[gateway] invalid REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opts)

	// Verify Redis connectivity
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("[gateway] cannot connect to Redis: %v", err)
	}
	log.Println("[gateway] connected to Redis")

	// Setup streams and consumer groups
	if err := stream.SetupStreams(ctx, rdb); err != nil {
		log.Fatalf("[gateway] failed to setup streams: %v", err)
	}

	// Create deduplicator and dead-letter queue
	deduplicator := dedup.NewDeduplicator(rdb, cfg.DedupTTL)
	dlq := dedup.NewDeadLetterQueue(rdb, cfg.DeadLetterStream)
	retryer := dedup.NewRetryer(dedup.RetryConfig{
		MaxRetries:  cfg.DedupMaxRetries,
		BackoffBase: cfg.DedupBackoffBase,
	}, dlq)
	log.Printf("[gateway] dedup enabled (ttl=%s, max_retries=%d, backoff_base=%s, dead_letter=%s)",
		cfg.DedupTTL, cfg.DedupMaxRetries, cfg.DedupBackoffBase, cfg.DeadLetterStream)

	// Create producer
	producer := stream.NewProducer(rdb)

	// Create adapter registry for outbound message dispatch
	registry := adapter.NewRegistry()

	// Create outbound consumer with registry-backed dispatcher.
	consumerName, _ := os.Hostname()
	if consumerName == "" {
		consumerName = "gateway-0"
	}
	consumer := stream.NewConsumer(
		rdb,
		consumerName,
		cfg.OutboundWorkers,
		cfg.PendingClaimInterval,
		createDispatcher(deduplicator, retryer, registry),
	)

	// Initialize rate limiter with default configuration
	rateLimiter := ratelimit.NewLimiter(rdb, ratelimit.DefaultConfig())
	log.Println("[gateway] rate limiter initialized with default config")

	// Build HTTP mux
	mux := http.NewServeMux()
	mux.Handle("/api/v1/gateway/inbound", handler.NewInboundHandler(producer, rateLimiter))
	mux.Handle("/healthz", handler.NewHealthHandler(rdb))
	mux.Handle("/api/v1/gateway/dead-letter", dedup.NewDeadLetterHandler(dlq))
	mux.Handle("/metrics", promhttp.Handler())

	// Register Chatwoot webhook handler
	if cfg.ChatwootWebhookToken != "" {
		cwWebhookHandler := webhook.NewChatwootWebhookHandler(rdb, webhook.ChatwootWebhookConfig{
			WebhookToken: cfg.ChatwootWebhookToken,
		})
		mux.Handle("/api/v1/webhook/chatwoot", cwWebhookHandler)
		log.Printf("[gateway] Chatwoot webhook handler registered at /api/v1/webhook/chatwoot")
	}

	// Create TokenManager for access token lifecycle management
	tokenMgr := token.NewTokenManager(rdb, cfg.TokenRefreshBuffer, cfg.TokenRefreshRetryMax, cfg.TokenRefreshRetryBackoff)

	// Register WeChat adapter if configured
	if cfg.WeChatAppID != "" {
		wxCfg := wechat.Config{
			AppID:          cfg.WeChatAppID,
			AppSecret:      cfg.WeChatAppSecret,
			Token:          cfg.WeChatToken,
			EncodingAESKey: cfg.WeChatEncodingAESKey,
			EncryptedMode:  cfg.WeChatEncryptedMode,
		}

		// Register WeChat channel with TokenManager
		tokenMgr.RegisterChannel(cfg.WeChatChannelID, token.ChannelCredentials{
			AppID:     cfg.WeChatAppID,
			AppSecret: cfg.WeChatAppSecret,
		}, token.NewWeChatRefresher())

		// Wire token getter to TokenManager
		channelID := cfg.WeChatChannelID
		tokenGetter := func(ctx context.Context) (string, error) {
			return tokenMgr.GetToken(ctx, channelID)
		}
		wxAdapter := wechat.NewAdapter(wxCfg, cfg.WeChatChannelID, tokenGetter)
		registry.Register(cfg.WeChatChannelID, wxAdapter)
		mux.Handle("/webhook/wechat", wechat.NewWebhookHandler(wxAdapter, producer))
		log.Printf("[gateway] WeChat adapter registered at /webhook/wechat (appid=%s)", cfg.WeChatAppID)
	}

	// Register Taobao adapter if configured
	if cfg.TaobaoAppKey != "" {
		tbCfg := taobao.Config{
			AppKey:    cfg.TaobaoAppKey,
			AppSecret: cfg.TaobaoAppSecret,
		}

		// Register Taobao channel with TokenManager
		tokenMgr.RegisterChannel(cfg.TaobaoChannelID, token.ChannelCredentials{
			AppID:     cfg.TaobaoAppKey,
			AppSecret: cfg.TaobaoAppSecret,
			Extra:     map[string]string{"refresh_token": cfg.TaobaoRefreshToken},
		}, token.NewTaobaoRefresher())

		// Wire token getter to TokenManager
		tbChannelID := cfg.TaobaoChannelID
		tbTokenGetter := func(ctx context.Context) (string, error) {
			return tokenMgr.GetToken(ctx, tbChannelID)
		}
		tbAdapter := taobao.NewAdapter(tbCfg, cfg.TaobaoChannelID, tbTokenGetter)
		registry.Register(cfg.TaobaoChannelID, tbAdapter)
		mux.Handle("/webhook/taobao", taobao.NewWebhookHandler(tbAdapter, producer))
		log.Printf("[gateway] Taobao adapter registered at /webhook/taobao (appkey=%s)", cfg.TaobaoAppKey)
	}

	// Register Kuaishou adapter if configured
	if cfg.KuaishouAppKey != "" {
		ksCfg := kuaishou.Config{
			AppKey:        cfg.KuaishouAppKey,
			AppSecret:     cfg.KuaishouAppSecret,
			WebhookSecret: cfg.KuaishouWebhookSecret,
		}

		// Register Kuaishou channel with TokenManager
		tokenMgr.RegisterChannel(cfg.KuaishouChannelID, token.ChannelCredentials{
			AppID:     cfg.KuaishouAppKey,
			AppSecret: cfg.KuaishouAppSecret,
		}, token.NewKuaishouRefresher())

		// Wire token getter to TokenManager
		ksChannelID := cfg.KuaishouChannelID
		ksTokenGetter := func(ctx context.Context) (string, error) {
			return tokenMgr.GetToken(ctx, ksChannelID)
		}
		ksAdapter := kuaishou.NewAdapter(ksCfg, cfg.KuaishouChannelID, ksTokenGetter)
		registry.Register(cfg.KuaishouChannelID, ksAdapter)
		mux.Handle("/webhook/kuaishou", kuaishou.NewWebhookHandler(ksAdapter, producer))
		log.Printf("[gateway] Kuaishou adapter registered at /webhook/kuaishou (appkey=%s)", cfg.KuaishouAppKey)
	}

	// Dynamic channel config pipeline (channel_configs table, admin-managed).
	// Disabled unless both DATABASE_URL and AES_ENCRYPTION_KEY are set, in
	// which case the gateway behaves exactly as it did before this pipeline
	// existed.
	var dynamicConfigDB *sql.DB
	if cfg.DatabaseURL != "" && cfg.AESEncryptionKey != "" {
		dynamicConfigDB = setupDynamicChannelPipeline(ctx, cfg, rdb, registry, producer, tokenMgr, mux)
	}

	// Start TokenManager background refresh goroutines
	tokenMgr.Start(ctx)

	srv := &http.Server{
		Addr:         ":" + cfg.GatewayPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start outbound consumer
	consumerCtx, consumerCancel := context.WithCancel(ctx)
	consumer.Start(consumerCtx)

	// Start HTTP server in background
	go func() {
		log.Printf("[gateway] HTTP server listening on :%s", cfg.GatewayPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[gateway] HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[gateway] received signal %s, starting graceful shutdown...", sig)

	// Phase 1: Stop accepting new HTTP requests
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[gateway] HTTP shutdown error: %v", err)
	}
	log.Println("[gateway] HTTP server stopped")

	// Phase 2: Stop consumer workers
	consumerCancel()
	consumer.Stop()

	// Phase 3: Stop TokenManager
	tokenMgr.Stop()

	// Phase 4: Drain pending messages
	drainCtx, drainCancel := context.WithTimeout(ctx, 10*time.Second)
	defer drainCancel()
	consumer.DrainPending(drainCtx)

	// Phase 5: Close Redis connection
	if err := rdb.Close(); err != nil {
		log.Printf("[gateway] Redis close error: %v", err)
	}

	// Phase 6: Close dynamic channel config database connection, if enabled
	if dynamicConfigDB != nil {
		if err := dynamicConfigDB.Close(); err != nil {
			log.Printf("[gateway] dynamic channel config DB close error: %v", err)
		}
	}

	log.Println("[gateway] shutdown complete")
}

// setupDynamicChannelPipeline connects to Postgres, loads enabled
// channel_configs rows, wires supported platforms (currently xiaohongshu)
// into the adapter registry, mounts a catch-all dynamic webhook route, and
// subscribes to config-invalidation events so future changes are picked up
// without a gateway restart. It returns the opened *sql.DB so the caller can
// close it during graceful shutdown.
func setupDynamicChannelPipeline(
	ctx context.Context,
	cfg *config.Config,
	rdb *redis.Client,
	registry *adapter.Registry,
	producer *stream.Producer,
	tokenMgr *token.TokenManager,
	mux *http.ServeMux,
) *sql.DB {
	aesKey, err := crypto.ParseHexKey(cfg.AESEncryptionKey)
	if err != nil {
		log.Fatalf("[gateway] invalid AES_ENCRYPTION_KEY: %v", err)
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[gateway] failed to open database: %v", err)
	}

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("[gateway] failed to connect to database: %v", err)
	}
	log.Println("[gateway] connected to PostgreSQL for dynamic channel configs")

	loader := channelcfg.NewLoader(db, aesKey, rdb)
	handlers := newDynamicHandlerSet()

	var dynMu sync.Mutex
	dynamicKeys := make(map[string]bool)

	reload := func() {
		configs, err := loader.LoadAll(ctx)
		if err != nil {
			log.Printf("[gateway] failed to load dynamic channel configs: %v", err)
			return
		}

		newHandlers := make(map[string]http.Handler)
		newKeys := make(map[string]bool)

		for _, c := range configs {
			if c.Platform != "xiaohongshu" {
				log.Printf("[gateway] skipping dynamic channel %s: platform %q not yet supported for dynamic wiring", c.ID, c.Platform)
				continue
			}

			key := c.Platform + ":" + c.ID
			xhsCfg := xiaohongshu.Config{
				AppID:         c.AppID,
				AppSecret:     c.AppSecret,
				WebhookSecret: c.WebhookToken,
			}

			// Register credentials with TokenManager so SendMessage can
			// obtain (and lazily refresh) an access token on demand.
			tokenMgr.RegisterChannel(key, token.ChannelCredentials{
				AppID:     c.AppID,
				AppSecret: c.AppSecret,
			}, token.NewXiaohongshuRefresher())

			chKey := key
			xhsTokenGetter := func(ctx context.Context) (string, error) {
				return tokenMgr.GetToken(ctx, chKey)
			}

			xhsAdapter := xiaohongshu.NewAdapter(xhsCfg, key, xhsTokenGetter)
			registry.Register(key, xhsAdapter)
			newHandlers[key] = xiaohongshu.NewWebhookHandler(xhsAdapter, producer)
			newKeys[key] = true
		}

		// Drop registry entries for channels that disappeared or were
		// disabled since the previous load.
		dynMu.Lock()
		for k := range dynamicKeys {
			if !newKeys[k] {
				registry.Unregister(k)
			}
		}
		dynamicKeys = newKeys
		dynMu.Unlock()

		handlers.set(newHandlers)
		log.Printf("[gateway] loaded %d dynamic channel config(s)", len(configs))
	}

	reload()

	if err := loader.Watch(ctx, reload); err != nil {
		log.Printf("[gateway] failed to watch config invalidation events: %v", err)
	}

	mux.Handle("/webhook/", dynamicDispatchHandler(handlers))
	log.Println("[gateway] dynamic channel config pipeline enabled")

	return db
}

// dynamicHandlerSet holds the current set of webhook handlers for
// dynamically-registered channels, keyed by "platform:channelID". The whole
// map is swapped atomically on every config reload.
type dynamicHandlerSet struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}

func newDynamicHandlerSet() *dynamicHandlerSet {
	return &dynamicHandlerSet{handlers: make(map[string]http.Handler)}
}

func (s *dynamicHandlerSet) set(handlers map[string]http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = handlers
}

func (s *dynamicHandlerSet) get(key string) (http.Handler, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.handlers[key]
	return h, ok
}

// dynamicDispatchHandler routes /webhook/{platform}/{channelID} requests to
// the matching dynamically-registered channel's webhook handler. Static
// routes such as /webhook/wechat are registered as exact patterns and take
// precedence over this "/webhook/" subtree pattern under Go's ServeMux
// longest-match rules, so existing static webhooks are unaffected.
func dynamicDispatchHandler(handlers *dynamicHandlerSet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/webhook/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}

		key := parts[0] + ":" + parts[1]
		h, ok := handlers.get(key)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}
}

// createDispatcher returns an OutboundDispatcher with dedup, retry, and adapter registry support.
// The deduplicator filters duplicate messages; the retryer handles retries with
// exponential backoff and dead-letter escalation; the registry routes messages to the correct adapter.
func createDispatcher(deduplicator *dedup.Deduplicator, retryer *dedup.Retryer, registry *adapter.Registry) stream.OutboundDispatcher {
	return func(ctx context.Context, msg *model.StandardMessage) error {
		// Dedup check using platform_msg_id (or fallback key)
		dedupKey := dedup.BuildDedupKey(msg.Data.PlatformMsgID, msg.Data.PlatformMeta.PlatformUserID, msg.Time)
		if dedupKey != "" {
			isDup, err := deduplicator.IsDuplicate(ctx, dedupKey)
			if err != nil {
				log.Printf("[dispatcher] dedup check error (proceeding): %v", err)
			}
			if isDup {
				log.Printf("[dispatcher] duplicate outbound message skipped: id=%s dedup_key=%s", msg.ID, dedupKey)
				return nil
			}
		}

		// Dispatch with retry and dead-letter escalation via adapter registry
		return retryer.ExecuteWithRetry(ctx, msg, func(ctx context.Context, m *model.StandardMessage) error {
			return registry.Dispatch(ctx, m)
		})
	}
}
