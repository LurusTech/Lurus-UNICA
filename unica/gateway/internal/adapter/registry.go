// Package adapter provides the ChannelAdapter interface and adapter registry.
package adapter

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/kefu/unica/pkg/model"
)

// Registry maps channel IDs to their ChannelAdapter implementations.
// It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]ChannelAdapter
}

// NewRegistry creates an empty adapter registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]ChannelAdapter),
	}
}

// Register adds or replaces an adapter for the given channel ID.
func (r *Registry) Register(channelID string, adapter ChannelAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[channelID] = adapter
	log.Printf("[registry] registered adapter for channel %s", channelID)
}

// Unregister removes the adapter for the given channel ID, if present.
// It is used to drop stale dynamic registrations after a config reload.
func (r *Registry) Unregister(channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.adapters[channelID]; ok {
		delete(r.adapters, channelID)
		log.Printf("[registry] unregistered adapter for channel %s", channelID)
	}
}

// Get returns the adapter for the given channel ID, or false if not found.
func (r *Registry) Get(channelID string) (ChannelAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[channelID]
	return a, ok
}

// Dispatch formats and sends an outbound message via the appropriate channel adapter.
// It looks up the adapter by msg.Data.ChannelID, calls FormatOutbound, then SendMessage.
func (r *Registry) Dispatch(ctx context.Context, msg *model.StandardMessage) error {
	channelID := msg.Data.ChannelID
	a, ok := r.Get(channelID)
	if !ok {
		return fmt.Errorf("no adapter registered for channel %s", channelID)
	}

	payload, err := a.FormatOutbound(ctx, msg)
	if err != nil {
		return fmt.Errorf("format outbound for channel %s: %w", channelID, err)
	}

	if err := a.SendMessage(ctx, channelID, payload); err != nil {
		return fmt.Errorf("send message to channel %s: %w", channelID, err)
	}

	return nil
}
