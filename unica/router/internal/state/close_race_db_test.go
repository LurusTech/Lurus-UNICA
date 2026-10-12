package state

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Two closers race for one conversation — an agent's resolve and the idle
// sweep, or two router replicas — and both read it as open. Exactly one may
// close it and run the on-close hooks; the survey goes out once.
func TestTransitionState_ConcurrentCloseFiresOnCloseOnce(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	channelID := "11111111-2222-3333-4444-555555555501"
	productLineID := "6f304ef6-e2db-4f97-8d5a-795d834227e1"

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name)
		 VALUES ($1, $2, 'close race test') RETURNING id`,
		"close-race-"+suffix, channelID).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	var convID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state)
		 VALUES ($1, $2, $3, 'human_processing') RETURNING id`,
		channelID, productLineID, customerID).Scan(&convID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		db.Exec(`DELETE FROM customers WHERE id = $1`, customerID)
	})

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	cache := NewCache(rdb)

	m := NewManager(NewRepository(db), cache, ManagerConfig{IdleTimeout: time.Hour, CheckInterval: time.Minute})
	fired := 0
	m.SetOnClose(func(ctx context.Context, conv *Conversation) { fired++ })

	// Both closers saw the conversation with an agent.
	stale := &SessionData{State: string(StateHumanProcessing), ProductLineID: productLineID}
	if err := cache.SetSession(ctx, convID, stale); err != nil {
		t.Fatal(err)
	}
	if err := m.TransitionState(ctx, convID, StateClosed, "first"); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := cache.SetSession(ctx, convID, stale); err != nil {
		t.Fatal(err)
	}
	err := m.TransitionState(ctx, convID, StateClosed, "second")
	var invalid *ErrInvalidTransition
	if !errors.As(err, &invalid) || invalid.From != StateClosed {
		t.Fatalf("second close = %v, want an invalid transition from closed", err)
	}
	if fired != 1 {
		t.Errorf("OnClose fired %d times, want 1", fired)
	}

	// The idle sweep finding it closed already is no error and no second hook.
	if err := NewRepository(db).UpdateConversationState(ctx, convID, StateClosed); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("repeated close = %v, want ErrAlreadyClosed", err)
	}
}
