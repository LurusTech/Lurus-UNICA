package platformsettings

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
	"time"
)

// scriptedConn answers every query with value (as one evolve_capture row), no
// rows when value is empty, or err.
type scriptedConn struct {
	recordingConn
	value string
	err   error
	reads int
}

func (c *scriptedConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	c.reads++
	if c.err != nil {
		return nil, c.err
	}
	return &oneRow{value: c.value}, nil
}

type oneRow struct {
	value string
	done  bool
}

func (r *oneRow) Columns() []string {
	return []string{"key", "value", "source", "note", "updated_by", "updated_at"}
}
func (r *oneRow) Close() error { return nil }
func (r *oneRow) Next(dest []driver.Value) error {
	if r.done || r.value == "" {
		return io.EOF
	}
	r.done = true
	dest[0], dest[1], dest[2], dest[3], dest[4], dest[5] =
		KeyEvolveCapture, r.value, SourceConsole, "", "", time.Now()
	return nil
}

type scriptedDriver struct{ conn *scriptedConn }

func (d *scriptedDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

func scriptedFlag(t *testing.T, ttl time.Duration) (*Flag, *scriptedConn) {
	t.Helper()
	conn := &scriptedConn{}
	name := "platformsettings_scripted_" + t.Name()
	sql.Register(name, &scriptedDriver{conn: conn})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewFlag(NewStore(db), KeyEvolveCapture, ttl), conn
}

func TestFlag_NoRowIsOff(t *testing.T) {
	f, _ := scriptedFlag(t, time.Hour)
	if f.On(context.Background()) {
		t.Error("an absent row must read as off")
	}
}

func TestFlag_ReadsOnAndCaches(t *testing.T) {
	f, conn := scriptedFlag(t, time.Hour)
	conn.value = "on"
	if !f.On(context.Background()) {
		t.Fatal("expected on")
	}
	conn.value = "off"
	if !f.On(context.Background()) || conn.reads != 1 {
		t.Errorf("a fresh reading should be reused within the TTL (reads=%d)", conn.reads)
	}
}

func TestFlag_ExpiredReadingIsRefreshed(t *testing.T) {
	f, conn := scriptedFlag(t, time.Nanosecond)
	conn.value = "on"
	f.On(context.Background())
	time.Sleep(time.Millisecond)
	conn.value = "off"
	if f.On(context.Background()) {
		t.Error("an expired reading should be refreshed")
	}
}

func TestFlag_FailedReadKeepsLastValue(t *testing.T) {
	f, conn := scriptedFlag(t, time.Nanosecond)
	conn.err = errors.New("db down")
	if f.On(context.Background()) {
		t.Error("a failed first read must fail closed")
	}
	conn.err, conn.value = nil, "on"
	time.Sleep(time.Millisecond)
	f.On(context.Background())
	conn.err = errors.New("db down again")
	time.Sleep(time.Millisecond)
	if !f.On(context.Background()) {
		t.Error("a failed later read should keep the last value actually read")
	}
}

func TestFlag_NilIsOff(t *testing.T) {
	var f *Flag
	if f.On(context.Background()) || NewFlag(nil, KeyEvolveCapture, 0).On(context.Background()) {
		t.Error("a nil flag or store must read as off")
	}
}
