// Package bridge provides Dify API integration for the admin service.
// It proxies console-API work (app provisioning, system prompt) and app-key chat
// calls to the Dify platform on behalf of knowledge/product admins. The dataset
// (knowledge) endpoints are not here: they need a dataset-type key and live in
// the shared difyapp client.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kefu/unica/pkg/difyapp"
)

// DifyBridgeConfig holds connection parameters for the Dify APIs.
type DifyBridgeConfig struct {
	// AdminURL is the Dify console API root (e.g. "http://dify:5001/console/api").
	AdminURL string
	// AdminToken is the authentication token for the Dify console API. Console
	// tokens expire, so most deployments leave this empty and configure
	// AdminEmail/AdminPassword instead.
	AdminToken string
	// AdminEmail and AdminPassword let the bridge mint console tokens on
	// demand when AdminToken is empty. Without either, every console call
	// that is not handed an explicit per-call token fails.
	AdminEmail    string
	AdminPassword string
	// APIBaseURL is the Dify service API root (e.g. "http://dify:5001/v1").
	APIBaseURL string
	// IndexingTechnique is how this deployment indexes knowledge documents.
	// A dataset's retrieval settings have to agree with it: Dify defaults a new
	// dataset to semantic search, which finds nothing in one indexed by
	// keywords. Empty means the high-quality default.
	IndexingTechnique string
}

// DifyBridge communicates with the Dify platform for AI config management.
type DifyBridge struct {
	httpClient *http.Client
	// chatClient is for calls that wait on a language model, where the bound
	// belongs to the caller's context rather than to a client-wide timeout.
	chatClient *http.Client
	config     DifyBridgeConfig

	// Cached console token minted via Login when no static AdminToken is
	// configured. Dify console tokens outlive any single request but do
	// expire, so the cache is time-bounded and invalidated on a 401.
	tokenMu       sync.Mutex
	cachedToken   string
	tokenMintedAt time.Time
}

// consoleTokenTTL bounds how long a minted console token is reused. Dify
// 0.15.x issues 60-minute tokens; half that leaves a wide safety margin, and
// the 401-retry path below catches early expiry anyway.
const consoleTokenTTL = 30 * time.Minute

// NewDifyBridge creates a new Dify bridge client.
//
// Two clients, because the calls have nothing in common but a host. Console
// calls read and write configuration and should never take thirty seconds; a
// chat call waits on a language model and routinely takes longer. One client
// with one timeout meant the shorter requirement silently governed the longer
// call, and a test question that took 31 seconds came back as a transport
// error — from the page whose whole job is to say whether the settings work.
func NewDifyBridge(cfg DifyBridgeConfig) *DifyBridge {
	return &DifyBridge{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		// No Timeout of its own: the caller's context is the bound, because
		// only the caller knows how long this particular question may take.
		// The backstop is there for a caller that passes a context with no
		// deadline at all, which would otherwise hang until the process ends.
		chatClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		config: cfg,
	}
}

// --- Response types ---

// AppInfo represents basic app information from Dify.
type AppInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Model is what the app actually answers with. It was not reported before,
	// which is why five product lines could sit on two different models with
	// nothing in any interface saying so.
	Model *AppModelInfo `json:"model,omitempty"`
	// Variables reports whether the app declares the inputs the router sends.
	// An undeclared input is dropped by Dify without an error, so this is the
	// difference between "the ontology had nothing to say" and "the ontology was
	// never delivered" — indistinguishable from the answer alone.
	Variables *AppVariablesInfo `json:"variables,omitempty"`
}

// AppVariablesInfo is the reconciliation between the inputs the router sends
// and the ones the app has declared.
type AppVariablesInfo struct {
	Declared []string `json:"declared"`
	Missing  []string `json:"missing"`
	// Complete is false when any context variable the router sends is not
	// declared. Those inputs are being discarded before the model sees them.
	Complete bool `json:"complete"`
}

// AppModelInfo is the model an app is configured with, flattened for display.
type AppModelInfo struct {
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	// Pinned reports whether this matches the platform model. False means the
	// app has drifted and its answers are not comparable with the other lines'.
	Pinned bool `json:"pinned"`
}

// RetrievedSegment is one piece of the knowledge base that an answer was
// allowed to draw on.
//
// The names are kept, not just the score. "How many segments came back" is
// answerable from a count, but "why did it answer like that" needs to know
// which documents they came from — and a recall of the wrong three documents
// looks exactly like a recall of the right three until you can read them.
type RetrievedSegment struct {
	DatasetID    string  `json:"dataset_id,omitempty"`
	DatasetName  string  `json:"dataset_name,omitempty"`
	DocumentID   string  `json:"document_id,omitempty"`
	DocumentName string  `json:"document_name,omitempty"`
	Score        float64 `json:"score"`
	Content      string  `json:"content,omitempty"`
}

// TestMessageResponse represents the response from a Dify test chat message.
//
// Dify has returned retriever_resources in two places across versions: at the
// top level and under metadata. Both are read, because a console that reported
// "0 segments" for the other shape would be describing a broken knowledge base
// that is in fact working — the most expensive kind of wrong answer this page
// can give.
type TestMessageResponse struct {
	Answer             string             `json:"answer"`
	ConversationID     string             `json:"conversation_id"`
	Confidence         float64            `json:"confidence,omitempty"`
	RetrieverResources []RetrievedSegment `json:"retriever_resources,omitempty"`
	Metadata           struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		RetrieverResources []RetrievedSegment `json:"retriever_resources,omitempty"`
	} `json:"metadata"`
}

// Retrieved returns the segments the answer drew on, from whichever place this
// Dify version put them.
func (r *TestMessageResponse) Retrieved() []RetrievedSegment {
	if len(r.RetrieverResources) > 0 {
		return r.RetrieverResources
	}
	return r.Metadata.RetrieverResources
}

// DifyAppCreated holds the identifiers returned after provisioning a Dify chat app.
type DifyAppCreated struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode,omitempty"`
}

// DifyDatasetCreated holds the identifiers returned after provisioning a Dify dataset.
type DifyDatasetCreated struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DifyAPIKeyCreated holds an API key generated for a Dify app.
type DifyAPIKeyCreated struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// difyLoginResponse mirrors the Dify console login API response envelope.
type difyLoginResponse struct {
	Result string `json:"result"`
	Data   struct {
		AccessToken string `json:"access_token"`
	} `json:"data"`
}

// --- API Methods ---

// APIBaseURL returns the configured Dify service API base URL, exposed so callers
// (e.g. handlers writing back a provisioning result) can persist it alongside app/dataset IDs.
func (b *DifyBridge) APIBaseURL() string {
	return b.config.APIBaseURL
}

// Login authenticates against the Dify console API with an admin email/password and
// returns an access token that can be passed to the other provisioning methods.
func (b *DifyBridge) Login(ctx context.Context, email, password string) (string, error) {
	if b.config.AdminURL == "" {
		return "", fmt.Errorf("dify admin URL is empty")
	}
	if email == "" || password == "" {
		return "", fmt.Errorf("dify admin email/password is empty")
	}

	reqBody := map[string]interface{}{
		"email":       email,
		"password":    password,
		"remember_me": true,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal login request: %w", err)
	}

	url := b.config.AdminURL + "/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dify login: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}

	log.Printf("[dify-bridge] POST /login -> %d (%s)", resp.StatusCode, elapsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from POST /login: %s", resp.StatusCode, string(body))
	}

	var loginResp difyLoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return "", fmt.Errorf("unmarshal login response: %w", err)
	}
	if loginResp.Data.AccessToken == "" {
		return "", fmt.Errorf("dify login response missing access_token")
	}

	return loginResp.Data.AccessToken, nil
}

// CreateChatApp provisions a new Dify chat app in the default workspace and applies the
// default system prompt to it, mirroring UpdateSystemPrompt semantics.
//
// appName and productLineName are separate because they are different things
// that happened to be one argument. appName is how the app is listed in the
// Dify console, and this deployment prefixes it ("UNICA-XDYX") so a shared
// workspace stays legible. productLineName is the name the assistant answers
// with, and it went into the prompt as "你是UNICA-XDYX的在线客服" — the
// platform's own provisioning convention, spoken to customers. It also left
// every freshly provisioned line looking unlike the platform template it had
// just been given, because the console writes that template with the product
// line's own name.
func (b *DifyBridge) CreateChatApp(ctx context.Context, token, appName, productLineName string) (*DifyAppCreated, error) {
	name := appName
	reqBody := map[string]interface{}{
		"name":        name,
		"mode":        "chat",
		"description": fmt.Sprintf("Customer service chat app for %s", name),
	}
	body, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps", reqBody, token)
	if err != nil {
		return nil, fmt.Errorf("create chat app %q: %w", name, err)
	}

	var resp DifyAppCreated
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal create app response: %w", err)
	}
	log.Printf("[dify-bridge] created chat app %q (id=%s)", resp.Name, resp.ID)

	// Setting the system prompt requires a configured model provider, which usually
	// does not exist yet at provisioning time. Treat failure as non-fatal: the prompt
	// can be configured later in the Dify console or via the AI-config API.
	prompt := difyapp.DefaultSystemPrompt(productLineName)
	if err := b.updateSystemPromptWithToken(ctx, resp.ID, prompt, token); err != nil {
		log.Printf("[dify-bridge] WARN: default system prompt not applied for app_id=%s (configure it in Dify after adding a model provider): %v", resp.ID, err)
	} else {
		log.Printf("[dify-bridge] set default system prompt for app_id=%s (len=%d)", resp.ID, len(prompt))
	}

	return &resp, nil
}

// CreateDataset provisions a new Dify dataset (knowledge base) in the default
// workspace and sets retrieval settings that match how its documents will be
// indexed. Dify's own defaults are semantic search — which retrieves nothing at
// all from a keyword-indexed dataset — and a top_k of 2, which is only enough
// when ranking is reliable.
//
// Two calls, not one: the create endpoint ignores a retrieval_model in its body
// without complaint, so settings sent there are simply lost. They have to be
// PATCHed onto the dataset afterwards.
//
// Three results, not two, because those two calls fail separately and the
// second failure is the silent one. A dataset that exists but kept Dify's
// defaults accepts documents, indexes them, reports itself healthy, and answers
// every question with nothing — so the caller has to be told, and it has to be
// told while still holding the dataset ID it must persist. retrievalErr is that
// second outcome; err means no dataset was created at all. The bridge does not
// decide whether retrievalErr is worth aborting for — onboarding continues past
// it, a repair does not — but it no longer decides not to mention it.
func (b *DifyBridge) CreateDataset(ctx context.Context, token, name string) (dataset *DifyDatasetCreated, retrievalErr error, err error) {
	reqBody := map[string]interface{}{
		"name":        name,
		"description": fmt.Sprintf("Knowledge base for %s", name),
	}
	body, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/datasets", reqBody, token)
	if err != nil {
		return nil, nil, fmt.Errorf("create dataset %q: %w", name, err)
	}

	var resp DifyDatasetCreated
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("unmarshal create dataset response: %w", err)
	}
	log.Printf("[dify-bridge] created dataset %q (id=%s)", resp.Name, resp.ID)

	// Logged as well as returned: the log names the dataset for whoever is
	// reading the deployment's output, and the return is what lets an interface
	// tell the person who pressed the button.
	// The platform's top_k is passed explicitly, and only here. A repair reads
	// the dataset's current top_k back as an override so it cannot roll back a
	// value an administrator chose — but a dataset created a moment ago has no
	// such choice, only Dify's own default of 2, and preserving that as though
	// someone had picked it is how every new line was born retrieving a third
	// of what the platform asks for while every interface called it configured.
	if err := b.SetDatasetRetrievalWith(ctx, resp.ID, token,
		RetrievalOverrides{TopK: difyapp.DefaultTopK()}); err != nil {
		log.Printf("[dify-bridge] WARN: dataset %s kept Dify's default retrieval settings; "+
			"it will accept documents and retrieve nothing from them until repaired: %v", resp.ID, err)
		return &resp, err, nil
	}
	return &resp, nil, nil
}

// SetDatasetRetrieval applies this deployment's retrieval settings to a dataset
// and verifies they took. Idempotent, so it doubles as the repair path for
// datasets provisioned before this existed.
func (b *DifyBridge) SetDatasetRetrieval(ctx context.Context, datasetID, token string) error {
	return b.SetDatasetRetrievalWith(ctx, datasetID, token, RetrievalOverrides{})
}

// RetrievalOverrides are the parts of retrieval that are a per-dataset decision
// rather than a platform one.
//
// Only top_k is here, and deliberately: score_threshold fails by returning
// nothing at all, and reranking needs a model this workspace may not have — the
// two settings whose failure mode is silence are the two nobody can set.
type RetrievalOverrides struct {
	// TopK is how many segments an answer may draw on. Zero means "leave what
	// the dataset already has", which is what every caller but the editor wants.
	TopK int
}

// SetDatasetRetrievalWith aligns a dataset's retrieval settings with the
// platform's, applying the caller's overrides on top.
func (b *DifyBridge) SetDatasetRetrievalWith(ctx context.Context, datasetID, token string, ov RetrievalOverrides) error {
	if datasetID == "" {
		return fmt.Errorf("dataset ID is empty")
	}

	// Read the dataset before writing to it. A retrieval method has to match the
	// index the documents were actually built with: pointing a keyword-indexed
	// (economy) dataset at semantic search leaves it answering every query with
	// nothing, and the write itself succeeds — so the failure would surface only
	// as a knowledge base that has silently stopped being consulted.
	//
	// The deployment-wide indexing technique is not evidence of what this
	// dataset holds: it is what *new* uploads use, and a dataset built before it
	// was changed keeps its old index.
	current, err := b.GetDatasetConfig(ctx, datasetID, token)
	if err != nil {
		return fmt.Errorf("set dataset retrieval: %w", err)
	}
	// What the dataset reports has three states, and each gets its own answer:
	//
	//   undecided — Dify assigns the technique when the first document is
	//     indexed, so a dataset nobody has uploaded to reports nothing. There is
	//     no index to contradict, so the platform default is applied and the
	//     write proceeds. Refusing here would refuse every dataset at the one
	//     moment it has to be configured: the moment it is created.
	//   decided and different — refused below. This is the only refusal.
	//   decided and the same — written, which is the repair path.
	switch {
	case difyapp.IndexingUndecided(current.IndexingTechnique):
		log.Printf("[dify-bridge] dataset %s has no indexing technique yet (nothing indexed into it); "+
			"applying this deployment's %s retrieval settings", datasetID, b.config.IndexingTechnique)
	case current.IndexingTechnique != b.config.IndexingTechnique:
		return fmt.Errorf(
			"set dataset retrieval: dataset %s was indexed as %q but this deployment is configured for %q; "+
				"applying %q retrieval to a %q index makes every query return nothing. "+
				"Re-index the dataset (delete and re-upload its documents) before repairing retrieval",
			datasetID, current.IndexingTechnique, b.config.IndexingTechnique,
			b.config.IndexingTechnique, current.IndexingTechnique)
	}

	// Built as platform defaults merged with this dataset's overrides, even
	// though there are no overrides yet. The PATCH replaces the whole
	// retrieval_model object, so once a second writer exists — a per-tenant
	// top_k, say — a "repair" that rebuilt the object from defaults alone would
	// silently roll that writer back. Constructing it this way from the start
	// means the two cannot fight.
	want := difyapp.RetrievalModel(b.config.IndexingTechnique)
	for k, v := range current.Overrides {
		want[k] = v
	}
	if ov.TopK > 0 {
		want["top_k"] = ov.TopK
	}

	if _, err := b.doAdminRequestWithToken(ctx, http.MethodPatch, "/datasets/"+datasetID,
		map[string]interface{}{"retrieval_model": want}, token); err != nil {
		return fmt.Errorf("set dataset retrieval: %w", err)
	}

	// Read back rather than trust the status code: this endpoint answers a
	// write it ignored with the same 200 as one it applied, which is how the
	// settings came to be sent on create and silently dropped.
	verified, err := b.GetDatasetConfig(ctx, datasetID, token)
	if err != nil {
		return fmt.Errorf("set dataset retrieval: verify: %w", err)
	}
	// Every field that was written is checked, not just the method. Checking one
	// field is what let a partially ignored write read as a successful one.
	wantMethod, _ := want["search_method"].(string)
	if verified.SearchMethod != wantMethod {
		return fmt.Errorf("set dataset retrieval: dataset %s still reports search_method=%q, want %q",
			datasetID, verified.SearchMethod, wantMethod)
	}
	if wantTopK, ok := asInt(want["top_k"]); ok && verified.TopK != wantTopK {
		return fmt.Errorf("set dataset retrieval: dataset %s still reports top_k=%d, want %d",
			datasetID, verified.TopK, wantTopK)
	}
	if wantRerank, ok := want["reranking_enable"].(bool); ok && verified.RerankingEnable != wantRerank {
		return fmt.Errorf("set dataset retrieval: dataset %s still reports reranking_enable=%v, want %v",
			datasetID, verified.RerankingEnable, wantRerank)
	}
	log.Printf("[dify-bridge] dataset %s retrieval set to %s (top_k=%d, indexing=%s)",
		datasetID, verified.SearchMethod, verified.TopK, verified.IndexingTechnique)
	return nil
}

// DatasetConfig is what a dataset reports about how it is indexed and searched.
type DatasetConfig struct {
	IndexingTechnique string
	SearchMethod      string
	TopK              int
	RerankingEnable   bool
	// Overrides are the retrieval fields this dataset carries that the platform
	// defaults do not decide. Empty today; the field exists so the merge in
	// SetDatasetRetrieval is already in place when the first per-tenant
	// retrieval setting arrives.
	Overrides map[string]interface{}
}

// GetDatasetConfig reads a dataset's indexing technique and retrieval settings.
func (b *DifyBridge) GetDatasetConfig(ctx context.Context, datasetID, token string) (*DatasetConfig, error) {
	if datasetID == "" {
		return nil, fmt.Errorf("dataset ID is empty")
	}
	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/datasets/"+datasetID, nil, token)
	if err != nil {
		return nil, fmt.Errorf("read dataset %s: %w", datasetID, err)
	}
	var raw struct {
		IndexingTechnique string `json:"indexing_technique"`
		RetrievalModel    struct {
			SearchMethod    string `json:"search_method"`
			TopK            int    `json:"top_k"`
			RerankingEnable bool   `json:"reranking_enable"`
		} `json:"retrieval_model_dict"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal dataset %s: %w", datasetID, err)
	}
	// The dataset is the store for top_k — there is no second copy of it — so
	// what it currently holds is carried into Overrides and survives a repair.
	// Without this, "repair retrieval" would rebuild the object from platform
	// defaults and quietly undo whatever an administrator had set, and the two
	// buttons on that card would take turns cancelling each other.
	overrides := map[string]interface{}{}
	if raw.RetrievalModel.TopK > 0 {
		overrides["top_k"] = raw.RetrievalModel.TopK
	}
	return &DatasetConfig{
		IndexingTechnique: raw.IndexingTechnique,
		SearchMethod:      raw.RetrievalModel.SearchMethod,
		TopK:              raw.RetrievalModel.TopK,
		RerankingEnable:   raw.RetrievalModel.RerankingEnable,
		Overrides:         overrides,
	}, nil
}

// asInt accepts the numeric shapes a JSON round trip can produce.
func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// DeleteApp removes a Dify app. Used when a tenant is taken apart: the app is
// provisioned per tenant, so nothing else refers to it once the tenant is gone.
func (b *DifyBridge) DeleteApp(ctx context.Context, token, appID string) error {
	if appID == "" {
		return fmt.Errorf("app ID is empty")
	}
	if _, err := b.doAdminRequestWithToken(ctx, http.MethodDelete, "/apps/"+appID, nil, token); err != nil {
		return fmt.Errorf("delete app %s: %w", appID, err)
	}
	log.Printf("[dify-bridge] deleted app %s", appID)
	return nil
}

// DeleteDataset removes a Dify dataset and every document indexed in it. It is
// a separate object from the app that consults it, so removing one leaves the
// other standing.
func (b *DifyBridge) DeleteDataset(ctx context.Context, token, datasetID string) error {
	if datasetID == "" {
		return fmt.Errorf("dataset ID is empty")
	}
	if _, err := b.doAdminRequestWithToken(ctx, http.MethodDelete, "/datasets/"+datasetID, nil, token); err != nil {
		return fmt.Errorf("delete dataset %s: %w", datasetID, err)
	}
	log.Printf("[dify-bridge] deleted dataset %s", datasetID)
	return nil
}

// CreateAppAPIKey generates a new API key for the specified Dify app.
func (b *DifyBridge) CreateAppAPIKey(ctx context.Context, token, appID string) (*DifyAPIKeyCreated, error) {
	body, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/api-keys", map[string]interface{}{}, token)
	if err != nil {
		return nil, fmt.Errorf("create API key for app %s: %w", appID, err)
	}

	var resp DifyAPIKeyCreated
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal create API key response: %w", err)
	}
	log.Printf("[dify-bridge] created API key for app_id=%s (key_id=%s)", appID, resp.ID)
	return &resp, nil
}

// GetAppConfig retrieves the current configuration of a Dify app (including system prompt).
func (b *DifyBridge) GetAppConfig(ctx context.Context, appID string) (*AppInfo, error) {
	if appID == "" {
		return nil, fmt.Errorf("app ID is empty")
	}

	body, err := b.doAdminRequest(ctx, http.MethodGet, "/apps/"+appID, nil)
	if err != nil {
		return nil, fmt.Errorf("get app config: %w", err)
	}

	// Parse the full app response to extract system prompt
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal app config: %w", err)
	}

	info := &AppInfo{
		ID: appID,
	}
	if name, ok := raw["name"].(string); ok {
		info.Name = name
	}
	if mode, ok := raw["mode"].(string); ok {
		info.Mode = mode
	}
	// System prompt may be in model_config.pre_prompt or pre_prompt
	if prePrompt, ok := raw["pre_prompt"].(string); ok {
		info.SystemPrompt = prePrompt
	} else if mc, ok := raw["model_config"].(map[string]interface{}); ok {
		if pp, ok := mc["pre_prompt"].(string); ok {
			info.SystemPrompt = pp
		}
	}
	if mc, ok := raw["model_config"].(map[string]interface{}); ok {
		if model, ok := mc["model"].(map[string]interface{}); ok {
			info.Model = flattenModel(model)
		}
		missing := difyapp.MissingContextVariables(mc["user_input_form"])
		info.Variables = &AppVariablesInfo{
			Declared: difyapp.DeclaredVariables(mc["user_input_form"]),
			Missing:  missing,
			Complete: len(missing) == 0,
		}
	}

	return info, nil
}

// EnsureContextVariables declares any router input the app is missing, and
// reports which ones it had to add.
//
// Additive and idempotent: an operator's own variables are preserved, and an
// app that already declares everything is left untouched rather than rewritten
// — a wholesale rewrite of the form is how such additions get lost.
//
// Like every other write to this endpoint it reads the whole model_config and
// writes it back, because Dify has no partial update for it.
func (b *DifyBridge) EnsureContextVariables(ctx context.Context, appID, token string) ([]string, error) {
	if appID == "" {
		return nil, fmt.Errorf("app id is empty")
	}
	if token == "" {
		var err error
		if token, err = b.consoleToken(ctx); err != nil {
			return nil, err
		}
	}

	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return nil, fmt.Errorf("read app config: %w", err)
	}
	var envelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal app config: %w", err)
	}
	cfg := envelope.ModelConfig
	if cfg == nil {
		cfg = map[string]interface{}{}
	}

	missing := difyapp.MissingContextVariables(cfg["user_input_form"])
	if len(missing) == 0 {
		return nil, nil
	}
	cfg["user_input_form"] = difyapp.WithContextVariables(cfg["user_input_form"])

	if _, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/model-config", cfg, token); err != nil {
		return nil, fmt.Errorf("write model config: %w", err)
	}

	// Read back rather than trust the status code, for the reason the retrieval
	// write does: this endpoint answers an ignored write with the same 200.
	verify, err := b.GetAppConfig(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("verify declared variables: %w", err)
	}
	if verify.Variables == nil || !verify.Variables.Complete {
		return nil, fmt.Errorf("app %s still does not declare %v after the write",
			appID, verify.Variables.Missing)
	}
	log.Printf("[dify-bridge] app %s now declares %v", appID, missing)
	return missing, nil
}

// flattenModel renders Dify's nested model object for display, and records
// whether it is the platform's.
func flattenModel(model map[string]interface{}) *AppModelInfo {
	out := &AppModelInfo{Pinned: difyapp.PlatformModel().Matches(model)}
	out.Provider, _ = model["provider"].(string)
	out.Name, _ = model["name"].(string)
	if params, ok := model["completion_params"].(map[string]interface{}); ok {
		if t, ok := params["temperature"].(float64); ok {
			out.Temperature = t
		}
		if m, ok := params["max_tokens"].(float64); ok {
			out.MaxTokens = int(m)
		}
	}
	return out
}

// PinModel points an app at a given model, and confirms afterwards that the
// app actually answers with it.
//
// Nothing used to write model_config["model"] at all, so a provisioned app
// inherited whatever the Dify workspace default happened to be — which is how
// the fleet ended up split across two models, with different token ceilings,
// and no interface reporting it. Writing it explicitly is what keeps the fleet
// from drifting apart again one new tenant at a time; taking the spec as an
// argument rather than reading a constant is what lets the console change it
// without a release, and lets a per-line override deviate on purpose.
//
// Like the system prompt, this goes through POST /apps/{id}/model-config, which
// replaces the whole configuration object; the current one is read first and
// written back with only the model changed.
func (b *DifyBridge) PinModel(ctx context.Context, appID string, spec difyapp.ModelSpec) error {
	return b.pinModelWithToken(ctx, appID, spec, "")
}

// PinModelWithToken is PinModel for a caller that already holds a console
// session, and it exists because provisioning is one such caller: the app was
// created moments earlier through a token this bridge did not mint, and letting
// PinModel take its own would log in a second time on every new product line.
//
// The value it pins is the caller's, resolved from the store — a line's own
// override, the platform revision, or the built-in default. That resolution is
// the admin layer's, not this one's: this package has no database and must not
// grow one.
func (b *DifyBridge) PinModelWithToken(ctx context.Context, appID string, spec difyapp.ModelSpec, token string) error {
	return b.pinModelWithToken(ctx, appID, spec, token)
}

// PinPlatformModel points an app at the platform's built-in default. It is the
// provisioning path: a new app has no configuration of its own to inherit, and
// the token is already in hand from the console session that created it, so it
// is passed along rather than minted again.
//
// Deployments that have edited the model in the console are pinned from the
// stored value instead — resolving that is the admin layer's job, and it calls
// PinModel with what it resolved. This entry point stays for callers that want
// the built-in default by name.
func (b *DifyBridge) PinPlatformModel(ctx context.Context, appID, token string) error {
	return b.pinModelWithToken(ctx, appID, difyapp.PlatformModel(), token)
}

// pinModelWithToken carries the read-modify-write both entry points share, plus
// the console token when the caller already holds one.
// ErrModelWriteLanded marks the model writes that Dify accepted but whose
// effect could not be confirmed: the readback failed, or came back describing a
// different model than the one just written.
//
// It exists because the caller's next move differs entirely between the two
// ways a pin can fail. A rejected write leaves the app exactly as it was, so
// there is nothing to undo and nothing to record. A landed-but-unconfirmed
// write may already be answering customers on the new model, so it needs the
// same revert and the same audit entry a successful write would need — and the
// operator needs to be told the app was touched, rather than the flat "nothing
// was written" that a rejection warrants. Reporting both as one error is how a
// console ends up stating the opposite of what happened.
var ErrModelWriteLanded = errors.New("model config write was accepted but its effect could not be confirmed")

func (b *DifyBridge) pinModelWithToken(ctx context.Context, appID string, spec difyapp.ModelSpec, token string) error {
	if appID == "" {
		return fmt.Errorf("app id is empty")
	}
	// Validated here as well as at the HTTP edge, because this is the last
	// place before the value reaches Dify and Dify will not object: it stores a
	// token ceiling too small for this model to finish reasoning as readily as
	// a workable one, and the app then answers customers with empty replies.
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("pin model: %w", err)
	}
	if token == "" {
		var err error
		if token, err = b.consoleToken(ctx); err != nil {
			return err
		}
	}

	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("pin model: read app config: %w", err)
	}
	var envelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("pin model: unmarshal app config: %w", err)
	}
	cfg := envelope.ModelConfig
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	cfg["model"] = spec.AsModelConfig()

	if _, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/model-config", cfg, token); err != nil {
		// Dify's own complaint is passed through whole. A model name this
		// deployment has no provider for is rejected here, and the operator who
		// typed it is the one who needs to read the rejection.
		//
		// This is the one failure after which the app is known to be untouched:
		// the write never took. Every failure below this line is reported as
		// ErrModelWriteLanded instead, because by then the write has been
		// accepted and the app may already be answering on the new model.
		return fmt.Errorf("pin model: write model config: %w", err)
	}

	// Verified rather than trusted, for the reason the prompt write states about
	// this same endpoint: a model-config write that changed nothing comes back
	// with the same 200 as one that took effect. Without this readback the
	// console would record a model as in force while the app kept answering on
	// the previous one — and the whole point of storing the model is to be able
	// to say which one produced a given evaluation score.
	verify, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("pin model: verify: %w: %w", ErrModelWriteLanded, err)
	}
	// Into a fresh envelope, not the one the write was built from. Unmarshal
	// merges into an existing map rather than replacing it, so decoding the
	// readback over the config just written would leave that config's own model
	// standing wherever the response has none — and an app Dify left with no
	// model at all is precisely one of the outcomes this check exists to catch.
	var liveEnvelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(verify, &liveEnvelope); err != nil {
		return fmt.Errorf("pin model: unmarshal verified config: %w: %w", ErrModelWriteLanded, err)
	}
	live, _ := liveEnvelope.ModelConfig["model"].(map[string]interface{})
	// Matches compares the provider, the name and both completion parameters —
	// an app on the right model with the wrong token ceiling is the
	// configuration that returned empty answers, so agreeing on the name alone
	// is not agreement.
	if !spec.Matches(live) {
		return fmt.Errorf("pin model: app %s still answers with a different model after the write (wrote %s, in effect %s): %w",
			appID, describeSpec(spec), describeLiveModel(live), ErrModelWriteLanded)
	}

	log.Printf("[dify-bridge] pinned app_id=%s to %s/%s (temperature=%g max_tokens=%d)",
		appID, spec.Provider, spec.Name, spec.Temperature, spec.MaxTokens)
	return nil
}

// describeSpec renders a spec the way the readback failure has to report it:
// with the completion parameters, since those are half of what "a different
// model" can mean here.
func describeSpec(spec difyapp.ModelSpec) string {
	return fmt.Sprintf("%s/%s temperature=%g max_tokens=%d",
		spec.Provider, spec.Name, spec.Temperature, spec.MaxTokens)
}

// describeLiveModel renders what Dify reports back, including the case where it
// reports no model object at all — which is itself a failure worth naming
// rather than printing as an empty pair of slashes.
func describeLiveModel(model map[string]interface{}) string {
	if model == nil {
		return "no model configured"
	}
	info := flattenModel(model)
	return fmt.Sprintf("%s/%s temperature=%g max_tokens=%d",
		info.Provider, info.Name, info.Temperature, info.MaxTokens)
}

// UpdateSystemPrompt updates the system prompt of a Dify app.
func (b *DifyBridge) UpdateSystemPrompt(ctx context.Context, appID string, prompt string) error {
	return b.updateSystemPromptWithToken(ctx, appID, prompt, "")
}

// updateSystemPromptWithToken sets an app's system prompt and declares the
// variables the router injects, leaving the rest of the configuration alone.
//
// Dify has no partial update for this. Sending {"pre_prompt": ...} to
// PUT /apps/{id} — what this did before — addresses the app *rename* endpoint,
// which a real Dify 0.15.3 answers with 400 "Missing required parameter in the
// JSON body: name" and which would not have touched the prompt even with a name
// supplied. The prompt lives on POST /apps/{id}/model-config, which replaces the
// whole configuration object, so the current one is read first and written back
// with only the prompt and the input form changed.
func (b *DifyBridge) updateSystemPromptWithToken(ctx context.Context, appID, prompt, token string) error {
	if appID == "" {
		return fmt.Errorf("app ID is empty")
	}

	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("update system prompt: read current config: %w", err)
	}
	var envelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("update system prompt: unmarshal current config: %w", err)
	}
	cfg := envelope.ModelConfig
	if cfg == nil {
		cfg = map[string]interface{}{}
	}

	// In advanced prompt mode pre_prompt is ignored, so writing it would report
	// success and change nothing.
	if mode, _ := cfg["prompt_type"].(string); mode == "advanced" {
		return fmt.Errorf("update system prompt: app %s is in advanced prompt mode, where pre_prompt is ignored; edit it in the Dify console", appID)
	}

	cfg["pre_prompt"] = prompt
	cfg["prompt_type"] = "simple"
	cfg["user_input_form"] = difyapp.WithContextVariables(cfg["user_input_form"])

	if _, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/model-config", cfg, token); err != nil {
		return fmt.Errorf("update system prompt: %w", err)
	}

	// Verified rather than trusted, for the reason AttachDataset states about
	// this same endpoint: a model-config write that changed nothing comes back
	// with the same 200 as one that took effect. The prompt went unverified
	// while the dataset binding beside it did not, which left the one claim
	// this store exists to make — "this revision is in effect" — resting on an
	// answer that does not carry it.
	//
	// A byte comparison is safe because the round trip was measured, not
	// assumed: trailing spaces, tabs, blank lines, zero-width characters, ZWJ
	// emoji and a 400-character line all came back identical. Had Dify
	// normalised anything, every push would report a failure that did not
	// happen.
	verify, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("update system prompt: verify: %w", err)
	}
	if err := json.Unmarshal(verify, &envelope); err != nil {
		return fmt.Errorf("update system prompt: unmarshal verified config: %w", err)
	}
	if live, _ := envelope.ModelConfig["pre_prompt"].(string); live != prompt {
		return fmt.Errorf("update system prompt: app %s still answers with different text after the write (%d runes stored, %d in effect)",
			appID, utf8.RuneCountInString(prompt), utf8.RuneCountInString(live))
	}

	log.Printf("[dify-bridge] updated system prompt for app_id=%s (len=%d)", appID, len(prompt))
	return nil
}

// AppDatasetIDs lists the datasets an app currently retrieves from.
//
// Read-only, and deliberately separate from AttachDatasetWithToken: bringing a
// product line up to standard has to tell "already attached" from "attached by
// this run", and an attach that writes in order to find out cannot. Dify
// answers a model-config write that changed nothing with the same 200 as one
// that took effect, so asking by writing would report every re-run as a repair
// and would keep rewriting the configuration of lines that need nothing.
func (b *DifyBridge) AppDatasetIDs(ctx context.Context, appID, token string) ([]string, error) {
	if appID == "" {
		return nil, fmt.Errorf("app ID is empty")
	}
	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return nil, fmt.Errorf("read app %s: %w", appID, err)
	}
	var envelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal app %s config: %w", appID, err)
	}
	return difyapp.BoundDatasetIDs(envelope.ModelConfig["dataset_configs"]), nil
}

// AttachDataset binds a dataset to an app so the app actually retrieves from
// it. Creating the dataset and creating the app leave them unconnected.
func (b *DifyBridge) AttachDataset(ctx context.Context, appID, datasetID string) error {
	return b.AttachDatasetWithToken(ctx, appID, datasetID, "")
}

// AttachDatasetWithToken adds datasetID to an app's retrieval configuration,
// leaving the rest of the configuration alone.
//
// Same read-modify-write shape as updateSystemPromptWithToken, and for the same
// reason: POST /apps/{id}/model-config replaces the whole configuration object,
// so anything not read back first is silently dropped. The write is verified
// rather than trusted — Dify answers a model-config write that changed nothing
// with the same 200 as one that took effect, and a binding that quietly failed
// looks exactly like a knowledge base the customer filled with useless
// documents.
func (b *DifyBridge) AttachDatasetWithToken(ctx context.Context, appID, datasetID, token string) error {
	if appID == "" {
		return fmt.Errorf("app ID is empty")
	}
	if datasetID == "" {
		return fmt.Errorf("dataset ID is empty")
	}

	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("attach dataset: read current config: %w", err)
	}
	var envelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("attach dataset: unmarshal current config: %w", err)
	}
	cfg := envelope.ModelConfig
	if cfg == nil {
		cfg = map[string]interface{}{}
	}

	cfg["dataset_configs"] = difyapp.WithDataset(cfg["dataset_configs"], datasetID)

	if _, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/model-config", cfg, token); err != nil {
		return fmt.Errorf("attach dataset: %w", err)
	}

	verify, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("attach dataset: verify: %w", err)
	}
	if err := json.Unmarshal(verify, &envelope); err != nil {
		return fmt.Errorf("attach dataset: unmarshal verified config: %w", err)
	}
	if difyapp.DatasetBound(difyapp.BoundDatasetIDs(envelope.ModelConfig["dataset_configs"]), datasetID) {
		log.Printf("[dify-bridge] attached dataset_id=%s to app_id=%s", datasetID, appID)
		return nil
	}
	return fmt.Errorf("attach dataset: app %s still does not list dataset %s after write", appID, datasetID)
}

// contextVariables are the app variables the router passes in the chat-messages
// `inputs` map. The list lives in difyapp because the router provisions the same
// apps and must declare the same set.
var contextVariables = difyapp.ContextVariables

// SendTestMessage sends a test chat message to a Dify app and returns the response.
func (b *DifyBridge) SendTestMessage(ctx context.Context, apiKey string, message string, userID string) (*TestMessageResponse, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is empty")
	}

	reqBody := map[string]interface{}{
		"inputs":        map[string]interface{}{},
		"query":         message,
		"user":          userID,
		"response_mode": "blocking",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := b.config.APIBaseURL + "/chat-messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := b.chatClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send test message: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	log.Printf("[dify-bridge] test message -> %d (%s)", resp.StatusCode, elapsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from test message: %s", resp.StatusCode, string(body))
	}

	var result TestMessageResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal test response: %w", err)
	}

	// Derive confidence from retriever resources if available
	if retrieved := result.Retrieved(); len(retrieved) > 0 {
		maxScore := 0.0
		for _, rr := range retrieved {
			if rr.Score > maxScore {
				maxScore = rr.Score
			}
		}
		result.Confidence = maxScore
	}

	return &result, nil
}

// --- Internal helpers ---

// doAdminRequest performs an HTTP request to the Dify Console (admin) API using the
// bridge's static AdminToken.
func (b *DifyBridge) doAdminRequest(ctx context.Context, method, path string, reqBody interface{}) ([]byte, error) {
	return b.doAdminRequestWithToken(ctx, method, path, reqBody, "")
}

// consoleToken resolves a token for the Dify console API: a static AdminToken
// when configured, otherwise a cached token minted via Login with the
// configured admin credentials. Console tokens expire, which is why most
// deployments configure email/password rather than a token — the provisioning
// path always logged in per call for exactly that reason, while the AI-config
// path relied on the static token alone and broke wherever it was unset.
func (b *DifyBridge) consoleToken(ctx context.Context) (string, error) {
	if b.config.AdminToken != "" {
		return b.config.AdminToken, nil
	}
	if b.config.AdminEmail == "" || b.config.AdminPassword == "" {
		return "", fmt.Errorf("dify admin token is empty and no admin credentials configured")
	}

	b.tokenMu.Lock()
	defer b.tokenMu.Unlock()
	if b.cachedToken != "" && time.Since(b.tokenMintedAt) < consoleTokenTTL {
		return b.cachedToken, nil
	}
	token, err := b.Login(ctx, b.config.AdminEmail, b.config.AdminPassword)
	if err != nil {
		return "", fmt.Errorf("mint console token: %w", err)
	}
	b.cachedToken = token
	b.tokenMintedAt = time.Now()
	return token, nil
}

// invalidateConsoleToken drops the cached minted token after a 401 so the next
// attempt logs in afresh. Static AdminTokens are not touched: they are
// operator-supplied and re-minting is not an option.
func (b *DifyBridge) invalidateConsoleToken() {
	b.tokenMu.Lock()
	b.cachedToken = ""
	b.tokenMu.Unlock()
}

// doAdminRequestWithToken performs an HTTP request to the Dify Console (admin) API.
// When token is non-empty it is used for the Authorization header instead of the
// bridge's resolved console token (used for per-call tokens obtained via Login).
func (b *DifyBridge) doAdminRequestWithToken(ctx context.Context, method, path string, reqBody interface{}, token string) ([]byte, error) {
	if b.config.AdminURL == "" {
		return nil, fmt.Errorf("dify admin URL is empty")
	}
	authToken := token
	minted := false
	if authToken == "" {
		var err error
		authToken, err = b.consoleToken(ctx)
		if err != nil {
			return nil, err
		}
		minted = b.config.AdminToken == ""
	}

	body, status, err := b.adminRoundTrip(ctx, method, path, reqBody, authToken)
	// A cached minted token can expire early; log in once more and retry.
	if err != nil && status == http.StatusUnauthorized && minted {
		b.invalidateConsoleToken()
		if authToken, err = b.consoleToken(ctx); err != nil {
			return nil, err
		}
		body, _, err = b.adminRoundTrip(ctx, method, path, reqBody, authToken)
	}
	return body, err
}

// adminRoundTrip is one console-API HTTP exchange. It returns the status code
// alongside the error so the caller can distinguish an auth failure worth
// retrying from everything else.
func (b *DifyBridge) adminRoundTrip(ctx context.Context, method, path string, reqBody interface{}, authToken string) ([]byte, int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	url := b.config.AdminURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	start := time.Now()
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	log.Printf("[dify-bridge] %s %s -> %d (%s)", method, path, resp.StatusCode, elapsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d from %s %s: %s", resp.StatusCode, method, path, string(body))
	}

	return body, resp.StatusCode, nil
}
