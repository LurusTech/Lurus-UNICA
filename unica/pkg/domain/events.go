package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The event feed is a read-only projection over rows the system already
// writes: handoff events, claim violations, ontology proposals (their arrival
// and their decision) and the audit log. It owns no table. Every row maps to
// exactly one Event by a pure function, so the portal's colour, icon, sound and
// swimlane all follow from one classification and can never disagree.

// EventCategory is the swimlane an event belongs to.
type EventCategory string

const (
	// EventForcedHandoff is a handoff policy demanded (ClassifyHandoffReason
	// says forced): a person must pick the conversation up.
	EventForcedHandoff EventCategory = "forced_handoff"
	// EventWithheld is a firewall or redline finding that held an answer back
	// (a blocking kind written with enforced=true).
	EventWithheld EventCategory = "withheld"
	// EventReducibleHandoff is a handoff a better ontology could remove.
	EventReducibleHandoff EventCategory = "reducible_handoff"
	// EventViolation is every other claim violation, including firewall
	// findings that were only recorded.
	EventViolation EventCategory = "violation"
	// EventOntology covers proposals arriving and being decided, and ontology
	// publish and rollback.
	EventOntology EventCategory = "ontology"
	// EventConfig covers switch and settings changes from the audit log.
	EventConfig EventCategory = "config"
)

// EventSeverity orders how loudly the portal may surface an event. Only
// urgent may ever make a sound on its own or raise a browser notification.
type EventSeverity string

const (
	SeverityInfo      EventSeverity = "info"
	SeverityAttention EventSeverity = "attention"
	SeverityUrgent    EventSeverity = "urgent"
)

// Event sources, which are also the second key of the cursor order. The
// spelling is part of the wire format (Event.ID is "<source>:<pk>").
const (
	EventSourceHandoff          = "handoff"
	EventSourceViolation        = "violation"
	EventSourceProposal         = "proposal"
	EventSourceProposalDecision = "proposal_decision"
	EventSourceAudit            = "audit"
)

// Event is one change the portal shows. Title and Detail are derived from the
// row's own fields; nothing is generated, and no rate is ever computed here.
type Event struct {
	ID             string        `json:"id"`
	At             time.Time     `json:"at"`
	Category       EventCategory `json:"category"`
	Severity       EventSeverity `json:"severity"`
	Title          string        `json:"title"`
	Detail         string        `json:"detail,omitempty"`
	ConversationID string        `json:"conversation_id,omitempty"`
	// Link is a portal-relative path ("evidence.html?conversation=…"), never
	// an absolute URL.
	Link   string `json:"link,omitempty"`
	Source string `json:"source"`
	// Reason is the machine reason or kind behind the event (a handoff
	// reason, a violation kind, an audit action), so the portal can label it
	// from its own map without parsing Title.
	Reason string `json:"reason,omitempty"`
	// Actor is who caused an audit or decision event, empty for events the
	// runtime wrote. The portal uses it to stay quiet about one's own actions.
	Actor string `json:"actor,omitempty"`
	// Resolves names the event this one settles ("violation:12" for a
	// review, "proposal:7" for a decision), so the portal can take the
	// original out of the inbox when the source row changes state.
	Resolves string `json:"resolves,omitempty"`
	// Before and After carry an audit row's states verbatim for the field-by-
	// field view; they are omitted for every other source.
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`
}

// EventCursor is a position in the feed's total order (At, Source, PK). An
// event is after the cursor when its triple compares greater. The zero cursor
// means "from the beginning of the read window".
type EventCursor struct {
	At     time.Time
	Source string
	PK     int64
}

// IsZero reports whether the cursor is the start of the feed.
func (c EventCursor) IsZero() bool { return c.At.IsZero() && c.Source == "" && c.PK == 0 }

// String encodes the cursor as an opaque URL-safe token. It doubles as the SSE
// event id, which is how Last-Event-ID resumes without loss or repetition.
func (c EventCursor) String() string {
	if c.IsZero() {
		return ""
	}
	raw := strconv.FormatInt(c.At.UTC().UnixNano(), 10) + "|" + c.Source + "|" + strconv.FormatInt(c.PK, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ErrBadCursor is returned for a token ParseEventCursor cannot read.
var ErrBadCursor = errors.New("bad event cursor")

// ParseEventCursor decodes a token produced by EventCursor.String. The empty
// token is the zero cursor.
func ParseEventCursor(tok string) (EventCursor, error) {
	if tok == "" {
		return EventCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return EventCursor{}, ErrBadCursor
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return EventCursor{}, ErrBadCursor
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return EventCursor{}, ErrBadCursor
	}
	pk, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || pk < 0 || !validEventSource(parts[1]) {
		return EventCursor{}, ErrBadCursor
	}
	return EventCursor{At: time.Unix(0, ns).UTC(), Source: parts[1], PK: pk}, nil
}

// EventIDCursor returns the cursor positioned at e, so a feed resumed from it
// starts strictly after e.
func EventIDCursor(at time.Time, source string, pk int64) EventCursor {
	return EventCursor{At: at.UTC(), Source: source, PK: pk}
}

func validEventSource(s string) bool {
	switch s {
	case EventSourceHandoff, EventSourceViolation, EventSourceProposal,
		EventSourceProposalDecision, EventSourceAudit:
		return true
	}
	return false
}

// eventID is the stable public id of an event.
func eventID(source string, pk int64) string { return fmt.Sprintf("%s:%d", source, pk) }
