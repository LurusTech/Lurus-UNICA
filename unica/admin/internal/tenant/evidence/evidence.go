// Package evidence serves one tenant's judgement evidence ledger: the rows the
// router wrote for a conversation together with a verification of the chain
// they sit in, a verification of any stretch of the chain, the head digest a
// customer downloads and keeps as an outside anchor, and the per-tenant switch.
//
// The ledger makes later changes to a recorded decision discoverable; it does
// not prevent them, and nothing here says otherwise. Hashing and verification
// are pkg/compliance's, shared with the router that writes the rows; this
// module imports nothing of the router.
//
// Everything acts on the tenant in the route, which was resolved and authorised
// before any handler here runs.
package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kefu/unica/admin/internal/audit"
	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/repository"
	"github.com/kefu/unica/admin/internal/routecache"
	"github.com/kefu/unica/pkg/compliance"
)

// MaxVerifyRows bounds one verification. A line's chain grows with its
// traffic, and one request must not read an unbounded number of rows; longer
// stretches are verified piece by piece, each piece anchored on the last.
const MaxVerifyRows = 10000

// defaultVerifyRows is the stretch verified when the caller names no limit.
const defaultVerifyRows = 1000

// maxConversationRows bounds the rows returned for one conversation.
const maxConversationRows = 2000

// ledgerStore is the read side of the ledger, narrowed so tests need no
// database.
type ledgerStore interface {
	ByConversation(ctx context.Context, productLineID, conversationID string, limit int) ([]compliance.AuditEntry, error)
	Range(ctx context.Context, productLineID string, fromSeq int64, limit int) ([]compliance.AuditEntry, error)
	FirstSeq(ctx context.Context, productLineID string) (int64, bool, error)
	Head(ctx context.Context, productLineID string) (Head, bool, error)
}

// productLines is the product-line access the handler needs.
type productLines interface {
	GetByID(ctx context.Context, id string) (*repository.ProductLine, error)
	GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error)
	SetConfigKey(ctx context.Context, id, key string, value interface{}) error
}

// Handler serves /api/v1/product-lines/{id}/evidence[...].
type Handler struct {
	store  ledgerStore
	pls    productLines
	logger *audit.Logger
	// routeCache drops the router's cached copy of the tenant's config_json,
	// where the switch lives; without it a change takes a cache lifetime to
	// reach conversations.
	routeCache *routecache.Invalidator
	now        func() time.Time
}

// NewHandler builds the handler.
func NewHandler(store ledgerStore, pls productLines, logger *audit.Logger, routeCache *routecache.Invalidator) *Handler {
	return &Handler{store: store, pls: pls, logger: logger, routeCache: routeCache, now: time.Now}
}

// Handle dispatches /api/v1/product-lines/{id}/evidence and its sub-paths. The
// tenant dispatcher rewrites the tenant-facing path into this shape.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	segments := pathSegments(r.URL.Path, "/api/v1/product-lines/")
	if len(segments) < 2 || segments[1] != "evidence" || len(segments) > 3 {
		errorJSON(w, http.StatusNotFound, "not found")
		return
	}
	plID := segments[0]

	// Scope first, before the product line is even looked up, so a tenant
	// probing another tenant's id learns nothing about whether it exists.
	if !auth.TenantScopeAllowed(r, plID) {
		errorJSON(w, http.StatusForbidden, "access denied for this product line")
		return
	}
	pl, err := h.pls.GetByID(r.Context(), plID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line")
		return
	}
	if pl == nil {
		errorJSON(w, http.StatusNotFound, "product line not found")
		return
	}

	sub := ""
	if len(segments) == 3 {
		sub = segments[2]
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		h.conversation(w, r, pl)
	case sub == "verify" && r.Method == http.MethodGet:
		h.verify(w, r, pl)
	case sub == "head" && r.Method == http.MethodGet:
		h.head(w, r, pl)
	case sub == "config" && r.Method == http.MethodGet:
		h.getConfig(w, r, pl)
	case sub == "config" && r.Method == http.MethodPut:
		h.putConfig(w, r, pl)
	case sub == "" || sub == "verify" || sub == "head" || sub == "config":
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		errorJSON(w, http.StatusNotFound, "not found")
	}
}

// entryView is one ledger row as the console reads it. The JSON columns are
// passed through as stored.
type entryView struct {
	Seq             int64           `json:"seq"`
	ConversationID  string          `json:"conversation_id"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	Kind            string          `json:"kind"`
	Decision        string          `json:"decision"`
	Reason          string          `json:"reason"`
	ReasonClass     string          `json:"reason_class"`
	ConfidenceScore *float64        `json:"confidence_score"`
	OntologyVersion *int            `json:"ontology_version"`
	FactsInjected   bool            `json:"facts_injected"`
	ValidationMode  string          `json:"validation_mode"`
	BreakerOpen     bool            `json:"breaker_open"`
	Enforced        bool            `json:"enforced"`
	Claims          json.RawMessage `json:"claims"`
	Violations      json.RawMessage `json:"violations"`
	Commitments     json.RawMessage `json:"commitments"`
	Sources         json.RawMessage `json:"sources"`
	ConfigSHA256    string          `json:"config_sha256"`
	CustomerMessage string          `json:"customer_message"`
	DeliveredAnswer *string         `json:"delivered_answer"`
	WithheldAnswer  *string         `json:"withheld_answer"`
	PrevHash        string          `json:"prev_hash"`
	RowHash         string          `json:"row_hash"`
	CreatedAt       string          `json:"created_at"`
	// HashOK reports that this row's own hash recomputes. The chain result
	// says whether it also sits where it should.
	HashOK bool `json:"hash_ok"`
}

func viewOf(e compliance.AuditEntry) entryView {
	return entryView{
		Seq:             e.Seq,
		ConversationID:  e.ConversationID,
		CorrelationID:   e.CorrelationID,
		Kind:            e.Kind,
		Decision:        e.Decision,
		Reason:          e.Reason,
		ReasonClass:     e.ReasonClass,
		ConfidenceScore: e.ConfidenceScore,
		OntologyVersion: e.OntologyVersion,
		FactsInjected:   e.FactsInjected,
		ValidationMode:  e.ValidationMode,
		BreakerOpen:     e.BreakerOpen,
		Enforced:        e.Enforced,
		Claims:          jsonOrEmpty(e.Claims),
		Violations:      jsonOrEmpty(e.Violations),
		Commitments:     jsonOrEmpty(e.Commitments),
		Sources:         jsonOrEmpty(e.Sources),
		ConfigSHA256:    e.ConfigSHA256,
		CustomerMessage: e.CustomerMessage,
		DeliveredAnswer: e.DeliveredAnswer,
		WithheldAnswer:  e.WithheldAnswer,
		PrevHash:        e.PrevHash,
		RowHash:         e.RowHash,
		CreatedAt:       e.CreatedAt.UTC().Format(time.RFC3339Nano),
		HashOK:          compliance.ChainHash(e.PrevHash, e) == e.RowHash,
	}
}

type conversationResponse struct {
	ProductLineID  string                  `json:"product_line_id"`
	ConversationID string                  `json:"conversation_id"`
	Entries        []entryView             `json:"entries"`
	Chain          compliance.VerifyResult `json:"chain"`
}

// conversation returns every ledger row of one conversation, with the
// verification of the stretch of the line's chain that contains them. The
// rows of one conversation are interleaved with other conversations' rows,
// so it is the whole stretch from its first row to its last that is checked;
// checking only the conversation's own rows could not show that none was
// removed from between them.
func (h *Handler) conversation(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	convID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("conversation_id")))
	if !isUUID(convID) {
		errorJSON(w, http.StatusBadRequest, "请填写会话 ID（UUID 格式）")
		return
	}
	entries, err := h.store.ByConversation(r.Context(), pl.ID, convID, maxConversationRows)
	if err != nil {
		log.Printf("[evidence] read conversation %s of %s: %v", convID, pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "failed to read evidence")
		return
	}
	if len(entries) == 0 {
		errorJSON(w, http.StatusNotFound, "没有找到这个会话的判定证据：会话不属于本租户，或者记录时证据链尚未开启")
		return
	}

	from := entries[0].Seq
	span := entries[len(entries)-1].Seq - from + 1
	limit := span
	if limit > MaxVerifyRows {
		limit = MaxVerifyRows
	}
	chain, err := h.verifyStretch(r.Context(), pl.ID, from, int(limit))
	if err != nil {
		log.Printf("[evidence] verify stretch of %s from %d: %v", pl.ID, from, err)
		errorJSON(w, http.StatusInternalServerError, "failed to verify evidence")
		return
	}
	if chain.OK && span > MaxVerifyRows {
		chain.Note = strings.TrimSpace(fmt.Sprintf(
			"本会话的记录跨度超过 %d 条，这里只校验到第 %d 条；之后的部分请用整段验链从第 %d 条继续。%s",
			MaxVerifyRows, chain.ToSeq, chain.ToSeq+1, chain.Note))
	}

	views := make([]entryView, 0, len(entries))
	for _, e := range entries {
		views = append(views, viewOf(e))
	}
	writeJSON(w, http.StatusOK, conversationResponse{
		ProductLineID:  pl.ID,
		ConversationID: convID,
		Entries:        views,
		Chain:          chain,
	})
}

// verifyStretch reads and verifies up to limit rows from fromSeq, and words
// the anchor correctly: a stretch with nothing stored before it was pruned by
// retention, one with earlier rows still stored is only a segment.
func (h *Handler) verifyStretch(ctx context.Context, plID string, fromSeq int64, limit int) (compliance.VerifyResult, error) {
	rows, err := h.store.Range(ctx, plID, fromSeq, limit)
	if err != nil {
		return compliance.VerifyResult{}, err
	}
	res := compliance.Verify(rows)
	if len(rows) > 0 && rows[0].Seq != fromSeq && fromSeq > 0 {
		// The first row asked for is missing, which Verify cannot see from
		// the rows alone.
		first, found, err := h.store.FirstSeq(ctx, plID)
		if err != nil {
			return compliance.VerifyResult{}, err
		}
		if found && first < fromSeq {
			res.OK = false
			res.FirstBadSeq = fromSeq
			res.Problem = fmt.Sprintf("第 %d 条记录缺失（序号断档）", fromSeq)
			res.Note = ""
			return res, nil
		}
	}
	if res.OK && res.StartsMidChain {
		first, found, err := h.store.FirstSeq(ctx, plID)
		if err != nil {
			return compliance.VerifyResult{}, err
		}
		if found && first < res.FromSeq {
			res.Note = compliance.NoteSegment
		}
	}
	return res, nil
}

type verifyResponse struct {
	ProductLineID string                  `json:"product_line_id"`
	Chain         compliance.VerifyResult `json:"chain"`
	// DigestChecked and DigestProblem report the comparison with a head
	// digest the caller saved earlier, when one was supplied.
	DigestChecked bool   `json:"digest_checked"`
	DigestMatch   bool   `json:"digest_match"`
	DigestProblem string `json:"digest_problem,omitempty"`
}

// verify checks a stretch of the line's chain: from_seq (default: the first
// stored row) and limit (default 1000, at most 10000). With digest — a line
// previously downloaded from the head endpoint — it instead checks the
// stretch ending at the digest's seq and compares that row with the digest.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	q := r.URL.Query()
	limit := defaultVerifyRows
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxVerifyRows {
			errorJSON(w, http.StatusBadRequest, fmt.Sprintf("limit 必须是 1 到 %d 之间的整数", MaxVerifyRows))
			return
		}
		limit = n
	}

	first, found, err := h.store.FirstSeq(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[evidence] first seq of %s: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "failed to verify evidence")
		return
	}

	resp := verifyResponse{ProductLineID: pl.ID}
	var ref compliance.HeadRef
	if digest := q.Get("digest"); digest != "" {
		ref, err = compliance.ParseHeadDigest(digest)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		if ref.ProductLineID != pl.ID {
			errorJSON(w, http.StatusBadRequest, "这份链头摘要属于另一个租户")
			return
		}
		resp.DigestChecked = true
	}

	var from int64
	switch {
	case resp.DigestChecked:
		from = ref.Seq - int64(limit) + 1
		if found && from < first {
			from = first
		}
		if from < 1 {
			from = 1
		}
		if ref.Seq >= from {
			limit = int(ref.Seq - from + 1)
		}
	case q.Get("from_seq") != "":
		n, err := strconv.ParseInt(q.Get("from_seq"), 10, 64)
		if err != nil || n < 1 {
			errorJSON(w, http.StatusBadRequest, "from_seq 必须是正整数")
			return
		}
		from = n
	default:
		if !found {
			resp.Chain = compliance.VerifyResult{OK: true, Note: "尚无记录"}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		from = first
	}

	chain, err := h.verifyStretch(r.Context(), pl.ID, from, limit)
	if err != nil {
		log.Printf("[evidence] verify stretch of %s from %d: %v", pl.ID, from, err)
		errorJSON(w, http.StatusInternalServerError, "failed to verify evidence")
		return
	}
	resp.Chain = chain

	if resp.DigestChecked {
		switch {
		case !chain.OK:
			resp.DigestProblem = "这一段链本身没有通过校验，见校验结果"
		case ref.Seq == 0:
			if ref.RowHash != compliance.GenesisHash(pl.ID) {
				resp.DigestProblem = "链头摘要记录的是空链，但哈希与本租户的链首不符"
			} else {
				resp.DigestMatch = true
			}
		default:
			rows, err := h.store.Range(r.Context(), pl.ID, ref.Seq, 1)
			if err != nil {
				log.Printf("[evidence] read seq %d of %s: %v", ref.Seq, pl.ID, err)
				errorJSON(w, http.StatusInternalServerError, "failed to verify evidence")
				return
			}
			if len(rows) == 0 || rows[0].Seq != ref.Seq || chain.ToSeq < ref.Seq {
				resp.DigestProblem = fmt.Sprintf("库中没有第 %d 条记录（可能已按保留期清理，或这份摘要不是本系统为本租户生成的），无法比对", ref.Seq)
			} else if err := compliance.CompareHead(ref, rows); err != nil {
				resp.DigestProblem = err.Error()
			} else {
				resp.DigestMatch = true
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type headResponse struct {
	ProductLineID string `json:"product_line_id"`
	Seq           int64  `json:"seq"`
	RowHash       string `json:"row_hash"`
	// Digest is the one line to save outside this system.
	Digest      string `json:"digest"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	GeneratedAt string `json:"generated_at"`
	// Consistent reports that the head agrees with the last stored row.
	Consistent bool   `json:"consistent"`
	Note       string `json:"note,omitempty"`
}

// head returns the chain head and its digest line. A customer who downloads
// this every day holds an anchor outside the system: a chain rewritten after
// the download can still verify on its own, but no longer matches the line.
func (h *Handler) head(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	head, found, err := h.store.Head(r.Context(), pl.ID)
	if err != nil {
		log.Printf("[evidence] head of %s: %v", pl.ID, err)
		errorJSON(w, http.StatusInternalServerError, "failed to read chain head")
		return
	}
	resp := headResponse{ProductLineID: pl.ID, GeneratedAt: h.now().UTC().Format(time.RFC3339)}
	if !found {
		resp.RowHash = compliance.GenesisHash(pl.ID)
		resp.Digest = compliance.HeadDigest(pl.ID, 0, resp.RowHash)
		resp.Consistent = true
		resp.Note = "尚无记录：证据链开启后的第一条判定会接在这个起点之后"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Seq = head.Seq
	resp.RowHash = head.RowHash
	resp.Digest = compliance.HeadDigest(pl.ID, head.Seq, head.RowHash)
	resp.UpdatedAt = head.UpdatedAt.UTC().Format(time.RFC3339)
	resp.Consistent = true
	if head.Seq > 0 {
		rows, err := h.store.Range(r.Context(), pl.ID, head.Seq, 1)
		if err != nil {
			log.Printf("[evidence] read head row of %s: %v", pl.ID, err)
			errorJSON(w, http.StatusInternalServerError, "failed to read chain head")
			return
		}
		if len(rows) == 0 || rows[0].Seq != head.Seq || rows[0].RowHash != head.RowHash {
			resp.Consistent = false
			resp.Note = "链头与库中最后一条记录不一致，请先做整段验链"
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type configResponse struct {
	ProductLineID    string `json:"product_line_id"`
	Enabled          bool   `json:"enabled"`
	CacheInvalidated *bool  `json:"cache_invalidated,omitempty"`
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	raw, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	writeJSON(w, http.StatusOK, configResponse{
		ProductLineID: pl.ID,
		Enabled:       compliance.LoadAuditConfig(raw).Enabled,
	})
}

// putConfig turns the ledger on or off for the tenant. The write is audited
// like every other switch, and the router's cached route is dropped so the
// change reaches the next message rather than the next cache expiry.
func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request, pl *repository.ProductLine) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		errorJSON(w, http.StatusBadRequest, "enabled（true 或 false）为必填")
		return
	}
	before, err := h.pls.GetConfigJSON(r.Context(), pl.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to load product line config")
		return
	}
	after := compliance.AuditConfig{Enabled: *req.Enabled}
	if err := h.pls.SetConfigKey(r.Context(), pl.ID, compliance.AuditConfigKey, after); err != nil {
		errorJSON(w, http.StatusInternalServerError, "failed to store evidence ledger config")
		return
	}
	h.logMutation(r, pl.ID, compliance.LoadAuditConfig(before), after)

	invalidated := h.routeCache.Invalidate(r.Context(), pl.ID)
	writeJSON(w, http.StatusOK, configResponse{
		ProductLineID:    pl.ID,
		Enabled:          after.Enabled,
		CacheInvalidated: &invalidated,
	})
}

// logMutation writes the audit row for a switch change. The product-lines
// subtree has no audit middleware, so the trail is written here.
func (h *Handler) logMutation(r *http.Request, plID string, before, after interface{}) {
	if h.logger == nil {
		return
	}
	actorID, actorRole := "", ""
	if claims := auth.GetClaims(r.Context()); claims != nil {
		actorID, actorRole = claims.UserID, claims.Role
	}
	h.logger.LogEvent(actorID, actorRole, "update", "evidence_config", plID, &plID, before, after, audit.ExtractIP(r))
}

// isUUID accepts the canonical 8-4-4-4-12 hex form. Checking here keeps a
// malformed id from becoming a database cast error and a 500.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

func jsonOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
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

func pathSegments(p, prefix string) []string {
	trimmed := strings.Trim(strings.TrimPrefix(p, prefix), "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
