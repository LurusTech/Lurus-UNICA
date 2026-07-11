package token

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

var (
	// TokenRefreshTotal counts token refresh attempts by channel and status.
	TokenRefreshTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_token_refresh_total",
		Help: "Total token refresh attempts",
	}, []string{"channel", "status"})

	// TokenRefreshDuration tracks token refresh latency.
	TokenRefreshDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_token_refresh_duration_seconds",
		Help:    "Token refresh latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"channel"})
)

// channelEntry holds per-channel registration data.
type channelEntry struct {
	credentials ChannelCredentials
	refresher   TokenRefresher
}

// TokenManager manages access tokens for multiple channels using Redis as backing store.
type TokenManager struct {
	rdb          *redis.Client
	channels     map[string]*channelEntry // channelID -> entry
	sfGroup      singleflight.Group
	buffer       time.Duration
	retryMax     int
	retryBackoff time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup
	mu           sync.RWMutex
}

// NewTokenManager creates a new TokenManager.
func NewTokenManager(rdb *redis.Client, buffer time.Duration, retryMax int, retryBackoff time.Duration) *TokenManager {
	return &TokenManager{
		rdb:          rdb,
		channels:     make(map[string]*channelEntry),
		buffer:       buffer,
		retryMax:     retryMax,
		retryBackoff: retryBackoff,
		stopCh:       make(chan struct{}),
	}
}

// RegisterChannel registers a channel for token management.
func (m *TokenManager) RegisterChannel(channelID string, creds ChannelCredentials, refresher TokenRefresher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[channelID] = &channelEntry{
		credentials: creds,
		refresher:   refresher,
	}
	log.Printf("[token] registered channel %s (platform=%s)", channelID, refresher.Platform())
}

// redisKey returns the Redis key for a channel's token.
func redisKey(channelID string) string {
	return fmt.Sprintf("channel:%s:token", channelID)
}

// GetToken returns the current access token for the given channel.
// It reads from Redis first; on cache miss it triggers a singleflight refresh.
func (m *TokenManager) GetToken(ctx context.Context, channelID string) (string, error) {
	// Try Redis cache first
	tok, err := m.rdb.Get(ctx, redisKey(channelID)).Result()
	if err == nil && tok != "" {
		return tok, nil
	}

	// Cache miss — do a singleflight refresh
	result, err, _ := m.sfGroup.Do(channelID, func() (interface{}, error) {
		return m.refreshToken(ctx, channelID)
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// refreshToken fetches a new token and stores it in Redis.
func (m *TokenManager) refreshToken(ctx context.Context, channelID string) (string, error) {
	m.mu.RLock()
	entry, ok := m.channels[channelID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("channel %s not registered", channelID)
	}

	start := time.Now()
	var lastErr error

	for attempt := 1; attempt <= m.retryMax; attempt++ {
		result, err := entry.refresher.Refresh(ctx, entry.credentials)
		if err == nil {
			// Store in Redis with TTL = expires_in - buffer
			ttl := time.Duration(result.ExpiresIn)*time.Second - m.buffer
			if ttl <= 0 {
				ttl = time.Duration(result.ExpiresIn) * time.Second / 2
			}

			if setErr := m.rdb.Set(ctx, redisKey(channelID), result.AccessToken, ttl).Err(); setErr != nil {
				log.Printf("[token] WARNING: failed to cache token in Redis for %s: %v", channelID, setErr)
			}

			elapsed := time.Since(start).Seconds()
			TokenRefreshDuration.WithLabelValues(channelID).Observe(elapsed)
			TokenRefreshTotal.WithLabelValues(channelID, "success").Inc()
			log.Printf("[token] refreshed token for %s (expires_in=%ds, ttl=%v)", channelID, result.ExpiresIn, ttl)
			return result.AccessToken, nil
		}

		lastErr = err
		log.Printf("[token] refresh attempt %d/%d failed for %s: %v", attempt, m.retryMax, channelID, err)

		if attempt < m.retryMax {
			select {
			case <-ctx.Done():
				TokenRefreshTotal.WithLabelValues(channelID, "error").Inc()
				return "", ctx.Err()
			case <-time.After(m.retryBackoff * time.Duration(attempt)):
			}
		}
	}

	// All retries exhausted — publish alert to Redis Stream
	TokenRefreshTotal.WithLabelValues(channelID, "error").Inc()
	m.publishAlert(ctx, channelID, lastErr)
	return "", fmt.Errorf("token refresh failed after %d retries for %s: %w", m.retryMax, channelID, lastErr)
}

// publishAlert sends a failure alert to the unica:alerts Redis Stream.
func (m *TokenManager) publishAlert(ctx context.Context, channelID string, err error) {
	alertCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fields := map[string]interface{}{
		"type":       "token_refresh_failure",
		"channel_id": channelID,
		"error":      err.Error(),
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}

	if xaddErr := m.rdb.XAdd(alertCtx, &redis.XAddArgs{
		Stream: "unica:alerts",
		Values: fields,
	}).Err(); xaddErr != nil {
		log.Printf("[token] WARNING: failed to publish alert for %s: %v", channelID, xaddErr)
	} else {
		log.Printf("[token] alert published for channel %s: %v", channelID, err)
	}
}

// Start begins background refresh goroutines for all registered channels.
func (m *TokenManager) Start(ctx context.Context) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for channelID := range m.channels {
		m.wg.Add(1)
		go m.backgroundRefresh(ctx, channelID)
	}
	log.Printf("[token] started background refresh for %d channels", len(m.channels))
}

// backgroundRefresh periodically refreshes the token for a channel before it expires.
func (m *TokenManager) backgroundRefresh(ctx context.Context, channelID string) {
	defer m.wg.Done()

	// Do an initial refresh immediately
	tok, err := m.refreshToken(ctx, channelID)
	if err != nil {
		log.Printf("[token] initial refresh failed for %s: %v", channelID, err)
	}
	_ = tok

	for {
		// Check TTL of current token to determine next refresh time
		ttl, err := m.rdb.TTL(ctx, redisKey(channelID)).Result()
		if err != nil || ttl <= 0 {
			ttl = 30 * time.Second // fallback: retry soon
		}

		// Refresh when ~80% of TTL has elapsed (i.e., wait for 80% of remaining TTL)
		waitDuration := ttl * 4 / 5
		if waitDuration < 10*time.Second {
			waitDuration = 10 * time.Second
		}

		select {
		case <-m.stopCh:
			log.Printf("[token] background refresh stopped for %s", channelID)
			return
		case <-ctx.Done():
			log.Printf("[token] background refresh context done for %s", channelID)
			return
		case <-time.After(waitDuration):
			if _, err := m.refreshToken(ctx, channelID); err != nil {
				log.Printf("[token] background refresh failed for %s: %v", channelID, err)
			}
		}
	}
}

// Stop gracefully stops all background refresh goroutines.
func (m *TokenManager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
	log.Println("[token] all background refresh goroutines stopped")
}
