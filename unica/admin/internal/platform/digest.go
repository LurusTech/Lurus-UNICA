package platform

// digest.go serves a preview of the operator to-do push opsdigest builds and
// sends: the same content, in the same two shapes, that the scheduled sender
// would either POST to the alert webhook or skip. It exists for two readers —
// an operator confirming the message reads right before OPS_DIGEST_WEBHOOK_URL
// is wired up, and a deployment that never wires one up at all and wants the
// same backlog view without a webhook to look at it through.
//
// Nothing here writes. Every figure it reports is opsdigest.Builder's own
// read of ViolationStatsSince, the pending proposal count, VersionQuality and
// Northstar — this file only decides who may ask for it and stretches the
// deadline to the work, the same as the knowledge and prompt rosters beside
// it.

import (
	"context"
	"net/http"
	"time"

	"github.com/kefu/unica/admin/internal/auth"
	"github.com/kefu/unica/admin/internal/opsdigest"
	"github.com/kefu/unica/admin/internal/rbac"
)

// digestBuilder is the slice of opsdigest.Builder this endpoint needs.
type digestBuilder interface {
	Build(ctx context.Context, now time.Time, weekly bool) (opsdigest.Result, error)
}

// digestWindow is the preview's read budget. Every query Builder runs is a
// cheap local read against this database (no Dify round trip, unlike the
// knowledge and prompt rosters beside this file), so a flat generous window
// covers a fleet far larger than this deployment's without needing the
// roster size up front the way promptWindow(n, ...) does for the Dify-bound
// endpoints.
const digestWindow = 30 * time.Second

// DigestHandler serves GET /api/v1/platform/digest.
type DigestHandler struct {
	builder digestBuilder
	// weekday is the same day opsdigest.Digest is configured with, so a
	// preview taken on a Monday shows the same Northstar section the real
	// send would carry that Monday rather than one the caller's own guess
	// might disagree with.
	weekday time.Weekday
}

// NewDigestHandler creates the preview handler.
func NewDigestHandler(builder digestBuilder, weekday time.Weekday) *DigestHandler {
	return &DigestHandler{builder: builder, weekday: weekday}
}

type digestResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Weekly says whether this preview carries the Northstar section, so a
	// caller previewing on a non-weekly day is not left wondering why the
	// three north-star lines are missing.
	Weekly   bool                          `json:"weekly"`
	Empty    bool                          `json:"empty"`
	Markdown string                        `json:"markdown"`
	Payload  opsdigest.AlertManagerPayload `json:"payload"`
}

// HandleList answers GET /api/v1/platform/digest?preview=1.
//
// Administrator only, matching every other platform-wide roster: this is
// every tenant's backlog in one payload. preview=1 is required so the
// endpoint is never reached by an automated client expecting the shape it
// would receive from the webhook — this one always answers 200 with the same
// envelope whether or not there is anything to report, and an empty webhook
// payload silently treated the same as a working preview would be a sending
// mistake this parameter exists to catch before it is made.
func (h *DigestHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	claims := auth.GetClaims(r.Context())
	if claims == nil || !rbac.IsAdmin(claims.Role) {
		errorJSON(w, http.StatusForbidden, "administrator role required")
		return
	}
	if r.URL.Query().Get("preview") != "1" {
		errorJSON(w, http.StatusBadRequest, "加上 ?preview=1 以确认这是预览请求；本端点从不真正发送 webhook")
		return
	}

	ctx, cancel := stretch(w, r, digestWindow)
	defer cancel()

	now := time.Now()
	weekly := now.Weekday() == h.weekday
	result, err := h.builder.Build(ctx, now, weekly)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, "生成运营待办摘要失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, digestResponse{
		GeneratedAt: now,
		Weekly:      weekly,
		Empty:       result.Empty,
		Markdown:    result.Markdown,
		Payload:     result.Payload,
	})
}
