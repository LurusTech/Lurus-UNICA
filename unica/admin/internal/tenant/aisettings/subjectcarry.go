package aisettings

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/pkg/domain"
)

// Subject carry is the D10 fix: a follow-up that names no object is sent to
// retrieval and the model with the object the customer last named. This file
// serves its switch and the operator's list of object names; the behaviour
// itself lives in the router and pkg/domain.

// subjectCarryResponse is the conversation block as the router reads it.
type subjectCarryResponse struct {
	ProductLineID string                  `json:"product_line_id"`
	SubjectCarry  domain.SubjectCarryMode `json:"subject_carry"`
	Subjects      []string                `json:"subjects"`
	// MaxSubjects and MaxSubjectRunes are the limits a write is held to,
	// reported so the console states them rather than restating a copy.
	MaxSubjects     int `json:"max_subjects"`
	MaxSubjectRunes int `json:"max_subject_runes"`
	// CacheInvalidated is set on writes only; see guardrailResponse.
	CacheInvalidated *bool `json:"cache_invalidated,omitempty"`
}

// updateSubjectCarryRequest changes either half; an omitted field is kept.
type updateSubjectCarryRequest struct {
	SubjectCarry *string   `json:"subject_carry"`
	Subjects     *[]string `json:"subjects"`
}

func subjectCarryOf(productLineID string, cfg domain.ConversationConfig) subjectCarryResponse {
	subjects := cfg.Subjects
	if subjects == nil {
		subjects = []string{}
	}
	return subjectCarryResponse{
		ProductLineID:   productLineID,
		SubjectCarry:    cfg.SubjectCarry,
		Subjects:        subjects,
		MaxSubjects:     domain.MaxSubjects,
		MaxSubjectRunes: domain.MaxSubjectRunes,
	}
}

// getSubjectCarry answers with the block exactly as the router will read it,
// so an unknown stored mode shows as off — which is what it does.
func (h *Handler) getSubjectCarry(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}
	writeJSON(w, http.StatusOK, subjectCarryOf(pl.ID, domain.LoadConversationConfig(raw)))
}

// updateSubjectCarry writes the switch and the subject list.
//
// The list is names only, written by a person: it is the one input to the
// rewrite a person controls, and a description pasted into it would put its
// figures in front of every carried follow-up. ValidateSubjects refuses
// anything that reads as a sentence rather than a name.
//
// Other keys of the conversation block are kept, so a later setting stored
// beside these two is not erased by a write that does not know about it. The
// router's cached route is dropped afterwards, like a guardrail write.
func (h *Handler) updateSubjectCarry(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req updateSubjectCarryRequest
	if err := decodeJSON(r, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SubjectCarry == nil && req.Subjects == nil {
		errorJSON(w, http.StatusBadRequest, "subject_carry or subjects is required")
		return
	}

	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[ai-settings] load config_json error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to load AI settings")
		return
	}
	cfg := domain.LoadConversationConfig(raw)

	if req.SubjectCarry != nil {
		mode, err := domain.ParseSubjectCarryMode(*req.SubjectCarry)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.SubjectCarry = mode
	}
	if req.Subjects != nil {
		subjects := domain.NormaliseSubjects(*req.Subjects)
		if err := domain.ValidateSubjects(subjects); err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.Subjects = subjects
	}

	block := conversationBlock(raw)
	modeJSON, _ := json.Marshal(cfg.SubjectCarry)
	subjectsJSON, _ := json.Marshal(subjectCarryOf(pl.ID, cfg).Subjects)
	block["subject_carry"] = modeJSON
	block["subjects"] = subjectsJSON

	if err := h.pls.SetConfigKey(r.Context(), pl.ID, domain.ConversationConfigKey, block); err != nil {
		log.Printf("[ai-settings] store conversation error: %v", err)
		errorJSON(w, http.StatusInternalServerError, "failed to store AI settings")
		return
	}
	invalidated := h.routeCache.Invalidate(r.Context(), pl.ID)

	resp := subjectCarryOf(pl.ID, cfg)
	resp.CacheInvalidated = &invalidated
	writeJSON(w, http.StatusOK, resp)
}

// conversationBlock returns the stored conversation block as raw fields, or an
// empty one when there is none or it cannot be read.
func conversationBlock(configJSON json.RawMessage) map[string]json.RawMessage {
	block := map[string]json.RawMessage{}
	if len(configJSON) == 0 {
		return block
	}
	var wrapper struct {
		Conversation map[string]json.RawMessage `json:"conversation"`
	}
	if err := json.Unmarshal(configJSON, &wrapper); err != nil || wrapper.Conversation == nil {
		return block
	}
	return wrapper.Conversation
}
