// Package ratelimit provides sliding window rate limiting and burst detection for the gateway.
package ratelimit

import "time"

// ChannelLimit defines rate limit parameters for a specific channel.
type ChannelLimit struct {
	Channel         string        // Channel identifier (e.g., "wechat", "douyin")
	MaxRequests     int64         // Maximum requests allowed per window
	Window          time.Duration // Sliding window duration
	BurstMultiplier float64       // Burst threshold = (MaxRequests / Window * 10s) * BurstMultiplier
	CooldownSec     int           // Seconds to throttle after burst detection
}

// Config holds rate limiting configuration for all channels.
type Config struct {
	Enabled  bool
	Channels map[string]ChannelLimit
	Default  ChannelLimit // Fallback when no channel-specific config exists
}

// DefaultConfig returns a production-ready rate limit configuration with
// per-channel limits for all 5 supported platforms.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Default: ChannelLimit{
			MaxRequests:     600,
			Window:          time.Minute,
			BurstMultiplier: 3.0,
			CooldownSec:     30,
		},
		Channels: map[string]ChannelLimit{
			"wechat": {
				Channel:         "wechat",
				MaxRequests:     1000,
				Window:          time.Minute,
				BurstMultiplier: 3.0,
				CooldownSec:     30,
			},
			"douyin": {
				Channel:         "douyin",
				MaxRequests:     500,
				Window:          time.Minute,
				BurstMultiplier: 3.0,
				CooldownSec:     30,
			},
			"xiaohongshu": {
				Channel:         "xiaohongshu",
				MaxRequests:     500,
				Window:          time.Minute,
				BurstMultiplier: 3.0,
				CooldownSec:     30,
			},
			"taobao": {
				Channel:         "taobao",
				MaxRequests:     800,
				Window:          time.Minute,
				BurstMultiplier: 3.0,
				CooldownSec:     30,
			},
			"kuaishou": {
				Channel:         "kuaishou",
				MaxRequests:     500,
				Window:          time.Minute,
				BurstMultiplier: 3.0,
				CooldownSec:     30,
			},
		},
	}
}

// LimitFor returns the ChannelLimit for the given channel, falling back
// to the default if no channel-specific config exists.
func (c *Config) LimitFor(channel string) ChannelLimit {
	if cl, ok := c.Channels[channel]; ok {
		return cl
	}
	def := c.Default
	def.Channel = channel
	return def
}
