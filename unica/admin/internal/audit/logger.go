package audit

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// Logger provides asynchronous audit log writing via a buffered channel.
type Logger struct {
	repo    *Repository
	entries chan *AuditEntry
	done    chan struct{}
}

// NewLogger creates a new audit logger with the given buffer size.
// Entries are written asynchronously in a background goroutine.
func NewLogger(repo *Repository, bufferSize int) *Logger {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	l := &Logger{
		repo:    repo,
		entries: make(chan *AuditEntry, bufferSize),
		done:    make(chan struct{}),
	}
	go l.processLoop()
	return l
}

// Log enqueues an audit entry for async writing.
// If the buffer is full, the entry is dropped with a warning log.
func (l *Logger) Log(entry *AuditEntry) {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	select {
	case l.entries <- entry:
		// enqueued
	default:
		log.Printf("[audit] WARNING: buffer full, dropping audit entry: action=%s resource=%s/%s",
			entry.Action, entry.ResourceType, entry.ResourceID)
	}
}

// LogEvent is a convenience method that builds an AuditEntry from parameters.
func (l *Logger) LogEvent(
	actorID, actorRole, action, resourceType, resourceID string,
	productLineID *string,
	beforeState, afterState interface{},
	ipAddress string,
) {
	entry := &AuditEntry{
		ActorID:      actorID,
		ActorRole:    actorRole,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		CreatedAt:    time.Now(),
	}

	if productLineID != nil && *productLineID != "" {
		entry.ProductLineID = productLineID
	}

	if beforeState != nil {
		data, err := json.Marshal(beforeState)
		if err == nil {
			raw := json.RawMessage(data)
			entry.BeforeState = &raw
		}
	}
	if afterState != nil {
		data, err := json.Marshal(afterState)
		if err == nil {
			raw := json.RawMessage(data)
			entry.AfterState = &raw
		}
	}

	if ipAddress != "" {
		entry.IPAddress = &ipAddress
	}

	l.Log(entry)
}

// Close drains the buffer and shuts down the background goroutine.
func (l *Logger) Close() {
	close(l.entries)
	<-l.done
}

// processLoop reads entries from the channel and writes them to the database.
func (l *Logger) processLoop() {
	defer close(l.done)

	for entry := range l.entries {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := l.repo.Insert(ctx, entry); err != nil {
			log.Printf("[audit] ERROR: failed to write audit log: %v (action=%s resource=%s/%s)",
				err, entry.Action, entry.ResourceType, entry.ResourceID)
		}
		cancel()
	}
}
