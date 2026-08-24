package routecache

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeChannels struct {
	ids []string
	err error
}

func (f *fakeChannels) ListIDs(context.Context, string) ([]string, error) { return f.ids, f.err }

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()}), mr
}

func TestInvalidate_DropsEveryChannelRoute(t *testing.T) {
	rdb, mr := newRedis(t)
	ctx := context.Background()
	mr.Set(keyPrefix+"ch-1", "cached")
	mr.Set(keyPrefix+"ch-2", "cached")
	mr.Set(keyPrefix+"ch-other", "another tenant's")

	inv := New(rdb, &fakeChannels{ids: []string{"ch-1", "ch-2"}})
	if !inv.Invalidate(ctx, "pl-1") {
		t.Fatal("a successful invalidation must report true")
	}
	for _, k := range []string{keyPrefix + "ch-1", keyPrefix + "ch-2"} {
		if mr.Exists(k) {
			t.Errorf("%s survived; the router would keep serving the old settings", k)
		}
	}
	if !mr.Exists(keyPrefix + "ch-other") {
		t.Error("another tenant's cached route must not be touched")
	}
}

// The boolean is the whole point of the shared implementation: a console that
// promises "takes effect immediately" needs to know when that is untrue.
func TestInvalidate_ReportsFalseWhenItCannotRun(t *testing.T) {
	rdb, _ := newRedis(t)
	ctx := context.Background()

	cases := map[string]*Invalidator{
		"nil invalidator": nil,
		"no redis client": New(nil, &fakeChannels{ids: []string{"ch-1"}}),
		"no channel list": New(rdb, nil),
		"lookup failed":   New(rdb, &fakeChannels{err: errors.New("db down")}),
	}
	for name, inv := range cases {
		if inv.Invalidate(ctx, "pl-1") {
			t.Errorf("%s: reported success without dropping anything", name)
		}
	}
}

// A tenant with no channels has nothing cached, and that is a real success —
// reporting it as a failure would make the console warn about every write on a
// tenant that has not been connected yet.
func TestInvalidate_NoChannelsIsSuccess(t *testing.T) {
	rdb, _ := newRedis(t)
	if !New(rdb, &fakeChannels{ids: nil}).Invalidate(context.Background(), "pl-1") {
		t.Error("a tenant with no channels must not be reported as a failed invalidation")
	}
}
