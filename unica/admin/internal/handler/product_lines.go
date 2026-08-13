package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/bridge"
	"github.com/kefu/unica/admin/internal/repository"
)

// productLineStore abstracts the product line repository methods needed by this handler,
// allowing tests to substitute a fake implementation without a real database.
type productLineStore interface {
	Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error)
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	List(ctx context.Context, ids []string) ([]repository.ProductLine, error)
	Update(ctx context.Context, id, name, displayName string, chatwootAccountID *int) (*repository.ProductLine, error)
	UpdateDifyBinding(ctx context.Context, id, agentID, apiKey, baseURL string, extraConfig map[string]string) (*repository.ProductLine, error)
	Delete(ctx context.Context, id string) (deleted bool, blocked bool, err error)
}

// ProductLineHandler handles product line CRUD endpoints, including one-click Dify provisioning.
type ProductLineHandler struct {
	plRepo            productLineStore
	difyBridge        *bridge.DifyBridge
	difyAdminEmail    string
	difyAdminPassword string
}

// NewProductLineHandler creates a new product line handler.
func NewProductLineHandler(plRepo *repository.ProductLineRepository, difyBridge *bridge.DifyBridge, difyAdminEmail, difyAdminPassword string) *ProductLineHandler {
	return &ProductLineHandler{
		plRepo:            plRepo,
		difyBridge:        difyBridge,
		difyAdminEmail:    difyAdminEmail,
		difyAdminPassword: difyAdminPassword,
	}
}

type createProductLineRequest struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	ChatwootAccountID *int   `json:"chatwoot_account_id,omitempty"`
}

type updateProductLineRequest struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	ChatwootAccountID *int   `json:"chatwoot_account_id,omitempty"`
}

// HandleProductLines handles GET (list) and POST (create) on /api/v1/product-lines.
func (h *ProductLineHandler) HandleProductLines(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listProductLines(w, r)
	case http.MethodPost:
		h.createProductLine(w, r)
	default:
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// HandleProductLine handles GET/PUT on /api/v1/product-lines/:id and
// POST on /api/v1/product-lines/:id/provision-dify.
func (h *ProductLineHandler) HandleProductLine(w http.ResponseWriter, r *http.Request) {
	segments := ExtractPathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) == 0 {
		ErrorJSON(w, http.StatusBadRequest, "product line id required")
		return
	}
	id := segments[0]

	if len(segments) >= 2 {
		switch segments[1] {
		case "provision-dify":
			if r.Method != http.MethodPost {
				ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h.provisionDify(w, r, id)
		default:
			ErrorJSON(w, http.StatusNotFound, "unknown product line sub-path: "+segments[1])
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getProductLine(w, r, id)
	case http.MethodPut:
		h.updateProductLine(w, r, id)
	case http.MethodDelete:
		h.deleteProductLine(w, r, id)
	default:
		ErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *ProductLineHandler) deleteProductLine(w http.ResponseWriter, r *http.Request, id string) {
	deleted, blocked, err := h.plRepo.Delete(r.Context(), id)
	if err != nil {
		log.Printf("[product-lines] delete error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to delete product line")
		return
	}
	if blocked {
		ErrorJSON(w, http.StatusConflict, "该产品线下仍有渠道配置，请先删除其渠道")
		return
	}
	if !deleted {
		ErrorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	JSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *ProductLineHandler) listProductLines(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r.Context())

	// Filter by product line scope for non-SuperAdmin
	var ids []string
	if claims != nil && claims.Role != "super_admin" {
		ids = claims.ProductLineIDs
	}

	pls, err := h.plRepo.List(r.Context(), ids)
	if err != nil {
		log.Printf("[product-lines] list error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to list product lines")
		return
	}

	JSON(w, http.StatusOK, pls)
}

func (h *ProductLineHandler) createProductLine(w http.ResponseWriter, r *http.Request) {
	var req createProductLineRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.DisplayName == "" {
		ErrorJSON(w, http.StatusBadRequest, "name and display_name required")
		return
	}

	pl, err := h.plRepo.Create(r.Context(), req.Name, req.DisplayName, req.ChatwootAccountID)
	if err != nil {
		log.Printf("[product-lines] create error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to create product line")
		return
	}

	JSON(w, http.StatusCreated, pl)
}

func (h *ProductLineHandler) getProductLine(w http.ResponseWriter, r *http.Request, id string) {
	pl, err := h.plRepo.GetByID(r.Context(), id)
	if err != nil {
		log.Printf("[product-lines] get error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	if pl == nil {
		ErrorJSON(w, http.StatusNotFound, "product line not found")
		return
	}
	JSON(w, http.StatusOK, pl)
}

func (h *ProductLineHandler) updateProductLine(w http.ResponseWriter, r *http.Request, id string) {
	var req updateProductLineRequest
	if err := DecodeJSON(r, &req); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.DisplayName == "" {
		ErrorJSON(w, http.StatusBadRequest, "name and display_name required")
		return
	}

	pl, err := h.plRepo.Update(r.Context(), id, req.Name, req.DisplayName, req.ChatwootAccountID)
	if err != nil {
		log.Printf("[product-lines] update error: %v", err)
		ErrorJSON(w, http.StatusInternalServerError, "failed to update product line")
		return
	}
	if pl == nil {
		ErrorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	JSON(w, http.StatusOK, pl)
}

// difyAdminUnconfiguredMessage is what both the provisioning route and customer
// onboarding say when the console credentials are missing; the two surfaces must
// not drift into naming different environment variables.
const difyAdminUnconfiguredMessage = "未配置 Dify 管理员账号，请联系系统管理员设置 DIFY_ADMIN_EMAIL 和 DIFY_ADMIN_PASSWORD 环境变量"

// provisionDifyResponse is the response body for POST /api/v1/product-lines/:id/provision-dify.
//
// Warnings carries the steps that failed without aborting provisioning. They
// have to reach the caller rather than the log alone: the dataset binding used
// to be missing entirely, and because nothing reported its absence, every
// product line ran for months with a knowledge base its app never read.
type provisionDifyResponse struct {
	Provisioned   bool     `json:"provisioned"`
	DifyAgentID   string   `json:"dify_agent_id"`
	DifyDatasetID string   `json:"dify_dataset_id"`
	Warnings      []string `json:"warnings,omitempty"`
}

// difyProvisionError is a provisioning failure that already knows how the HTTP
// surface must report it, so the route and the onboarding orchestrator answer
// with the same status and wording without either restating the mapping.
type difyProvisionError struct {
	status  int
	message string
}

func (e *difyProvisionError) Error() string { return e.message }

// Status reports the HTTP status this failure maps to.
func (e *difyProvisionError) Status() int { return e.status }

// provisionDify one-click provisions a Dify chat app + dataset for a product line inside
// the default Dify workspace, then persists the binding. It is idempotent: if the product
// line already has a dify_agent_id, the current binding is returned with provisioned=false.
func (h *ProductLineHandler) provisionDify(w http.ResponseWriter, r *http.Request, id string) {
	resp, perr := h.provisionDifyLine(r.Context(), id)
	if perr != nil {
		ErrorJSON(w, perr.status, perr.message)
		return
	}
	JSON(w, http.StatusOK, resp)
}

// provisionDifyLine holds the provisioning sequence itself, without an
// http.ResponseWriter, so customer onboarding can run the same steps and keep
// going when they fail.
func (h *ProductLineHandler) provisionDifyLine(ctx context.Context, id string) (*provisionDifyResponse, *difyProvisionError) {
	pl, err := h.plRepo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[product-lines] provision-dify get error: %v", err)
		return nil, &difyProvisionError{http.StatusInternalServerError, "internal error"}
	}
	if pl == nil {
		return nil, &difyProvisionError{http.StatusNotFound, "product line not found"}
	}

	if pl.DifyAgentID != nil && *pl.DifyAgentID != "" {
		datasetID := ""
		if pl.DifyDatasetID != nil {
			datasetID = *pl.DifyDatasetID
		}
		return &provisionDifyResponse{
			Provisioned:   false,
			DifyAgentID:   *pl.DifyAgentID,
			DifyDatasetID: datasetID,
		}, nil
	}

	if h.difyAdminEmail == "" || h.difyAdminPassword == "" {
		return nil, &difyProvisionError{http.StatusServiceUnavailable, difyAdminUnconfiguredMessage}
	}

	token, err := h.difyBridge.Login(ctx, h.difyAdminEmail, h.difyAdminPassword)
	if err != nil {
		log.Printf("[product-lines] dify login error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "登录 Dify 失败: " + err.Error()}
	}

	provisionName := fmt.Sprintf("UNICA-%s", pl.Name)

	app, err := h.difyBridge.CreateChatApp(ctx, token, provisionName)
	if err != nil {
		log.Printf("[product-lines] dify create app error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "创建 Dify 应用失败: " + err.Error()}
	}

	dataset, err := h.difyBridge.CreateDataset(ctx, token, provisionName)
	if err != nil {
		log.Printf("[product-lines] dify create dataset error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "创建 Dify 知识库失败: " + err.Error()}
	}

	apiKey, err := h.difyBridge.CreateAppAPIKey(ctx, token, app.ID)
	if err != nil {
		log.Printf("[product-lines] dify create api key error: %v", err)
		return nil, &difyProvisionError{http.StatusBadGateway, "创建 Dify API Key 失败: " + err.Error()}
	}

	// Without this the app and its dataset stay unconnected: uploads succeed,
	// indexing completes, and not one answer ever draws on them.
	//
	// Non-fatal for the same reason the default prompt is: this writes through
	// POST /apps/{id}/model-config, which a workspace with no model provider yet
	// rejects, and that must not block onboarding. Unlike before, it is reported
	// — silence here is exactly how the missing binding survived undetected.
	var warnings []string
	if err := h.difyBridge.AttachDatasetWithToken(ctx, app.ID, dataset.ID, token); err != nil {
		log.Printf("[product-lines] WARN: dataset %s not bound to app %s; the knowledge base will not be consulted until it is: %v", dataset.ID, app.ID, err)
		warnings = append(warnings, "知识库未能绑定到 Dify 应用，上传的文档暂不会参与回答，请稍后重试绑定: "+err.Error())
	}

	updated, err := h.plRepo.UpdateDifyBinding(ctx, pl.ID, app.ID, apiKey.Token, h.difyBridge.APIBaseURL(), map[string]string{
		"dify_dataset_id": dataset.ID,
	})
	if err != nil {
		log.Printf("[product-lines] update dify binding error: %v", err)
		return nil, &difyProvisionError{http.StatusInternalServerError, "保存 Dify 绑定信息失败"}
	}
	if updated == nil {
		return nil, &difyProvisionError{http.StatusNotFound, "product line not found"}
	}

	return &provisionDifyResponse{
		Provisioned:   true,
		DifyAgentID:   app.ID,
		DifyDatasetID: dataset.ID,
		Warnings:      warnings,
	}, nil
}
