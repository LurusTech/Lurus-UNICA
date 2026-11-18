package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/capability"
	"github.com/kefu/unica/admin/internal/config"
	"github.com/kefu/unica/admin/internal/crypto"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/tenant/evidence"
	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/platformsettings"
)

type adminStores struct {
	db                *sql.DB
	rdb               *redis.Client
	jwtMgr            *auth.JWTManager
	aesKey            []byte
	userRepo          *repository.UserRepository
	plRepo            *repository.ProductLineRepository
	channelRepo       *repository.ChannelRepository
	promptVersionRepo *repository.PromptVersionRepository
	modelVersionRepo  *repository.ModelVersionRepository
	settingsStore     *platformsettings.Store
	indexingTechnique func(context.Context) string
	difyBridge        *bridge.DifyBridge
	chatwootClient    *bridge.ChatwootClient
	routerBridge      *bridge.RouterBridge
	capabilityProbe   *capability.Probe
	auditRepo         *audit.Repository
	auditLogger       *audit.Logger
	domainStore       *domain.Store
	evidenceRepo      *evidence.Repository
}

func initStores(ctx context.Context, cfg *config.Config, db *sql.DB, rdb *redis.Client) *adminStores {
	jwtMgr := auth.NewJWTManager(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)

	userRepo := repository.NewUserRepository(db)
	plRepo := repository.NewProductLineRepository(db)
	channelRepo := repository.NewChannelRepository(db)
	promptVersionRepo := repository.NewPromptVersionRepository(db)
	modelVersionRepo := repository.NewModelVersionRepository(db)

	aesKey := loadAESKey(cfg.AESEncryptionKey)

	settingsStore := platformsettings.NewStore(db)
	seedIndexingTechnique(ctx, settingsStore, cfg.DifyIndexingTechnique)
	indexingTechnique := storedIndexingTechnique(settingsStore, cfg.DifyIndexingTechnique)

	difyBridge := bridge.NewDifyBridge(bridge.DifyBridgeConfig{
		AdminURL:          cfg.DifyAdminURL,
		AdminToken:        cfg.DifyAdminToken,
		AdminEmail:        cfg.DifyAdminEmail,
		AdminPassword:     cfg.DifyAdminPassword,
		APIBaseURL:        cfg.DifyAPIBaseURL,
		IndexingTechnique: indexingTechnique,
	})

	var chatwootClient *bridge.ChatwootClient
	if cfg.ChatwootBaseURL != "" && cfg.ChatwootPlatformToken != "" {
		chatwootClient = bridge.NewChatwootClient(bridge.ChatwootConfig{
			BaseURL:       cfg.ChatwootBaseURL,
			PlatformToken: cfg.ChatwootPlatformToken,
		})
	} else {
		log.Println("[admin] WARNING: CHATWOOT_BASE_URL/CHATWOOT_PLATFORM_TOKEN not set, tenant onboarding will skip Chatwoot provisioning")
	}

	auditRepo := audit.NewRepository(db)
	auditLogger := audit.NewLogger(auditRepo, 256)
	log.Println("[admin] audit logger initialized")

	routerBridge := bridge.NewRouterBridge(cfg.RouterInternalURL)
	capabilityProbe := capability.NewProbe(cfg.DifyDatasetAPIKey, cfg.RouterInternalURL, routerBridge)

	return &adminStores{
		db:                db,
		rdb:               rdb,
		jwtMgr:            jwtMgr,
		aesKey:            aesKey,
		userRepo:          userRepo,
		plRepo:            plRepo,
		channelRepo:       channelRepo,
		promptVersionRepo: promptVersionRepo,
		modelVersionRepo:  modelVersionRepo,
		settingsStore:     settingsStore,
		indexingTechnique: indexingTechnique,
		difyBridge:        difyBridge,
		chatwootClient:    chatwootClient,
		routerBridge:      routerBridge,
		capabilityProbe:   capabilityProbe,
		auditRepo:         auditRepo,
		auditLogger:       auditLogger,
		domainStore:       domain.NewStore(db, time.Minute),
		evidenceRepo:      evidence.NewRepository(db),
	}
}

func loadAESKey(hexKey string) []byte {
	if hexKey != "" {
		aesKey, err := crypto.ParseHexKey(hexKey)
		if err != nil {
			log.Fatalf("[admin] invalid AES_ENCRYPTION_KEY: %v", err)
		}
		log.Println("[admin] AES encryption key loaded")
		return aesKey
	}
	log.Println("[admin] WARNING: AES_ENCRYPTION_KEY not set, channel credential encryption disabled")
	aesKey := make([]byte, 32)
	copy(aesKey, []byte("dev-only-key-not-for-production!"))
	return aesKey
}

// seedIndexingTechnique seeds DIFY_INDEXING_TECHNIQUE into platform_settings once
// and logs a warning if an already-stored value disagrees with the environment.
func seedIndexingTechnique(ctx context.Context, store *platformsettings.Store, fromEnv string) {
	inserted, err := store.Seed(ctx, platformsettings.KeyIndexingTechnique, fromEnv)
	switch {
	case errors.Is(err, platformsettings.ErrNoStore):
		return
	case err != nil:
		log.Printf("[admin] WARNING: could not store DIFY_INDEXING_TECHNIQUE=%q: %v; "+
			"new datasets will use the environment value until the table is reachable", fromEnv, err)
		return
	case inserted:
		log.Printf("[admin] indexing technique was not stored yet; seeded it with %q from DIFY_INDEXING_TECHNIQUE", fromEnv)
		return
	}

	rows, err := store.Load(ctx, platformsettings.KeyIndexingTechnique)
	if err != nil {
		return
	}
	if row, ok := rows[platformsettings.KeyIndexingTechnique]; ok && row.Value != fromEnv {
		log.Printf("[admin] WARNING: DIFY_INDEXING_TECHNIQUE=%q in this process's environment is being ignored; "+
			"the stored value %q (source %s) is what new datasets are created with. "+
			"Remove it from the deployment config to stop the disagreement", fromEnv, row.Value, row.Source)
	}
}

// storedIndexingTechnique resolves the active indexing technique per call from
// platform_settings, falling back to fromEnv if the store is unreadable.
func storedIndexingTechnique(store *platformsettings.Store, fromEnv string) func(context.Context) string {
	var lastReported string
	var mu sync.Mutex

	return func(ctx context.Context) string {
		rows, err := store.Load(ctx, platformsettings.KeyIndexingTechnique)
		if err != nil {
			if !errors.Is(err, platformsettings.ErrNoStore) {
				mu.Lock()
				if msg := err.Error(); msg != lastReported {
					lastReported = msg
					log.Printf("[admin] indexing technique unreadable (%v); using the environment value %q", err, fromEnv)
				}
				mu.Unlock()
			}
			return fromEnv
		}
		if row, ok := rows[platformsettings.KeyIndexingTechnique]; ok {
			return row.Value
		}
		return fromEnv
	}
}
