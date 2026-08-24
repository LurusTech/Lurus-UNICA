package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/experience"
	"github.com/kefu/unica/router/internal/guardrail"
	"github.com/kefu/unica/router/internal/handoff"
	"github.com/kefu/unica/router/internal/routing"
	"github.com/kefu/unica/router/internal/state"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[router] starting...")

	// Load configuration from environment
	cfg := loadConfig()

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", cfg.postgresURL)
	if err != nil {
		log.Fatalf("[router] failed to open PostgreSQL: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("[router] failed to connect to PostgreSQL: %v", err)
	}
	log.Println("[router] connected to PostgreSQL")

	// Connect to Redis
	opts, err := redis.ParseURL(cfg.redisURL)
	if err != nil {
		log.Fatalf("[router] invalid REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("[router] cannot connect to Redis: %v", err)
	}
	log.Println("[router] connected to Redis")

	// Create state management components
	repo := state.NewRepository(db)
	cache := state.NewCache(rdb)
	managerConfig := state.ManagerConfig{
		IdleTimeout:   cfg.idleTimeout,
		CheckInterval: 1 * time.Minute,
	}
	stateManager := state.NewManager(repo, cache, managerConfig)

	// Keep the monthly partitions of messages and audit_logs provisioned ahead of
	// the clock. Started before the workers: an insert into a month with no
	// partition fails outright, and the router acks and drops the message.
	partitions := state.NewPartitionMaintainer(db, cfg.partitionInterval, cfg.partitionMonthsAhead)
	partitions.Start(ctx)

	// Create routing components
	routeCache := routing.NewRouteCache(rdb, db, cfg.routeCacheTTL)
	difyClient := bridge.NewDifyClient()

	routerConfig := routing.RouterConfig{
		ConsumerGroup: cfg.consumerGroup,
		ConsumerName:  cfg.consumerName,
		Workers:       cfg.workers,
		TriageMode:    cfg.triageMode,
		SceneMode:     cfg.sceneMode,
	}
	log.Printf("[router] intent triage mode: %s, scene mode: %s", cfg.triageMode, cfg.sceneMode)

	router := routing.NewRouter(rdb, db, stateManager, difyClient, routeCache, routerConfig)

	if cfg.ontologyEnabled {
		router.SetOntology(domain.NewStore(db, cfg.ontologyCacheTTL))
		log.Printf("[router] domain ontology enabled (cache ttl %s); per-product-line opt-in via config_json.ontology",
			cfg.ontologyCacheTTL)
	} else {
		log.Printf("[router] domain ontology disabled by ONTOLOGY_ENABLED")
	}

	// Wire the acest knowledge integration (experience playbook + external KB)
	// when configured. Disabled entirely when ACEST_KB_URL is unset; recall
	// and feedback both fail open, so an unreachable kb-server never affects
	// message routing.
	var expCollector *experience.Collector
	if cfg.acestURL != "" {
		acestClient := bridge.NewAcestClient()
		acestCfg := bridge.AcestConfig{BaseURL: cfg.acestURL, Token: cfg.acestToken}

		if err := acestClient.Health(ctx, acestCfg); err != nil {
			log.Printf("[router] warning: acest kb-server not reachable at %s: %v (continuing, will retry per-call)", cfg.acestURL, err)
		} else {
			log.Printf("[router] acest kb-server connected at %s", cfg.acestURL)
		}

		expCollector = experience.NewCollector(acestClient, acestCfg, cfg.acestQueueSize)
		expCollector.Start()

		router.SetAcest(&routing.AcestIntegration{
			Client:        acestClient,
			Config:        acestCfg,
			RecallTimeout: cfg.acestRecallTimeout,
			RecallTopK:    cfg.acestRecallTopK,
			KBSources:     cfg.acestKBSources,
			Collector:     expCollector,
		})
	}

	// Create handoff handler and consumer
	chatwootClient := bridge.NewChatwootClient()
	handoffHandler := handoff.NewHandoffHandler(db, rdb, stateManager, difyClient, chatwootClient)
	handoffConsumer := handoff.NewHandoffConsumer(rdb, handoffHandler, cfg.consumerGroup+"-handoff", cfg.consumerName+"-handoff")

	// Start state manager background tasks
	stateManager.Start(ctx)

	// Start router workers
	routerCtx, routerCancel := context.WithCancel(ctx)
	router.Start(routerCtx)

	// Start handoff consumer
	handoffConsumer.Start(ctx)

	// Setup HTTP server for health checks and metrics
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Check Redis connectivity
		if err := rdb.Ping(r.Context()).Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"unhealthy","redis":"disconnected"}`))
			return
		}
		// Check PostgreSQL connectivity
		if err := db.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"unhealthy","postgres":"disconnected"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy"}`))
	})
	mux.Handle("/metrics", promhttp.Handler())

	// /configz reports the behaviour switches this process is actually running
	// with. It exists because they are decided by this process's environment and
	// nothing else can see them: the admin service has none of these variables,
	// so a console that tried to display them by reading its own environment
	// would confidently show the opposite of what is in force.
	//
	// Switches only. This endpoint is unauthenticated like /healthz and
	// /metrics, so no connection string, token or key belongs in it — the ACEST
	// integration is reported as on or off, never by its URL or credential.
	mux.HandleFunc("/configz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"intent_triage":      string(cfg.triageMode),
			"scene_mode":         string(cfg.sceneMode),
			"ontology_enabled":   cfg.ontologyEnabled,
			"ontology_cache_ttl": cfg.ontologyCacheTTL.String(),
			"route_cache_ttl":    cfg.routeCacheTTL.String(),
			"idle_timeout":       cfg.idleTimeout.String(),
			"acest_enabled":      cfg.acestURL != "",
			"workers":            cfg.workers,
			// How long a conversation's Dify thread is remembered, i.e. how far
			// back the assistant can still see. A compile-time constant rather
			// than an env var, but it is behaviour an operator asks about in
			// the same breath as the rest, and this is where they look.
			"dify_conv_ttl": routing.DifyConvTTL.String(),
		}); err != nil {
			log.Printf("[router] configz encode error: %v", err)
		}
	})

	srv := &http.Server{
		Addr:         ":" + cfg.port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[router] HTTP server listening on :%s", cfg.port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[router] HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[router] received signal %s, starting graceful shutdown...", sig)

	// Phase 1: Stop HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[router] HTTP shutdown error: %v", err)
	}
	log.Println("[router] HTTP server stopped")

	// Phase 2: Stop handoff consumer
	handoffConsumer.Stop()

	// Phase 3: Stop router workers
	routerCancel()
	router.Stop()

	// Phase 4: Stop state manager
	stateManager.Stop()
	partitions.Stop()

	// Phase 4b: Stop experience collector (best-effort feedback loop)
	if expCollector != nil {
		expCollector.Stop()
	}

	// Phase 5: Close connections
	if err := rdb.Close(); err != nil {
		log.Printf("[router] Redis close error: %v", err)
	}

	log.Println("[router] shutdown complete")
}

// config holds all router service configuration.
type config struct {
	postgresURL   string
	redisURL      string
	consumerGroup string
	consumerName  string
	workers       int
	routeCacheTTL time.Duration
	port          string
	idleTimeout   time.Duration
	triageMode    guardrail.TriageMode
	sceneMode     routing.SceneMode

	// Monthly partition provisioning for messages and audit_logs.
	partitionInterval    time.Duration
	partitionMonthsAhead int

	// Domain ontology (disabled outright when ontologyEnabled is false; each
	// product line still has to opt in via config_json).
	ontologyEnabled  bool
	ontologyCacheTTL time.Duration

	// acest kb-server integration (disabled when acestURL is empty)
	acestURL           string
	acestToken         string
	acestRecallTimeout time.Duration
	acestRecallTopK    int
	acestKBSources     []string
	acestQueueSize     int
}

// loadConfig reads configuration from environment variables with defaults.
func loadConfig() config {
	cfg := config{
		postgresURL:   envOrDefault("POSTGRES_URL", "postgres://postgres:password@localhost:5432/unica_core?sslmode=disable"),
		redisURL:      envOrDefault("REDIS_URL", "redis://localhost:6379/0"),
		consumerGroup: envOrDefault("ROUTER_CONSUMER_GROUP", "router-group"),
		consumerName:  envOrDefault("ROUTER_CONSUMER_NAME", "router-1"),
		port:          envOrDefault("ROUTER_PORT", "8081"),
	}

	workers, err := strconv.Atoi(envOrDefault("ROUTER_WORKERS", "4"))
	if err != nil || workers < 1 {
		workers = 4
	}
	cfg.workers = workers

	cacheTTL, err := time.ParseDuration(envOrDefault("ROUTE_CACHE_TTL", "5m"))
	if err != nil {
		cacheTTL = 5 * time.Minute
	}
	cfg.routeCacheTTL = cacheTTL

	idleTimeout, err := time.ParseDuration(envOrDefault("IDLE_TIMEOUT", "30m"))
	if err != nil {
		idleTimeout = 30 * time.Minute
	}
	cfg.idleTimeout = idleTimeout

	// Partition provisioning. The defaults are deliberately generous rather than
	// tunable-by-necessity: three months of headroom means the tables only run dry
	// after a quarter of downtime, and a daily pass costs one catalog query.
	partitionInterval, err := time.ParseDuration(envOrDefault("PARTITION_CHECK_INTERVAL", "24h"))
	if err != nil {
		partitionInterval = state.DefaultPartitionInterval
	}
	cfg.partitionInterval = partitionInterval
	monthsAhead, err := strconv.Atoi(envOrDefault("PARTITION_MONTHS_AHEAD", "3"))
	if err != nil || monthsAhead < 1 {
		monthsAhead = state.DefaultPartitionMonthsAhead
	}
	cfg.partitionMonthsAhead = monthsAhead

	// Pre-dispatch intent triage. Defaults to shadow: classifications are
	// recorded as metrics but every routing decision stays with the legacy rules,
	// so enabling this on an existing deployment changes nothing observable to
	// customers. A malformed value falls back to the default rather than
	// silently disabling the feature.
	triageMode, err := guardrail.ParseTriageMode(os.Getenv("INTENT_TRIAGE"))
	if err != nil {
		log.Printf("[router] warning: %v; falling back to %q", err, guardrail.DefaultTriageMode)
		triageMode = guardrail.DefaultTriageMode
	}
	cfg.triageMode = triageMode

	// Commercial-stage classification and response-strategy injection.
	// Defaults to shadow for the same reason as triage: stage metrics accrue
	// while every answer stays exactly as it was.
	sceneMode, err := routing.ParseSceneMode(os.Getenv("SCENE_MODE"))
	if err != nil {
		log.Printf("[router] warning: %v; falling back to %q", err, routing.DefaultSceneMode)
		sceneMode = routing.DefaultSceneMode
	}
	cfg.sceneMode = sceneMode

	// Domain ontology. Enabled by default because it costs nothing until a
	// product line opts in; ONTOLOGY_ENABLED=false is the switch for turning it
	// off everywhere at once during an incident.
	cfg.ontologyEnabled = envOrDefault("ONTOLOGY_ENABLED", "true") != "false"
	ontologyTTL, err := time.ParseDuration(envOrDefault("ONTOLOGY_CACHE_TTL", "5m"))
	if err != nil {
		ontologyTTL = 5 * time.Minute
	}
	cfg.ontologyCacheTTL = ontologyTTL

	// acest knowledge integration
	cfg.acestURL = strings.TrimRight(os.Getenv("ACEST_KB_URL"), "/")
	cfg.acestToken = os.Getenv("ACEST_KB_TOKEN")
	if cfg.acestURL != "" && cfg.acestToken == "" {
		log.Printf("[router] warning: ACEST_KB_URL set but ACEST_KB_TOKEN empty; authenticated calls will fail")
	}
	recallTimeout, err := time.ParseDuration(envOrDefault("ACEST_RECALL_TIMEOUT", "2s"))
	if err != nil {
		recallTimeout = 2 * time.Second
	}
	cfg.acestRecallTimeout = recallTimeout
	topK, err := strconv.Atoi(envOrDefault("ACEST_RECALL_TOP_K", "3"))
	if err != nil || topK < 1 {
		topK = 3
	}
	cfg.acestRecallTopK = topK
	if src := os.Getenv("ACEST_KB_SOURCES"); src != "" {
		for _, s := range strings.Split(src, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.acestKBSources = append(cfg.acestKBSources, s)
			}
		}
	}
	queueSize, err := strconv.Atoi(envOrDefault("ACEST_EXPERIENCE_QUEUE", "256"))
	if err != nil || queueSize < 1 {
		queueSize = 256
	}
	cfg.acestQueueSize = queueSize

	log.Printf("[router] config: postgres=%s redis=%s group=%s consumer=%s workers=%d cache_ttl=%s port=%s idle_timeout=%s",
		cfg.postgresURL, cfg.redisURL, cfg.consumerGroup, cfg.consumerName,
		cfg.workers, cfg.routeCacheTTL, cfg.port, cfg.idleTimeout)

	return cfg
}

// envOrDefault returns the value of an environment variable or a default.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
