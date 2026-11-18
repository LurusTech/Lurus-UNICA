package main

import (
	"log"

	"github.com/kefu/unica/admin/internal/config"
	"github.com/kefu/unica/admin/internal/handler"
	"github.com/kefu/unica/admin/internal/identity"
	"github.com/kefu/unica/admin/internal/opsdigest"
	"github.com/kefu/unica/admin/internal/platform"
	"github.com/kefu/unica/admin/internal/routecache"
	"github.com/kefu/unica/admin/internal/tenant/aisettings"
	"github.com/kefu/unica/admin/internal/tenant/channels"
	tenantcompliance "github.com/kefu/unica/admin/internal/tenant/compliance"
	"github.com/kefu/unica/admin/internal/tenant/events"
	"github.com/kefu/unica/admin/internal/tenant/evidence"
	"github.com/kefu/unica/admin/internal/tenant/facts"
	"github.com/kefu/unica/admin/internal/tenant/knowledge"
	"github.com/kefu/unica/admin/internal/tenant/proposals"
	"github.com/kefu/unica/admin/internal/tenant/quality"
	"github.com/kefu/unica/admin/internal/tenant/workbench"
	"github.com/kefu/unica/pkg/difyapp"
)

type adminServices struct {
	authHandler           *handler.AuthHandler
	userHandler           *identity.UserHandler
	tenantHandler         *identity.TenantHandler
	channelHandler        *channels.Handler
	knowledgeHandler      *knowledge.Handler
	aiSettingsHandler     *aisettings.Handler
	modelOverrideHandler  *aisettings.ModelOverrideHandler
	auditLogHandler       *handler.AuditLogHandler
	factsHandler          *facts.Handler
	violationsHandler     *quality.Handler
	signalsHandler        *quality.SignalsHandler
	proposalsHandler      *proposals.Handler
	evidenceHandler       *evidence.Handler
	eventsHandler         *events.Handler
	complianceHandler     *tenantcompliance.Handler
	workbenchHandler      *workbench.Handler
	platformHandler       *platform.Handler
	promptsHandler        *platform.PromptsHandler
	modelsHandler         *platform.ModelsHandler
	knowledgeRoster       *platform.KnowledgeHandler
	digestCfg             opsdigest.Config
	digestBuilder         *opsdigest.Builder
	digestHandler         *platform.DigestHandler
}

func buildServices(cfg *config.Config, s *adminStores) *adminServices {
	authHandler := handler.NewAuthHandler(s.userRepo, s.jwtMgr, s.rdb, cfg.RefreshTokenTTL)
	userHandler := identity.NewUserHandler(s.userRepo, cfg.BcryptCost)
	tenantHandler := identity.NewTenantHandler(identity.TenantHandlerConfig{
		ProductLines:       s.plRepo,
		Users:              s.userRepo,
		DifyBridge:         s.difyBridge,
		DifyAdminEmail:     cfg.DifyAdminEmail,
		DifyAdminPassword:  cfg.DifyAdminPassword,
		Chatwoot:           s.chatwootClient,
		ChatwootWebhookURL: cfg.ChatwootWebhookURL,
		BcryptCost:         cfg.BcryptCost,
		Audit:              s.auditLogger,
		PromptVersions:     s.promptVersionRepo,
		ModelVersions:      s.modelVersionRepo,
	})
	channelHandler := channels.NewHandler(s.channelRepo, s.aesKey, cfg.GatewayHost, s.rdb)
	knowledgeHandler := knowledge.NewHandler(s.plRepo, cfg.DifyAPIBaseURL, cfg.DifyDatasetAPIKey, s.indexingTechnique)
	if cfg.DifyDatasetAPIKey == "" {
		log.Println("[admin] WARNING: DIFY_DATASET_API_KEY not set, knowledge base management disabled")
	}

	aiSettingsHandler := aisettings.NewHandler(aisettings.Config{
		ProductLines:      s.plRepo,
		Channels:          s.channelRepo,
		Dify:              s.difyBridge,
		Redis:             s.rdb,
		Router:            s.routerBridge,
		PromptVersions:    s.promptVersionRepo,
		Provisioner:       tenantHandler,
		IndexingTechnique: s.indexingTechnique,
		Capabilities:      s.capabilityProbe,
	})

	modelOverrideHandler := aisettings.NewModelOverrideHandler(aisettings.ModelOverrideConfig{
		ProductLines: s.plRepo,
		Versions:     s.modelVersionRepo,
		Dify:         s.difyBridge,
		Audit:        s.auditLogger,
	})

	auditLogHandler := handler.NewAuditLogHandler(s.auditRepo)
	factsHandler := facts.NewHandler(s.domainStore, s.plRepo, s.auditLogger, routecache.New(s.rdb, s.channelRepo))
	violationsHandler := quality.NewHandler(s.domainStore, s.plRepo, s.auditLogger)
	signalsHandler := quality.NewSignalsHandler(s.domainStore, s.plRepo, s.auditLogger)
	proposalsHandler := proposals.NewHandler(s.domainStore, s.plRepo, s.auditLogger)
	evidenceHandler := evidence.NewHandler(s.evidenceRepo, s.plRepo, s.auditLogger, routecache.New(s.rdb, s.channelRepo))
	eventsHandler := events.NewHandler(s.domainStore, s.plRepo)
	complianceHandler := tenantcompliance.NewHandler(s.plRepo, s.auditLogger, routecache.New(s.rdb, s.channelRepo))

	workbenchHandler := workbench.NewHandler(workbench.Config{
		Tenants:  s.plRepo,
		Accounts: s.userRepo,
		Chatwoot: s.chatwootClient,
	})

	platformCfg := platform.SettingsConfig{
		Router:            s.routerBridge,
		Models:            s.modelVersionRepo,
		ProductLines:      s.plRepo,
		Dify:              s.difyBridge,
		Console:           s.difyBridge,
		Audit:             s.auditLogger,
		IndexingTechnique: s.indexingTechnique,
		Capabilities:      s.capabilityProbe,
	}
	if s.settingsStore != nil {
		platformCfg.Settings = s.settingsStore
	}
	platformHandler := platform.NewSettingsHandler(platformCfg)

	promptsHandler := platform.NewPromptsHandler(platform.PromptsConfig{
		ProductLines: s.plRepo,
		Versions:     s.promptVersionRepo,
		Dify:         s.difyBridge,
		Audit:        s.auditLogger,
	})

	modelsHandler := platform.NewModelsHandler(platform.ModelsConfig{
		ProductLines: s.plRepo,
		Versions:     s.modelVersionRepo,
		Dify:         s.difyBridge,
		Audit:        s.auditLogger,
	})

	knowledgeRosterCfg := platform.KnowledgeConfig{ProductLines: s.plRepo, Dify: s.difyBridge}
	if cfg.DifyDatasetAPIKey != "" {
		knowledgeRosterCfg.Documents = difyapp.NewDatasetClient(cfg.DifyAPIBaseURL, cfg.DifyDatasetAPIKey)
	}
	knowledgeRoster := platform.NewKnowledgeHandler(knowledgeRosterCfg)

	digestCfg := opsdigest.ConfigFromEnv()
	digestBuilder := opsdigest.NewBuilder(s.domainStore, s.plRepo, digestCfg.PortalBaseURL)
	digestHandler := platform.NewDigestHandler(digestBuilder, digestCfg.Weekday)

	return &adminServices{
		authHandler:          authHandler,
		userHandler:          userHandler,
		tenantHandler:        tenantHandler,
		channelHandler:       channelHandler,
		knowledgeHandler:     knowledgeHandler,
		aiSettingsHandler:    aiSettingsHandler,
		modelOverrideHandler: modelOverrideHandler,
		auditLogHandler:      auditLogHandler,
		factsHandler:         factsHandler,
		violationsHandler:    violationsHandler,
		signalsHandler:       signalsHandler,
		proposalsHandler:     proposalsHandler,
		evidenceHandler:      evidenceHandler,
		eventsHandler:        eventsHandler,
		complianceHandler:    complianceHandler,
		workbenchHandler:     workbenchHandler,
		platformHandler:      platformHandler,
		promptsHandler:       promptsHandler,
		modelsHandler:        modelsHandler,
		knowledgeRoster:      knowledgeRoster,
		digestCfg:            digestCfg,
		digestBuilder:        digestBuilder,
		digestHandler:        digestHandler,
	}
}
