package domain

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// execRecorder is a database/sql driver that records statements and accepts
// them, so the exact SQL a recorder sends can be asserted without Postgres.
type execRecorder struct {
	queries []string
	args    [][]driver.NamedValue
}

func (c *execRecorder) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *execRecorder) Close() error                        { return nil }
func (c *execRecorder) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *execRecorder) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.queries = append(c.queries, q)
	c.args = append(c.args, args)
	return driver.RowsAffected(1), nil
}

type execRecorderDriver struct{ conn *execRecorder }

func (d *execRecorderDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

func recordingDomainStore(t *testing.T) (*Store, *execRecorder) {
	t.Helper()
	conn := &execRecorder{}
	name := "domain_recorder_" + t.Name()
	sql.Register(name, &execRecorderDriver{conn: conn})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, 0), conn
}

// With capture off the insert must be the pre-023 statement, column for
// column: a deployment that has not applied 023 would otherwise lose every
// handoff row the moment this code shipped, switch or no switch.
func TestRecordHandoffEvent_CaptureOffUsesThePre023Statement(t *testing.T) {
	s, conn := recordingDomainStore(t)
	s.RecordHandoffEvent(context.Background(), HandoffEvent{
		ConversationID: "c", ProductLineID: "p", Reason: "low_confidence",
	})
	if len(conn.queries) != 1 {
		t.Fatalf("got %d statements", len(conn.queries))
	}
	q := conn.queries[0]
	for _, col := range []string{"correlation_id", "customer_message", "suppressed_answer"} {
		if strings.Contains(q, col) {
			t.Errorf("capture is off but the insert names %s:\n%s", col, q)
		}
	}
	if len(conn.args[0]) != 6 {
		t.Errorf("pre-023 insert takes 6 arguments, got %d", len(conn.args[0]))
	}
}

func TestRecordHandoffEvent_CaptureOnWritesTheMoment(t *testing.T) {
	s, conn := recordingDomainStore(t)
	s.RecordHandoffEvent(context.Background(), HandoffEvent{
		ConversationID: "c", ProductLineID: "p", Reason: "claim_conflict", AnswerSuppressed: true,
		Capture: &HandoffCapture{CorrelationID: "corr-1", CustomerMessage: "能退吗", SuppressedAnswer: "可以退"},
	})
	q, args := conn.queries[0], conn.args[0]
	for _, col := range []string{"correlation_id", "customer_message", "suppressed_answer"} {
		if !strings.Contains(q, col) {
			t.Errorf("capture is on but the insert omits %s", col)
		}
	}
	if len(args) != 9 || args[6].Value != "corr-1" || args[7].Value != "能退吗" || args[8].Value != "可以退" {
		t.Errorf("capture arguments wrong: %+v", args)
	}
}

func TestRecordAnswerTrace_EmptyListsAreArrays(t *testing.T) {
	s, conn := recordingDomainStore(t)
	s.RecordAnswerTrace(context.Background(), AnswerTrace{
		ConversationID: "c", ProductLineID: "p", CustomerMessage: "q", RawAnswer: "a [FACT:x=1]",
		Decision: "send", Reason: "confidence_ok",
	})
	if len(conn.queries) != 1 {
		t.Fatalf("got %d statements", len(conn.queries))
	}
	args := conn.args[0]
	if args[5].Value != "[]" || args[11].Value != "[]" {
		t.Errorf("retrieval/violations should serialise as [], got %v / %v", args[5].Value, args[11].Value)
	}
	if args[6].Value != nil {
		t.Errorf("no active ontology must store NULL, got %v", args[6].Value)
	}
	if args[4].Value != "a [FACT:x=1]" {
		t.Errorf("the raw answer must keep its tags, got %v", args[4].Value)
	}
}

func TestCaptureRecordersAreNilSafe(t *testing.T) {
	var s *Store
	s.RecordAnswerTrace(context.Background(), AnswerTrace{})
	s.RecordHandoffEvent(context.Background(), HandoffEvent{Capture: &HandoffCapture{}})
}
