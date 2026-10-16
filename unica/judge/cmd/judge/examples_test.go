package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The README's first command runs on examples/. If it stops producing the
// findings the README promises, a newcomer's first ten minutes break, so the
// example is pinned here.
func TestExampleCheckup(t *testing.T) {
	dir := t.TempDir()
	html, data := filepath.Join(dir, "report.html"), filepath.Join(dir, "report.json")
	code := runCheckup([]string{
		"-in", "../../examples/chats.csv", "-format", "csv", "-csv-cols", "会话,时间,发送方,内容",
		"-facts", "../../examples/facts/demo-shop.yaml", "-packs", "common,ecommerce",
		"-line", "demo-shop", "-out", html, "-json", data,
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	raw, err := os.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Unbacked     []struct{ Got string } `json:"unbacked"`
		Redlines     []struct{ Got string } `json:"redlines"`
		MissingFacts []struct {
			Cue, Property string
		} `json:"missing_facts"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	got := func(fs []struct{ Got string }) string {
		var s []string
		for _, f := range fs {
			s = append(s, f.Got)
		}
		return strings.Join(s, ",")
	}
	if g := got(r.Unbacked); g != "50元、全额,48小时,免费" {
		t.Errorf("unbacked = %s", g)
	}
	if g := got(r.Redlines); g != "全网最低,最低价,假一赔十,终身保修" {
		t.Errorf("redlines = %s", g)
	}
	for _, m := range r.MissingFacts {
		if m.Property == "warranty_months" {
			t.Errorf("warranty_months is asserted in the example facts, yet named missing: %+v", m)
		}
	}
	page, _ := os.ReadFile(html)
	if strings.Contains(string(page)+string(raw), "13800000000") {
		t.Error("example phone number survived redaction")
	}
}
