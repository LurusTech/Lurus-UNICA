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
		AdminURL:   cfg.DifyAdminURL,
		AdminToken: cfg.DifyAdminToken,
		APIBaseURL: cfg.DifyAPIBaseURL,
	})

	// Initialize handlers
	authHandler := handler.NewAuthHandler(userRepo, roleRepo, jwtMgr, rdb, cfg.RefreshTokenTTL)
	userHandler := handler.NewUserHandler(userRepo, roleRepo, cfg.BcryptCost)
	roleHandler := handler.NewRoleHandler(roleRepo, userRepo)
	plHandler := handler.NewProductLineHandler(plRepo, difyBridge, cfg.DifyAdminEmail, cfg.DifyAdminPassword)
	channelHandler := handler.NewChannelHandler(channelRepo, aesKey, cfg.GatewayHost, rdb)
	aiConfigHandler := handler.NewAIConfigHandler(aiConfigRepo, plRepo, difyBridge, rdb)

	// Initialize audit logging
	auditRepo := audit.NewRepository(db)
	auditLogger := audit.NewLogger(auditRepo, 256)
	defer auditLogger.Close()
	auditLogHandler := handler.NewAuditLogHandler(auditRepo)
	log.Println("[admin] audit logger initialized")

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

	aiConfigAuditMW := audit.Middleware(auditLogger, "ai_config",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/ai-config/")
			if len(segments) == 0 {
				return nil, "", "", nil
			}
			plID := segments[0]
			cfg, err := aiConfigRepo.GetByProductLineID(r.Context(), plID)
			if err != nil {
				return nil, plID, plID, err
			}
			data, _ := json.Marshal(cfg)
			return data, plID, plID, nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
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
	mux.Handle("/api/v1/product-lines/", authMW(requireManagePL(http.HandlerFunc(plHandler.HandleProductLine))))

	// Protected endpoints - Channels (with audit middleware)
	mux.Handle("/api/v1/channels", authMW(requireManageChannels(channelAuditMW(http.HandlerFunc(channelHandler.HandleChannels)))))
	mux.Handle("/api/v1/channels/", authMW(requireManageChannels(channelAuditMW(http.HandlerFunc(channelHandler.HandleChannel)))))

	// Protected endpoints - AI Config (with audit middleware)
	mux.Handle("/api/v1/ai-config/", authMW(requireManageAIConfig(aiConfigAuditMW(http.HandlerFunc(aiConfigHandler.HandleAIConfig)))))

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
