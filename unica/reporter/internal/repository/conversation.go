// Package repository provides database query operations for the reporter service.
package repository

import (
	"database/sql"
	"time"
)

// Repository wraps the database connection for report queries.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a new Repository.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// ProductLineStats holds aggregated stats for a single product line.
type ProductLineStats struct {
	ProductLineID  string  `json:"product_line_id"`
	ProductName    string  `json:"product_name,omitempty"`
	Total          int     `json:"total"`
	AIResolved     int     `json:"ai_resolved"`
	Handoffs       int     `json:"handoffs"`
	Closed         int     `json:"closed"`
	ResolutionRate float64 `json:"resolution_rate"`
	HandoffRate    float64 `json:"handoff_rate"`
}

// AgentStats holds performance metrics for a single agent.
type AgentStats struct {
	AgentID            string  `json:"agent_id"`
	AgentName          string  `json:"agent_name,omitempty"`
	ConversationCount  int     `json:"conversation_count"`
	AvgResponseSeconds float64 `json:"avg_response_seconds"`
	AvgSatisfaction    float64 `json:"avg_satisfaction"`
}

// TopQuestion holds a frequently asked question with its count.
type TopQuestion struct {
	Question string `json:"question"`
	Count    int    `json:"count"`
}

// DailyStats holds daily aggregated stats for trend charts.
type DailyStats struct {
	Date           string  `json:"date"`
	ProductLineID  string  `json:"product_line_id"`
	Total          int     `json:"total"`
	AIResolved     int     `json:"ai_resolved"`
	Handoffs       int     `json:"handoffs"`
	Closed         int     `json:"closed"`
	AvgResponse    float64 `json:"avg_response_seconds"`
	AvgSatisfaction float64 `json:"avg_satisfaction"`
}

// ChannelTraffic holds message count per channel.
type ChannelTraffic struct {
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name,omitempty"`
	Inbound     int    `json:"inbound"`
	Outbound    int    `json:"outbound"`
	Total       int    `json:"total"`
}

// TimeRange represents a time range for queries.
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// DefaultTimeRange returns the last 7 days.
func DefaultTimeRange() TimeRange {
	now := time.Now().UTC()
	return TimeRange{
		Start: now.AddDate(0, 0, -7),
		End:   now,
	}
}
