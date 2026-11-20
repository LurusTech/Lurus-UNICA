package state

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// ChannelName reads the name an operator gave the channel, from either of the
// two places a channel can be configured: the static channels table, or a
// channel_configs row created from the console.
func TestChannelName_ReadsBothChannelTables(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var lineID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name) VALUES ($1) RETURNING id`, "channel-name-"+uuid.NewString()[:8],
	).Scan(&lineID); err != nil {
		t.Fatalf("create product line: %v", err)
	}
	staticID, configuredID := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO channels (id, platform, name, product_line_id) VALUES ($1, 'wechat', '微信公众号', $2)`,
		staticID, lineID); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO channel_configs (id, product_line_id, platform, display_name, app_id, app_secret_encrypted)
		 VALUES ($1, $2, 'taobao', '淘宝旗舰店', $3, '\x00')`,
		configuredID, lineID, "app-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("create channel config: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM channel_configs WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM channels WHERE product_line_id = $1`, lineID)
		db.Exec(`DELETE FROM product_lines WHERE id = $1`, lineID)
	})

	for id, want := range map[string]string{
		staticID:         "微信公众号",
		configuredID:     "淘宝旗舰店",
		uuid.NewString(): "",
		"not-a-uuid":     "",
		"":               "",
	} {
		if got := ChannelName(ctx, db, id); got != want {
			t.Errorf("ChannelName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestChannelName_NoDatabase(t *testing.T) {
	if got := ChannelName(context.Background(), nil, uuid.NewString()); got != "" {
		t.Errorf("ChannelName without a database = %q, want empty", got)
	}
}
