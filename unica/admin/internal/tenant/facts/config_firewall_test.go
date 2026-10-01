package facts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/domain"
)

const configPath = "/api/v1/product-lines/pl-1/ontology-config"

func readConfig(t *testing.T, h *Handler) map[string]json.RawMessage {
	t.Helper()
	w := doJSON(t, h, http.MethodGet, configPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", w.Code, w.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// An older page that only knows inject_facts and validation must not switch
// the firewall off when it saves.
func TestOntologyConfig_PutKeepsFieldsItDoesNotCarry(t *testing.T) {
	h, _, fp := newOntologyFixture()

	w := doJSON(t, h, http.MethodPut, configPath,
		`{"commitment":"enforce","commitment_scope":"money","redline_packs":["common"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	w = doJSON(t, h, http.MethodPut, configPath, `{"inject_facts":true,"validation":"shadow"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	cfg := domain.LoadConfig(fp.config)
	if cfg.Commitment != domain.CommitmentEnforce || cfg.CommitmentScope != domain.CommitmentScopeMoney ||
		len(cfg.RedlinePacks) != 1 || cfg.RedlinePacks[0] != "common" {
		t.Errorf("firewall settings lost on a partial PUT: %+v", cfg)
	}
	if !cfg.InjectFacts || cfg.Validation != domain.ValidationShadow {
		t.Errorf("carried fields not applied: %+v", cfg)
	}

	// The same with the breaker block the current page also sends.
	w = doJSON(t, h, http.MethodPut, configPath,
		`{"inject_facts":true,"validation":"enforce","breaker":{"disabled":false,"trip_rate":0.3}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	cfg = domain.LoadConfig(fp.config)
	if cfg.Commitment != domain.CommitmentEnforce || cfg.Breaker.TripRate != 0.3 {
		t.Errorf("breaker PUT must keep the firewall and apply the breaker: %+v", cfg)
	}

	// And the other way round: switching the firewall leaves injection alone.
	w = doJSON(t, h, http.MethodPut, configPath, `{"commitment":"shadow"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	cfg = domain.LoadConfig(fp.config)
	if !cfg.InjectFacts || cfg.Validation != domain.ValidationEnforce || cfg.Commitment != domain.CommitmentShadow {
		t.Errorf("firewall-only PUT must keep the other switches: %+v", cfg)
	}
}

func TestOntologyConfig_RejectsUnknownFirewallValues(t *testing.T) {
	for name, body := range map[string]string{
		"unknown pack":  `{"redline_packs":["common","no_such_pack"]}`,
		"unknown mode":  `{"commitment":"enforcce"}`,
		"unknown scope": `{"commitment_scope":"everything"}`,
		"packs not ids": `{"redline_packs":"common"}`,
	} {
		h, _, fp := newOntologyFixture()
		w := doJSON(t, h, http.MethodPut, configPath, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, w.Code)
		}
		if fp.setKey != "" {
			t.Errorf("%s: nothing may be written on a rejected PUT", name)
		}
	}

	h, _, _ := newOntologyFixture()
	w := doJSON(t, h, http.MethodPut, configPath, `{"redline_packs":["no_such_pack"]}`)
	if !strings.Contains(w.Body.String(), "common") {
		t.Errorf("the 400 must list the packs that exist: %s", w.Body.String())
	}
}

func TestOntologyConfig_GetOffersPacksAndCorpusHint(t *testing.T) {
	h, _, _ := newOntologyFixture()
	out := readConfig(t, h)

	var commitment string
	if err := json.Unmarshal(out["commitment"], &commitment); err != nil || commitment != "off" {
		t.Errorf("an unconfigured line must read as off, got %s", out["commitment"])
	}
	var hint string
	if err := json.Unmarshal(out["corpus_hint"], &hint); err != nil || !strings.Contains(hint, "shadow") {
		t.Errorf("corpus_hint missing or does not advise shadow first: %s", out["corpus_hint"])
	}
	var packs []domain.RedlinePackInfo
	if err := json.Unmarshal(out["redline_pack_options"], &packs); err != nil || len(packs) == 0 {
		t.Fatalf("redline_pack_options missing: %s", out["redline_pack_options"])
	}
	if packs[0].ID == "" || packs[0].Label == "" {
		t.Errorf("pack option incomplete: %+v", packs[0])
	}
}

// Duplicated pack ids are stored once.
func TestOntologyConfig_DedupesPacks(t *testing.T) {
	h, _, fp := newOntologyFixture()
	w := doJSON(t, h, http.MethodPut, configPath, `{"redline_packs":["common","common"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if cfg := domain.LoadConfig(fp.config); len(cfg.RedlinePacks) != 1 {
		t.Errorf("packs = %v, want one", cfg.RedlinePacks)
	}
}
