package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/config"
	"github.com/kefu/unica/admin/internal/crypto"
	"github.com/kefu/unica/admin/internal/handler"
	"github.com/kefu/unica/admin/internal/identity"
	_ "github.com/kefu/unica/admin/internal/metrics" // register Prometheus metrics
	"github.com/kefu/unica/admin/internal/platform"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/routecache"
	"github.com/kefu/unica/admin/internal/tenant/aisettings"
	"github.com/kefu/unica/admin/internal/tenant/channels"
	"github.com/kefu/unica/admin/internal/tenant/facts"
	"github.com/kefu/unica/admin/internal/tenant/knowledge"
	"github.com/kefu/unica/admin/internal/tenant/quality"
	"github.com/kefu/unica/admin/internal/tenant/workbench"
	"github.com/kefu/unica/pkg/domain"
)

// tenantPrefix is the root of the tenant-scoped route family. Every resource a
// tenant owns hangs below it, which is what makes one middleware enough to
// authorise all of them.
const tenantPrefix = "/api/v1/tenants/"

// Legacy path roots. The tenant routes are the only addresses a client may use;
// these are the shapes the modules that predate them still parse, so the
// dispatcher rewrites into them. A module that reads the tenant route itself —
// knowledge, ai-settings, workbench — needs no entry here, and each one that
// learns to takes a line out of this list.
const (
	legacyProductLinesPrefix  = "/api/v1/product-lines/"
	legacyChannelsPrefix      = "/api/v1/channels/"
	legacyChannelsRoot        = "/api/v1/channels"
	legacyViolationsPrefix    = "/api/v1/violations/"
	legacyHandoffEventsPrefix = "/api/v1/handoff-events/"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[admin] starting...")

	cfg := config.Load()

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[admin] failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("[admin] failed to connect to database: %v", err)
	}
	log.Println("[admin] connected to PostgreSQL")

	// Connect to Redis
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("[admin] invalid REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(redisOpts)

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("[admin] failed to connect to Redis: %v", err)
	}
	log.Println("[admin] connected to Redis")

	// Initialize JWT manager
	jwtMgr := auth.NewJWTManager(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)

	// Initialize repositories
	userRepo := repository.NewUserRepository(db)
	plRepo := repository.NewProductLineRepository(db)
	channelRepo := repository.NewChannelRepository(db)
	// The prompt's authority. Dify holds a projection of what is in this table,
	// which is why both the tenant's settings page and the platform's push read
	// and write it rather than treating the Dify app as the store.
	promptVersionRepo := repository.NewPromptVersionRepository(db)

	// Parse AES encryption key for channel credential storage
	var aesKey []byte
	if cfg.AESEncryptionKey != "" {
		var err error
		aesKey, err = crypto.ParseHexKey(cfg.AESEncryptionKey)
		if err != nil {
			log.Fatalf("[admin] invalid AES_ENCRYPTION_KEY: %v", err)
		}
		log.Println("[admin] AES encryption key loaded")
	} else {
		log.Println("[admin] WARNING: AES_ENCRYPTION_KEY not set, channel credential encryption disabled")
		// Generate a temporary key for development only
		aesKey = make([]byte, 32)
		copy(aesKey, []byte("dev-only-key-not-for-production!"))
	}

	// Initialize the Dify bridge
	difyBridge := bridge.NewDifyBridge(bridge.DifyBridgeConfig{
		AdminURL:      cfg.DifyAdminURL,
		AdminToken:    cfg.DifyAdminToken,
		AdminEmail:    cfg.DifyAdminEmail,
		AdminPassword: cfg.DifyAdminPassword,
		APIBaseURL:    cfg.DifyAPIBaseURL,
		// Datasets are provisioned here but filled by the knowledge module, so
		// both have to be told the same indexing technique or the knowledge base
		// is created to be searched one way and populated to be searched another.
		IndexingTechnique: cfg.DifyIndexingTechnique,
	})

	// Chatwoot tenant provisioning needs the platform token, which is issued by
	// hand in the Super Admin console. Without it onboarding still runs and
	// reports the Chatwoot step as unavailable.
	var chatwootClient *bridge.ChatwootClient
	if cfg.ChatwootBaseURL != "" && cfg.ChatwootPlatformToken != "" {
		chatwootClient = bridge.NewChatwootClient(bridge.ChatwootConfig{
			BaseURL:       cfg.ChatwootBaseURL,
			PlatformToken: cfg.ChatwootPlatformToken,
		})
	} else {
		log.Println("[admin] WARNING: CHATWOOT_BASE_URL/CHATWOOT_PLATFORM_TOKEN not set, tenant onboarding will skip Chatwoot provisioning")
	}

	// Audit logging comes first: the identity layer writes its removal trail
	// through it, so the logger has to exist before the handlers do.
	auditRepo := audit.NewRepository(db)
	auditLogger := audit.NewLogger(auditRepo, 256)
	defer auditLogger.Close()
	log.Println("[admin] audit logger initialized")

	// Initialize handlers. The identity layer owns accounts and the tenant
	// lifecycle; everything a tenant owns afterwards is a handler of its own.
	authHandler := handler.NewAuthHandler(userRepo, jwtMgr, rdb, cfg.RefreshTokenTTL)
	userHandler := identity.NewUserHandler(userRepo, cfg.BcryptCost)
	tenantHandler := identity.NewTenantHandler(identity.TenantHandlerConfig{
		ProductLines:       plRepo,
		Users:              userRepo,
		DifyBridge:         difyBridge,
		DifyAdminEmail:     cfg.DifyAdminEmail,
		DifyAdminPassword:  cfg.DifyAdminPassword,
		Chatwoot:           chatwootClient,
		ChatwootWebhookURL: cfg.ChatwootWebhookURL,
		BcryptCost:         cfg.BcryptCost,
		Audit:              auditLogger,
		PromptVersions:     promptVersionRepo,
	})
	channelHandler := channels.NewHandler(channelRepo, aesKey, cfg.GatewayHost, rdb)
	knowledgeHandler := knowledge.NewHandler(plRepo,
		cfg.DifyAPIBaseURL, cfg.DifyDatasetAPIKey, cfg.DifyIndexingTechnique)
	if cfg.DifyDatasetAPIKey == "" {
		log.Println("[admin] WARNING: DIFY_DATASET_API_KEY not set, knowledge base management disabled")
	}
	// The AI settings module writes the guardrail block the runtime reads, and
	// drops the runtime's cached routes afterwards — hence the channel roster
	// and the Redis client.
	// One bridge shared by the console page and the platform endpoint, so both
	// read the same short-lived cache rather than each polling the router.
	routerBridge := bridge.NewRouterBridge(cfg.RouterInternalURL)

	aiSettingsHandler := aisettings.NewHandler(aisettings.Config{
		ProductLines:   plRepo,
		Channels:       channelRepo,
		Dify:           difyBridge,
		Redis:          rdb,
		Router:         routerBridge,
		PromptVersions: promptVersionRepo,
	})

	auditLogHandler := handler.NewAuditLogHandler(auditRepo)

	// Ontology editing and violation review surfaces. The store's cache is
	// irrelevant here (admin reads versions and evidence, not the hot path),
	// so a short TTL is fine.
	domainStore := domain.NewStore(db, time.Minute)
	factsHandler := facts.NewHandler(domainStore, plRepo, auditLogger, routecache.New(rdb, channelRepo))
	violationsHandler := quality.NewHandler(domainStore, plRepo, auditLogger)
	// The observation layer: violation concentration, dead-constraint coverage,
	// and the structured "why did this leave the AI" trail with its annotations.
	signalsHandler := quality.NewSignalsHandler(domainStore, plRepo, auditLogger)

	// The workbench trades the caller's own portal account for a password-free
	// Chatwoot session, provisioning the agent it signs in as when the account
	// carries none yet. Without a Chatwoot deployment the endpoint says so.
	workbenchHandler := workbench.NewHandler(workbench.Config{
		Tenants:  plRepo,
		Accounts: userRepo,
		Chatwoot: chatwootClient,
	})

	// Build middleware chain. Two gates carry the whole model: an administrator
	// gate for the platform surfaces, and the tenant gate for everything a
	// tenant owns.
	authMW := auth.AuthMiddleware(jwtMgr)
	requireAdmin := auth.RequireAdmin()
	tenantAuth := auth.TenantAuth(tenantPrefix)

	// Build audit middleware for auditable endpoints
	channelAuditMW := audit.Middleware(auditLogger, "channel_config",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, legacyChannelsPrefix)
			if len(segments) == 0 {
				return nil, "", "", nil
			}
			id := segments[0]
			cfg, err := channelRepo.GetByID(r.Context(), id)
			if err != nil || cfg == nil {
				return nil, id, "", err
			}
			data, _ := json.Marshal(cfg)
			return data, id, cfg.ProductLineID, nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			cfg, err := channelRepo.GetByID(r.Context(), resourceID)
			if err != nil || cfg == nil {
				return nil, err
			}
			data, _ := json.Marshal(cfg)
			return data, nil
		},
	)

	// The tenant a request under /api/v1/tenants/{id}/... acts on. The middleware
	// above has already resolved and authorised it, so the path segment is the
	// tenant itself rather than whatever the caller typed.
	auditTenantID := func(r *http.Request) string {
		segments := handler.ExtractPathSegments(r.URL.Path, tenantPrefix)
		if len(segments) == 0 {
			return ""
		}
		return segments[0]
	}

	// AI settings write one block of the tenant's config, so that block is the
	// before/after state — the rest of config_json belongs to other modules and
	// would only add noise a reviewer has to diff through.
	aiSettingsAuditMW := audit.Middleware(auditLogger, "ai_settings",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			tenantID := auditTenantID(r)
			if tenantID == "" {
				return nil, "", "", nil
			}
			state, err := aiSettingsHandler.AuditState(r.Context(), tenantID)
			return state, tenantID, tenantID, err
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			return aiSettingsHandler.AuditState(r.Context(), resourceID)
		},
	)

	// Knowledge requests mutate Dify documents, not a database row, so there is
	// no state to snapshot on either side. The before-state instead names the
	// document the request touches, so a delete row still says what was deleted.
	knowledgeAuditMW := audit.Middleware(auditLogger, "knowledge",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			tenantID := auditTenantID(r)
			if tenantID == "" {
				return nil, "", "", nil
			}
			target := map[string]string{"product_line_id": tenantID}
			segments := handler.ExtractPathSegments(r.URL.Path, tenantPrefix)
			if len(segments) > 3 && segments[2] == "documents" {
				target["document_id"] = segments[3]
			}
			data, _ := json.Marshal(target)
			return data, tenantID, tenantID, nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) { return nil, nil },
	)

	userAuditMW := audit.Middleware(auditLogger, "user",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/users/")
			if len(segments) == 0 {
				return nil, "", "", nil
			}
			id := segments[0]
			user, err := userRepo.GetByID(r.Context(), id)
			if err != nil || user == nil {
				return nil, id, "", err
			}
			data, _ := json.Marshal(user)
			return data, id, "", nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			user, err := userRepo.GetByID(r.Context(), resourceID)
			if err != nil || user == nil {
				return nil, err
			}
			data, _ := json.Marshal(user)
			return data, nil
		},
	)

	// Onboarding has no before state (the tenant is what the call brings into
	// existence) and its after state is the response itself, which the create
	// path extracts — the after function only has to be present for that path to
	// run. The response also carries the one-time passwords, so it is redacted
	// on the way into the trail.
	tenantAuditMW := audit.MiddlewareWithOptions(auditLogger, identity.CustomerAuditResource,
		nil,
		func(r *http.Request, resourceID string) (json.RawMessage, error) { return nil, nil },
		audit.Options{Redact: identity.RedactCustomerSecrets},
	)

	// Build HTTP mux
	mux := http.NewServeMux()

	// Public endpoints
	mux.HandleFunc("/api/v1/auth/login", authHandler.HandleLogin)
	mux.HandleFunc("/api/v1/auth/refresh", authHandler.HandleRefresh)

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Prometheus metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())

	// Accounts. Administrator territory throughout: an account carries the role
	// and the tenant that decide what it may reach, so handing out accounts is
	// handing out authority.
	mux.Handle("/api/v1/users", authMW(requireAdmin(userAuditMW(http.HandlerFunc(userHandler.HandleUsers)))))
	mux.Handle("/api/v1/users/", authMW(requireAdmin(userAuditMW(http.HandlerFunc(userHandler.HandleUser)))))

	// The tenant roster: list the tenants, or bring one into existence.
	//
	// The onboarding body is four short fields, and the audit middleware buffers
	// request bodies for replay, so the ceiling goes outside it: inside, a large
	// body would already have been read whole before any limit applied.
	onboardBodyLimit := handler.LimitRequestBody(1 << 20)
	mux.Handle("/api/v1/tenants", authMW(requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			tenantHandler.HandleTenants(w, r)
		case http.MethodPost:
			onboardBodyLimit(tenantAuditMW(http.HandlerFunc(tenantHandler.HandleOnboard))).ServeHTTP(w, r)
		default:
			handler.ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}))))

	// Everything one tenant owns. tenantAuth resolves {id} (including the "me"
	// alias) and applies the single rule — an administrator reaches any tenant,
	// a user only its own — so the dispatcher below decides only which module
	// serves the request, never whether it may be served.
	//
	// The knowledge upload is the largest body in this subtree and the audit
	// middleware buffers bodies for replay, so the ceiling goes outside it. The
	// slack above the handler's own 15 MB limit is what lets the handler answer
	// 413 rather than fail on a body truncated to exactly the limit.
	knowledgeBodyLimit := handler.LimitRequestBody(knowledge.MaxUploadBytes + (1 << 20))
	tenantResources := &tenantRouter{
		tenantRecord:     http.HandlerFunc(tenantHandler.HandleTenant),
		knowledge:        knowledgeBodyLimit(knowledgeAuditMW(http.HandlerFunc(knowledgeHandler.Handle))),
		aiSettings:       aiSettingsAuditMW(http.HandlerFunc(aiSettingsHandler.Handle)),
		channels:         channelAuditMW(http.HandlerFunc(channelHandler.HandleChannels)),
		channel:          channelAuditMW(http.HandlerFunc(channelHandler.HandleChannel)),
		ontology:         http.HandlerFunc(factsHandler.Handle),
		violationsByLine: http.HandlerFunc(violationsHandler.HandleByProductLine),
		violationReview:  http.HandlerFunc(violationsHandler.HandleReview),
		violationStats:   http.HandlerFunc(signalsHandler.HandleViolationStats),
		handoffs:         http.HandlerFunc(signalsHandler.HandleHandoffs),
		handoffAnnotate:  http.HandlerFunc(signalsHandler.HandleAnnotate),
		workbench:        http.HandlerFunc(workbenchHandler.Handle),
	}
	mux.Handle(tenantPrefix, authMW(tenantAuth(tenantResources)))

	// The audit trail. Open to any authenticated caller; the handler narrows a
	// tenant's user to its own rows.
	mux.Handle("/api/v1/audit-logs", authMW(http.HandlerFunc(auditLogHandler.HandleAuditLogs)))

	// What this deployment is set to, for an operator who would otherwise need a
	// shell on the router host. Read-only by nature: half of it is the router's
	// environment and the other half is compiled in, so neither can be written
	// through an API at all.
	platformHandler := platform.NewHandler(routerBridge)
	mux.Handle("/api/v1/platform/settings", authMW(http.HandlerFunc(platformHandler.Handle)))

	// The previous path kept, since it is what the runbook names.
	mux.Handle("/api/v1/platform/runtime", authMW(http.HandlerFunc(platformHandler.Handle)))

	// Which tenants are still on an older platform template, and the one
	// control that acts on the answer. Both are platform-scoped: falling behind
	// is caused by the template moving, so the roster is cross-tenant and the
	// push names its targets explicitly. Administrator-only, checked inside the
	// handler the way the settings endpoint checks it.
	//
	// The push writes each line's revision through the same repository the
	// tenant page writes, so a platform push and a tenant's own save land in one
	// history rather than two.
	promptsHandler := platform.NewPromptsHandler(platform.PromptsConfig{
		ProductLines: plRepo,
		Versions:     promptVersionRepo,
		Dify:         difyBridge,
		Audit:        auditLogger,
	})
	mux.Handle("/api/v1/platform/prompts", authMW(http.HandlerFunc(promptsHandler.HandleList)))
	mux.Handle("/api/v1/platform/prompts/push", authMW(http.HandlerFunc(promptsHandler.HandlePush)))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP server
	go func() {
		log.Printf("[admin] HTTP server listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[admin] HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[admin] received signal %s, shutting down...", sig)

	shutdownCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[admin] shutdown error: %v", err)
	}

	if err := rdb.Close(); err != nil {
		log.Printf("[admin] Redis close error: %v", err)
	}

	log.Println("[admin] shutdown complete")
}

// tenantRouter dispatches /api/v1/tenants/{id}/... to the module that owns the
// resource. The tenant is already resolved and authorised by the time it runs.
type tenantRouter struct {
	tenantRecord     http.Handler
	knowledge        http.Handler
	aiSettings       http.Handler
	channels         http.Handler
	channel          http.Handler
	ontology         http.Handler
	violationsByLine http.Handler
	violationReview  http.Handler
	violationStats   http.Handler
	handoffs         http.Handler
	handoffAnnotate  http.Handler
	workbench        http.Handler
}

// aiSettingsPaths is the closed list of ai-settings sub-paths. Listing them here
// closes the subtree: a name absent from it is a 404, not a request that reaches
// the module and is refused there.
var aiSettingsPaths = map[string]bool{
	"":                  true,
	"prompt":            true,
	"prompt/reset":      true,
	"prompt/rollback":   true,
	"prompt/versions":   true,
	"threshold":         true,
	"handoff-rules":     true,
	"dataset/bind":      true,
	"dataset/retrieval": true,
	"dataset/top-k":     true,
	"survey":            true,
	"variables/repair":  true,
	"test":              true,
}

// factsPaths map the tenant-facing "facts" vocabulary onto the ontology
// handler's own. "config" is the per-tenant switch block, which lives on a
// sibling path there rather than under the resource.
var factsPaths = map[string]string{
	"":         "ontology",
	"validate": "ontology/validate",
	"publish":  "ontology/publish",
	"rollback": "ontology/rollback",
	"config":   "ontology-config",
}

func (t *tenantRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segments := handler.ExtractPathSegments(r.URL.Path, tenantPrefix)
	if len(segments) == 0 {
		handler.ErrorJSON(w, http.StatusBadRequest, "tenant id required")
		return
	}
	tenantID := segments[0]
	rest := segments[1:]

	if len(rest) == 0 {
		t.serveTenant(w, r, tenantID)
		return
	}

	sub := strings.Join(rest[1:], "/")
	switch rest[0] {
	case "knowledge":
		// The tenant modules read the tenant route themselves, so their
		// requests travel on untouched.
		t.knowledge.ServeHTTP(w, r)
	case "ai-settings":
		if !aiSettingsPaths[sub] {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown ai-settings sub-path: "+sub)
			return
		}
		t.aiSettings.ServeHTTP(w, r)
	case "channels":
		// The tenant is carried in the request context, which is where the
		// channel handler reads its scope from; the legacy path only has to
		// name the resource.
		if sub == "" {
			t.serve(w, r, t.channels, legacyChannelsRoot)
			return
		}
		t.serve(w, r, t.channel, joinPath(legacyChannelsPrefix, sub))
	case "facts":
		mapped, ok := factsPaths[sub]
		if !ok {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown facts sub-path: "+sub)
			return
		}
		t.serve(w, r, t.ontology, joinPath(legacyProductLinesPrefix+tenantID, mapped))
	case "violations":
		if sub == "" {
			t.serve(w, r, t.violationsByLine, joinPath(legacyProductLinesPrefix+tenantID, "violations"))
			return
		}
		if sub == "stats" {
			t.serve(w, r, t.violationStats, joinPath(legacyProductLinesPrefix+tenantID, "violations/stats"))
			return
		}
		t.serve(w, r, t.violationReview, joinPath(legacyViolationsPrefix, sub))
	case "handoffs":
		// The list and its stats live on the product line; the annotation
		// addresses one event by its own id, like a violation review.
		if sub == "" || sub == "stats" {
			t.serve(w, r, t.handoffs, joinPath(legacyProductLinesPrefix+tenantID, "handoffs", sub))
			return
		}
		t.serve(w, r, t.handoffAnnotate, joinPath(legacyHandoffEventsPrefix, sub))
	case "workbench":
		if sub != "sso" {
			handler.ErrorJSON(w, http.StatusNotFound, "unknown workbench sub-path: "+sub)
			return
		}
		// The workbench reads the tenant route itself, so the request travels
		// on untouched.
		t.workbench.ServeHTTP(w, r)
	default:
		handler.ErrorJSON(w, http.StatusNotFound, "unknown tenant resource: "+rest[0])
	}
}

// serveTenant answers /api/v1/tenants/{id} itself: the tenant record, or its
// removal. Both are the identity layer's, which is why they reach the same
// handler; it refuses the removal to anyone but an administrator.
func (t *tenantRouter) serveTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		t.serve(w, r, t.tenantRecord, legacyProductLinesPrefix+tenantID)
	default:
		handler.ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serve rewrites the request path to the one the target handler parses and
// hands the request over. Only the path changes: the tenant in the context and
// the claims stay exactly as the middleware left them.
func (t *tenantRouter) serve(w http.ResponseWriter, r *http.Request, next http.Handler, path string) {
	u := *r.URL
	u.Path = path
	r2 := r.Clone(r.Context())
	r2.URL = &u
	next.ServeHTTP(w, r2)
}

// joinPath assembles a path from a base and the segments below it, skipping the
// empty ones so a resource root does not end up with a trailing slash.
func joinPath(base string, parts ...string) string {
	out := strings.TrimSuffix(base, "/")
	for _, p := range parts {
		if p == "" {
			continue
		}
		out += "/" + p
	}
	return out
}
