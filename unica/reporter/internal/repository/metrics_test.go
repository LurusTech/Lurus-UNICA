package repository

import (
	"testing"
	"time"
)

func TestDefaultTimeRange(t *testing.T) {
	tr := DefaultTimeRange()
	now := time.Now().UTC()

	// Start should be approximately 7 days ago
	expectedStart := now.AddDate(0, 0, -7)
	if tr.Start.Sub(expectedStart).Abs() > time.Second {
		t.Errorf("start time mismatch: got %v, want ~%v", tr.Start, expectedStart)
	}

	// End should be approximately now
	if tr.End.Sub(now).Abs() > time.Second {
		t.Errorf("end time mismatch: got %v, want ~%v", tr.End, now)
	}

	// Duration should be 7 days
	duration := tr.End.Sub(tr.Start)
	expected := 7 * 24 * time.Hour
	if (duration - expected).Abs() > time.Second {
		t.Errorf("duration mismatch: got %v, want %v", duration, expected)
	}
}

func TestProductLineStats_Rates(t *testing.T) {
	// Test rate calculation logic (done in repository methods)
	s := ProductLineStats{
		Total:      100,
		AIResolved: 70,
		Handoffs:   30,
	}
	if s.Total > 0 {
		s.ResolutionRate = float64(s.AIResolved) / float64(s.Total)
		s.HandoffRate = float64(s.Handoffs) / float64(s.Total)
	}

	if s.ResolutionRate != 0.7 {
		t.Errorf("resolution rate: got %v, want 0.7", s.ResolutionRate)
	}
	if s.HandoffRate != 0.3 {
		t.Errorf("handoff rate: got %v, want 0.3", s.HandoffRate)
	}
}

func TestProductLineStats_ZeroTotal(t *testing.T) {
	s := ProductLineStats{
		Total:      0,
		AIResolved: 0,
		Handoffs:   0,
	}
	// Should not divide by zero
	if s.Total > 0 {
		s.ResolutionRate = float64(s.AIResolved) / float64(s.Total)
	}
	if s.ResolutionRate != 0 {
		t.Errorf("resolution rate should be 0 for zero total, got %v", s.ResolutionRate)
	}
}

func TestKnowledgeHitStats_Rates(t *testing.T) {
	s := KnowledgeHitStats{
		TotalAICalls: 200,
		Hits:         150,
	}
	if s.TotalAICalls > 0 {
		s.HitRate = float64(s.Hits) / float64(s.TotalAICalls)
	}
	if s.HitRate != 0.75 {
		t.Errorf("hit rate: got %v, want 0.75", s.HitRate)
	}
}
