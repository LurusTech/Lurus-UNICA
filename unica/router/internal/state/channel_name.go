package state

import (
	"context"
	"database/sql"
	"errors"
	"log"
)

// ChannelName is the name an operator gave a channel: channels.name for a
// static channel, channel_configs.display_name for one configured in the
// console. It is empty when there is no database, the id names no channel or
// the lookup fails — callers that use it for display do without.
func ChannelName(ctx context.Context, db *sql.DB, channelID string) string {
	if db == nil || channelID == "" {
		return ""
	}
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM channels WHERE id::text = $1
		 UNION ALL
		 SELECT display_name FROM channel_configs WHERE id::text = $1
		 LIMIT 1`, channelID,
	).Scan(&name)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[state] warning: channel name lookup for %s failed: %v", channelID, err)
		}
		return ""
	}
	return name
}
