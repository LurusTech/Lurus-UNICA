package repository

import (
	"database/sql"
	"testing"
)

func TestApplyDifyBindingFields_PopulatesFromConfig(t *testing.T) {
	pl := &ProductLine{}
	difyID := sql.NullString{String: "app-001", Valid: true}
	configJSON := []byte(`{"dify_dataset_id":"ds-001","other_key":"ignored"}`)

	applyDifyBindingFields(pl, difyID, configJSON)

	if pl.DifyAgentID == nil || *pl.DifyAgentID != "app-001" {
		t.Errorf("expected dify_agent_id 'app-001', got %v", pl.DifyAgentID)
	}
	if !pl.HasDifyBinding {
		t.Errorf("expected has_dify_binding=true")
	}
	if pl.DifyDatasetID == nil || *pl.DifyDatasetID != "ds-001" {
		t.Errorf("expected dify_dataset_id 'ds-001', got %v", pl.DifyDatasetID)
	}
}

func TestApplyDifyBindingFields_EmptyAgentID(t *testing.T) {
	pl := &ProductLine{}
	difyID := sql.NullString{}
	configJSON := []byte(`{}`)

	applyDifyBindingFields(pl, difyID, configJSON)

	if pl.DifyAgentID != nil {
		t.Errorf("expected nil dify_agent_id, got %v", pl.DifyAgentID)
	}
	if pl.HasDifyBinding {
		t.Errorf("expected has_dify_binding=false")
	}
	if pl.DifyDatasetID != nil {
		t.Errorf("expected nil dify_dataset_id, got %v", pl.DifyDatasetID)
	}
}

func TestApplyDifyBindingFields_MalformedConfigJSON(t *testing.T) {
	pl := &ProductLine{}
	difyID := sql.NullString{String: "app-002", Valid: true}
	configJSON := []byte(`not-json`)

	// Must not panic on malformed config_json; agent ID should still be applied.
	applyDifyBindingFields(pl, difyID, configJSON)

	if pl.DifyAgentID == nil || *pl.DifyAgentID != "app-002" {
		t.Errorf("expected dify_agent_id 'app-002', got %v", pl.DifyAgentID)
	}
	if pl.DifyDatasetID != nil {
		t.Errorf("expected nil dify_dataset_id for malformed config, got %v", pl.DifyDatasetID)
	}
}

func TestNewProductLineRepository(t *testing.T) {
	repo := NewProductLineRepository(nil)
	if repo == nil {
		t.Fatal("expected non-nil repository")
	}
}
