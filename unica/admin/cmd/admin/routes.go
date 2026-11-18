package main

import (
	"encoding/json"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/config"
	"github.com/kefu/unica/admin/internal/handler"
	"github.com/kefu/unica/admin/internal/identity"
	"github.com/kefu/unica/admin/internal/tenant/knowledge"
)

type auditMiddlewares struct {
	channel    func(http.Handler) http.Handler
	aiSettings func(http.Handler) http.Handler
	knowledge  func(http.Handler) http.Handler
	user       func(http.Handler) http.Handler
	tenant     func(http.Handler) http.Handler
}

func extractAuditTenantID(r *http.Request) string {
	segments := handler.ExtractPathSegments(r.URL.Path, tenantPrefix)
	if len(segments) == 0 {
		return ""
	}
	return segments[0]
}

func buildAuditMiddlewares(s *adminStores, svc *adminServices) auditMiddlewares {
	channelAuditMW := audit.Middleware(s.auditLogger, "channel_config",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, legacyChannelsPrefix)
			if len(segments) == 0 {
				return nil, "", "", nil
			}
			id := segments[0]
			cfg, err := s.channelRepo.GetByID(r.Context(), id)
			if err != nil || cfg == nil {
				return nil, id, "", err
			}
			data, _ := json.Marshal(cfg)
			return data, id, cfg.ProductLineID, nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			cfg, err := s.channelRepo.GetByID(r.Context(), resourceID)
			if err != nil || cfg == nil {
				return nil, err
			}
			data, _ := json.Marshal(cfg)
			return data, nil
		},
	)

	aiSettingsAuditMW := audit.Middleware(s.auditLogger, "ai_settings",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			tenantID := extractAuditTenantID(r)
			if tenantID == "" {
				return nil, "", "", nil
			}
			state, err := svc.aiSettingsHandler.AuditState(r.Context(), tenantID)
			return state, tenantID, tenantID, err
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			return svc.aiSettingsHandler.AuditState(r.Context(), resourceID)
		},
	)

	knowledgeAuditMW := audit.Middleware(s.auditLogger, "knowledge",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			tenantID := extractAuditTenantID(r)
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

	userAuditMW := audit.Middleware(s.auditLogger, "user",
		func(r *http.Request) (json.RawMessage, string, string, error) {
			segments := handler.ExtractPathSegments(r.URL.Path, "/api/v1/users/")
			if len(segments) == 0 {
				return nil, "", "", nil
			}
			id := segments[0]
			user, err := s.userRepo.GetByID(r.Context(), id)
			if err != nil || user == nil {
				return nil, id, "", err
			}
			data, _ := json.Marshal(user)
			return data, id, "", nil
		},
		func(r *http.Request, resourceID string) (json.RawMessage, error) {
			user, err := s.userRepo.GetByID(r.Context(), resourceID)
			if err != nil || user == nil {
				return nil, err
			}
			data, _ := json.Marshal(user)
			return data, nil
		},
	)

	tenantAuditMW := audit.MiddlewareWithOptions(s.auditLogger, identity.CustomerAuditResource,
		nil,
		func(r *http.Request, resourceID string) (json.RawMessage, error) { return nil, nil },
		audit.Options{Redact: identity.RedactCustomerSecrets},
	)

	return auditMiddlewares{
		channel:    channelAuditMW,
		aiSettings: aiSettingsAuditMW,
		knowledge:  knowledgeAuditMW,
		user:       userAuditMW,
		tenant:     tenantAuditMW,
	}
}

func buildMux(_ *config.Config, s *adminStores, svc *adminServices) *http.ServeMux {
	authMW := auth.AuthMiddleware(s.jwtMgr)
	requireAdmin := auth.RequireAdmin()
	tenantAuth := auth.TenantAuth(tenantPrefix)
	audits := buildAuditMiddlewares(s, svc)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", svc.authHandler.HandleLogin)
	mux.HandleFunc("/api/v1/auth/refresh", svc.authHandler.HandleRefresh)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/metrics", promhttp.Handler())

	mux.Handle("/api/v1/users", authMW(requireAdmin(audits.user(http.HandlerFunc(svc.userHandler.HandleUsers)))))
	mux.Handle("/api/v1/users/", authMW(requireAdmin(audits.user(http.HandlerFunc(svc.userHandler.HandleUser)))))

	onboardBodyLimit := handler.LimitRequestBody(1 << 20)
	mux.Handle("/api/v1/tenants", authMW(requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			svc.tenantHandler.HandleTenants(w, r)
		case http.MethodPost:
			onboardBodyLimit(audits.tenant(http.HandlerFunc(svc.tenantHandler.HandleOnboard))).ServeHTTP(w, r)
		default:
			handler.ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}))))

	knowledgeBodyLimit := handler.LimitRequestBody(knowledge.MaxUploadBytes + (1 << 20))
	tenantResources := &tenantRouter{
		tenantRecord:     http.HandlerFunc(svc.tenantHandler.HandleTenant),
		knowledge:        knowledgeBodyLimit(audits.knowledge(http.HandlerFunc(svc.knowledgeHandler.Handle))),
		aiSettings:       audits.aiSettings(http.HandlerFunc(svc.aiSettingsHandler.Handle)),
		channels:         audits.channel(http.HandlerFunc(svc.channelHandler.HandleChannels)),
		channel:          audits.channel(http.HandlerFunc(svc.channelHandler.HandleChannel)),
		ontology:         http.HandlerFunc(svc.factsHandler.Handle),
		violationsByLine: http.HandlerFunc(svc.violationsHandler.HandleByProductLine),
		violationReview:  http.HandlerFunc(svc.violationsHandler.HandleReview),
		violationStats:   http.HandlerFunc(svc.signalsHandler.HandleViolationStats),
		handoffs:         http.HandlerFunc(svc.signalsHandler.HandleHandoffs),
		handoffAnnotate:  http.HandlerFunc(svc.signalsHandler.HandleAnnotate),
		proposals:        http.HandlerFunc(svc.proposalsHandler.Handle),
		versionQuality:   http.HandlerFunc(svc.signalsHandler.HandleVersionQuality),
		northstar:        http.HandlerFunc(svc.signalsHandler.HandleNorthstar),
		workbench:        http.HandlerFunc(svc.workbenchHandler.Handle),
		productLineModel: http.HandlerFunc(svc.modelOverrideHandler.Handle),
		evidence:         http.HandlerFunc(svc.evidenceHandler.Handle),
		compliance:       http.HandlerFunc(svc.complianceHandler.Handle),
		events:           http.HandlerFunc(svc.eventsHandler.Handle),
	}
	mux.Handle(tenantPrefix, authMW(tenantAuth(tenantResources)))

	mux.Handle("/api/v1/audit-logs", authMW(http.HandlerFunc(svc.auditLogHandler.HandleAuditLogs)))
	mux.Handle("/api/v1/platform/settings", authMW(http.HandlerFunc(svc.platformHandler.Handle)))
	mux.Handle("/api/v1/platform/runtime", authMW(http.HandlerFunc(svc.platformHandler.Handle)))
	mux.Handle("/api/v1/platform/model", authMW(http.HandlerFunc(svc.platformHandler.HandleModel)))
	mux.Handle("/api/v1/platform/dify-console/session", authMW(http.HandlerFunc(svc.platformHandler.HandleDifyConsoleSession)))
	mux.Handle("/api/v1/platform/switches", authMW(http.HandlerFunc(svc.platformHandler.HandleSwitches)))
	mux.Handle("/api/v1/platform/prompts", authMW(http.HandlerFunc(svc.promptsHandler.HandleList)))
	mux.Handle("/api/v1/platform/prompts/push", authMW(http.HandlerFunc(svc.promptsHandler.HandlePush)))
	mux.Handle("/api/v1/platform/models", authMW(http.HandlerFunc(svc.modelsHandler.HandleList)))
	mux.Handle("/api/v1/platform/models/push", authMW(http.HandlerFunc(svc.modelsHandler.HandlePush)))
	mux.Handle("/api/v1/platform/knowledge", authMW(http.HandlerFunc(svc.knowledgeRoster.HandleList)))
	mux.Handle("/api/v1/platform/digest", authMW(http.HandlerFunc(svc.digestHandler.HandleList)))

	return mux
}
