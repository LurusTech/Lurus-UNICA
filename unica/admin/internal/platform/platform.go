// Package platform answers what this deployment is set to, for an operator who
// would otherwise need a shell on the router host to find out.
//
// Everything here is read-only, and deliberately so: the values come from two
// places that cannot be written through an API at all. The switches are the
// router's environment, which changes when the router restarts; the rest are
// constants compiled into these binaries, which change when a version ships.
// An interface that let either be edited would be offering a control that ends
// at the next deploy.
//
// The division matters more than the contents. A value's source decides how it
// changes and who can change it, so each one is reported with its source rather
// than as a bare number in a list — otherwise the page reads as a settings
// screen with the save button missing.
package platform

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/rbac"
	"github.com/kefu/unica/admin/internal/tenant/knowledge"
	"github.com/kefu/unica/pkg/difyapp"
	"github.com/kefu/unica/pkg/guardrail"
	"github.com/kefu/unica/pkg/survey"
)

// SwitchReader is the router's live configuration, as reported by the router.
type SwitchReader interface {
	Switches(ctx context.Context) (*bridge.RuntimeSwitches, error)
}

// Handler serves GET /api/v1/platform/settings.
type Handler struct {
	router SwitchReader
}

func NewHandler(router SwitchReader) *Handler {
	return &Handler{router: router}
}

// runtimeSection is the router's own state. It is reported as unavailable
// rather than filled in from this service's environment when the router cannot
// be reached: a plausible wrong value here would be read as the setting
// messages are actually routed by, and this service does not have these values.
type runtimeSection struct {
	Available bool                    `json:"available"`
	Reason    string                  `json:"reason,omitempty"`
	Switches  *bridge.RuntimeSwitches `json:"switches,omitempty"`
}

// compiledSection is what is fixed until the next release. It is served whole
// rather than summarised: the point of showing it is that these texts decide
// how every product line answers, and a summary of a prompt is not a prompt.
type compiledSection struct {
	PromptTemplate     string                      `json:"prompt_template"`
	PromptRequirements []difyapp.PromptRequirement `json:"prompt_requirements"`
	SceneStrategies    []sceneStrategy             `json:"scene_strategies"`
	Guardrail          *guardrail.Config           `json:"guardrail_defaults"`
	Survey             *survey.Config              `json:"survey_defaults"`
	Model              difyapp.ModelSpec           `json:"model"`
	Knowledge          knowledgeDefaults           `json:"knowledge"`
}

type sceneStrategy struct {
	Stage string `json:"stage"`
	Text  string `json:"text"`
}

// knowledgeDefaults are the two decisions that bound what retrieval can ever
// return: how a document is cut up, and how the pieces are searched.
type knowledgeDefaults struct {
	IndexingTechnique string                 `json:"indexing_technique"`
	SearchMethod      string                 `json:"search_method"`
	TopK              int                    `json:"top_k"`
	ProcessRule       map[string]interface{} `json:"process_rule"`
}

type settingsResponse struct {
	Runtime  runtimeSection  `json:"runtime"`
	Compiled compiledSection `json:"compiled"`
}

// Handle answers with the deployment's settings. Administrator only: these are
// platform state, and a tenant shown them would be reading values it has no way
// to act on.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return
	}

	runtime := runtimeSection{}
	if h.router == nil {
		runtime.Reason = "router address not configured"
	} else if switches, err := h.router.Switches(r.Context()); err != nil {
		log.Printf("[platform] runtime switches unavailable: %v", err)
		runtime.Reason = err.Error()
	} else {
		runtime.Available = true
		runtime.Switches = switches
	}

	strategies := make([]sceneStrategy, 0, len(difyapp.Stages()))
	for _, stage := range difyapp.Stages() {
		strategies = append(strategies, sceneStrategy{Stage: stage, Text: difyapp.StrategyFor(stage)})
	}

	// The retrieval defaults are asked for by indexing technique, so they are
	// reported for the technique this deployment actually creates datasets
	// with. Reporting the other one would describe a deployment nobody is on.
	retrieval := difyapp.RetrievalModel(difyapp.IndexingHighQuality)
	method, _ := retrieval["search_method"].(string)
	topK, _ := retrieval["top_k"].(int)

	writeJSON(w, http.StatusOK, settingsResponse{
		Runtime: runtime,
		Compiled: compiledSection{
			PromptTemplate:     difyapp.PromptTemplate(),
			PromptRequirements: difyapp.PromptRequirements(),
			SceneStrategies:    strategies,
			Guardrail:          guardrail.Defaults(),
			Survey:             survey.Defaults(),
			Model:              difyapp.PlatformModel(),
			Knowledge: knowledgeDefaults{
				IndexingTechnique: difyapp.IndexingHighQuality,
				SearchMethod:      method,
				TopK:              topK,
				ProcessRule:       knowledge.DefaultProcessRule(),
			},
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
