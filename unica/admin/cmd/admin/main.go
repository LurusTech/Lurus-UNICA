package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	_ "github.com/kefu/unica/admin/internal/metrics" // register Prometheus metrics
	"github.com/kefu/unica/admin/internal/config"
	"github.com/kefu/unica/admin/internal/crypto"
	"github.com/kefu/unica/admin/internal/handler"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
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
	roleRepo := repository.NewRoleRepository(db)
	plRepo := repository.NewProductLineRepository(db)
	channelRepo := repository.NewChannelRepository(db)

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

	// Initialize AI config repository and Dify bridge
	aiConfigRepo := repository.NewAIConfigRepository(db)
	difyBridge := bridge.NewDifyBridge(bridge.DifyBridgeConfig{
		AdminURL:      cfg.DifyAdminURL,
		AdminToken:    cfg.DifyAdminToken,
		AdminEmail:    cfg.DifyAdminEmail,
		AdminPassword: cfg.DifyAdminPassword,
		APIBaseURL:    cfg.DifyAPIBaseURL,
		// Datasets are provisioned here but filled by the AI-config handler, so
		// both have to be told the same indexing technique or the knowledge base
		// is created to be searched one way and populated to be searched another.
		IndexingTechnique: cfg.DifyIndexingTechnique,
	})

	// Initialize handlers
	authHandler := handler.NewAuthHandler(userRepo, roleRepo, jwtMgr, rdb, cfg.RefreshTokenTTL)
	userHandler := handler.NewUserHandler(userRepo, roleRepo, cfg.BcryptCost)
	roleHandler := handler.NewRoleHandler(roleRepo, userRepo)
	plHandler := handler.NewProductLineHandler(plRepo, difyBridge, cfg.DifyAdminEmail, cfg.DifyAdminPassword)
	channelHandler := handler.NewChannelHandler(channelRepo, aesKey, cfg.GatewayHost, rdb)
	aiConfigHandler := handler.NewAIConfigHandler(aiConfigRepo, plRepo, difyBridge, rdb,
		cfg.DifyAPIBaseURL, cfg.DifyDatasetAPIKey, cfg.DifyIndexingTechnique)
	if cfg.DifyDatasetAPIKey == "" {
		log.Println("[admin] WARNING: DIFY_DATASET_API_KEY not set, knowledge base management disabled")
	}

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
		log.Println("[admin] WARNING: CHATWOOT_BASE_URL/CHATWOOT_PLATFORM_TOKEN not set, customer onboarding will skip Chatwoot provisioning")
	}
	customerHandler := handler.NewCustomerHandler(plRepo, userRepo, roleRepo, plHandler,
		chatwootClient, cfg.ChatwootWebhookURL, cfg.BcryptCost)

	// Initialize audit logging
	auditRepo := audit.NewRepository(db)
	auditLogger := audit.NewLogger(auditRepo, 256)
	defer auditLogger.Close()
	auditLogHandler := handler.NewAuditLogHandler(auditRepo)
	log.Println("[admin] audit logger initialized")

	// Ontology editing and violation review surfaces. The store's cache is
	// irrelevant here (admin reads versions and evidence, not the hot path),
	// so a short TTL is fine.
	domainStore := domain.NewStore(db, time.Minute)
	ontologyHandler := handler.NewOntologyHandler(domainStore, plRepo, auditLogger)
	violationsHandler := handler.NewViolationsHandler(domainStore, plRepo, auditLogger)

	// Build middleware chain
	authMW := auth.AuthMiddleware(jwtMgr)
	requireManageUsers := auth.RequirePermission(rbac.PermManageUsers)
	requireManagePL := auth.RequirePermission(rbac.PermManageProductLines)
	requireManageChannels := auth.RequirePermission(rbac.PermManageChannels)
	requireManageAIConfig := auth.RequirePermission(rbac.PermManageAIConfig)
	requireViewAuditLogs := auth.RequirePermission(rbac.PermViewAuditLogs)

	// Build audit middleware for auditable endpoints
	channelAuditMW := audit.Middleware(auditLogger, "channel_config",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/channels/")
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

	// Knowledge sub-paths mutate Dify documents, not the ai_agent_configs row;
	// snapshotting that row around them would pair a before-state of one entity
	// with an after-state of another. Their before-state instead names the
	// document the request touches, so a delete row still says what was deleted.
	isKnowledgePath := func(segments []string) bool {
		return len(segments) > 1 && segments[1] == "knowledge"
	}
	aiConfigAuditMW := audit.Middleware(auditLogger, "ai_config",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/ai-config/")
			if len(segments) == 0 {
				return nil, "", "", nil
			}
			plID := segments[0]
			if isKnowledgePath(segments) {
				target := map[string]string{"product_line_id": plID}
				if len(segments) > 3 && segments[2] == "documents" {
					target["document_id"] = segments[3]
				}
				data, _ := json.Marshal(target)
				return data, plID, plID, nil
			}
			cfg, err := aiConfigRepo.GetByProductLineID(r.Context(), plID)
			if err != nil {
				return nil, plID, plID, err
			}
			data, _ := json.Marshal(cfg)
			return data, plID, plID, nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/ai-config/")
			if isKnowledgePath(segments) {
				return nil, nil
			}
			cfg, err := aiConfigRepo.GetByProductLineID(r.Context(), resourceID)
			if err != nil {
				return nil, err
			}
			data, _ := json.Marshal(cfg)
			return data, nil
		},
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

	roleAuditMW := audit.Middleware(auditLogger, "role_assignment",
		nil, // no before state for role assignment create
		nil, // after state captured from response body
	)

	// Onboarding has no before state (the customer is what the call brings into
	// existence) and its after state is the response itself, which the create
	// path extracts — the after function only has to be present for that path to
	// run. The response also carries the one-time passwords, so it is redacted
	// on the way into the trail.
	customerAuditMW := audit.MiddlewareWithOptions(auditLogger, handler.CustomerAuditResource,
		nil,
		func(r *http.Request, resourceID string) (json.RawMessage, error) { return nil, nil },
		audit.Options{Redact: handler.RedactCustomerSecrets},
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

	// Protected endpoints - Users (with audit middleware)
	mux.Handle("/api/v1/users", authMW(requireManageUsers(userAuditMW(http.HandlerFunc(userHandler.HandleUsers)))))
	mux.Handle("/api/v1/users/", authMW(requireManageUsers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route to role handlers for /users/:id/roles paths
		segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/users/")
		if len(segments) >= 2 && segments[1] == "roles" {
			if len(segments) == 2 {
				roleAuditMW(http.HandlerFunc(roleHandler.HandleAssignRole)).ServeHTTP(w, r)
			} else if len(segments) == 3 {
				roleAuditMW(http.HandlerFunc(roleHandler.HandleRemoveRole)).ServeHTTP(w, r)
			} else {
				handler.ErrorJSON(w, http.StatusNotFound, "not found")
			}
			return
		}
		userAuditMW(http.HandlerFunc(userHandler.HandleUser)).ServeHTTP(w, r)
	}))))

	// Protected endpoints - Customer onboarding. The call creates a product line
	// and a portal account in one step, so it demands both permissions; the
	// permission matrix grants that pair to super admins only.
	//
	// The onboarding body is four short fields, and the audit middleware buffers
	// request bodies for replay, so the ceiling goes outside it: inside, a large
	// body would already have been read whole before any limit applied.
	customerBodyLimit := handler.LimitRequestBody(1 << 20)
	mux.Handle("/api/v1/customers", authMW(requireManageUsers(requireManagePL(
		customerBodyLimit(customerAuditMW(http.HandlerFunc(customerHandler.HandleCustomers)))))))

	// Protected endpoints - Product Lines
	// GET (list) is scoped by the caller's claims and open to any authenticated
	// user; POST (create) is a mutation and must require the manage permission.
	mux.Handle("/api/v1/product-lines", authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			plHandler.HandleProductLines(w, r)
			return
		}
		requireManagePL(http.HandlerFunc(plHandler.HandleProductLines)).ServeHTTP(w, r)
	})))
	// The ontology sub-resources are policy editing, not product-line
	// administration, so they are gated by the AI-config permission while the
	// rest of the subtree keeps requiring the manage-product-lines permission.
	// Audit for these mutations is written inside the handler: subtree-level
	// audit middleware would start auditing every product-line request.
	mux.Handle("/api/v1/product-lines/", authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/product-lines/")
		if len(segments) >= 2 {
			switch segments[1] {
			case "ontology", "ontology-config":
				requireManageAIConfig(http.HandlerFunc(ontologyHandler.Handle)).ServeHTTP(w, r)
				return
			case "violations":
				requireManageAIConfig(http.HandlerFunc(violationsHandler.HandleByProductLine)).ServeHTTP(w, r)
				return
			}
		}
		requireManagePL(http.HandlerFunc(plHandler.HandleProductLine)).ServeHTTP(w, r)
	})))

	// Violation review is addressed by violation id, not product line; the
	// handler resolves the line from the row and applies scoping itself.
	mux.Handle("/api/v1/violations/", authMW(requireManageAIConfig(http.HandlerFunc(violationsHandler.HandleReview))))

	// Protected endpoints - Channels (with audit middleware)
	mux.Handle("/api/v1/channels", authMW(requireManageChannels(channelAuditMW(http.HandlerFunc(channelHandler.HandleChannels)))))
	mux.Handle("/api/v1/channels/", authMW(requireManageChannels(channelAuditMW(http.HandlerFunc(channelHandler.HandleChannel)))))

	// Protected endpoints - AI Config (with audit middleware)
	// The knowledge upload is the only large body in this subtree, and the audit
	// middleware buffers bodies for replay, so the ceiling goes outside it. The
	// slack above the handler's own 15 MB limit is what lets the handler answer
	// 413 rather than fail on a body truncated to exactly the limit.
	aiConfigBodyLimit := handler.LimitRequestBody(handler.MaxKnowledgeUploadBytes + (1 << 20))
	mux.Handle("/api/v1/ai-config/", authMW(requireManageAIConfig(aiConfigBodyLimit(aiConfigAuditMW(http.HandlerFunc(aiConfigHandler.HandleAIConfig))))))

	// Protected endpoints - Audit Logs (SuperAdmin and ProductAdmin only)
	mux.Handle("/api/v1/audit-logs", authMW(requireViewAuditLogs(http.HandlerFunc(auditLogHandler.HandleAuditLogs))))

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
