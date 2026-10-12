// Package server is the HTTP face of the gate. It is deliberately small:
// judge, read or replace the facts, read or verify the evidence, read the
// shadow summary. There is no console here and there will not be one — the
// console is a product built on the evidence this emits, not a part of the
// gate.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/kefu/unica/judge/internal/gate"
)

// maxBody bounds a request body. A reply is a few kilobytes; a megabyte
// means a misrouted upload.
const maxBody = 1 << 20

// Server serves the gate.
type Server struct {
	gate  *gate.Gate
	token string
	mux   *http.ServeMux
}

// New builds a server. An empty token leaves the API open, which is right
// on a laptop and wrong anywhere else; main warns when it is empty and the
// address is not loopback.
func New(g *gate.Gate, token string) *Server {
	s := &Server{gate: g, token: token, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("POST /judge", s.auth(s.judge))
	s.mux.HandleFunc("GET /facts", s.auth(s.getFacts))
	s.mux.HandleFunc("PUT /facts", s.auth(s.putFacts))
	s.mux.HandleFunc("GET /facts/{line}", s.auth(s.getFacts))
	s.mux.HandleFunc("PUT /facts/{line}", s.auth(s.putFacts))
	s.mux.HandleFunc("GET /evidence/{line}/{seq}", s.auth(s.getEvidence))
	s.mux.HandleFunc("GET /evidence/{line}", s.auth(s.verifyEvidence))
	s.mux.HandleFunc("GET /shadow", s.auth(s.shadow))
	s.mux.HandleFunc("GET /shadow/{line}", s.auth(s.shadow))
	return s
}

// Handle mounts an additional handler, for adapters.
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or wrong bearer token")
			return
		}
		next(w, r)
	}
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	snap := s.gate.Facts().Snapshot(gate.DefaultLine)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"mode":          s.gate.Mode(),
		"packs":         s.gate.Packs(),
		"facts_loaded":  snap.Ontology != nil,
		"facts_version": snap.Version,
		"facts_lines":   s.gate.Facts().Lines(),
		"per_line":      s.gate.Facts().PerLine(),
	})
}

func (s *Server) judge(w http.ResponseWriter, r *http.Request) {
	var req gate.Request
	if err := decodeJSON(w, r, &req); err != nil {
		status := http.StatusBadRequest
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, err.Error())
		return
	}
	resp, err := s.gate.Judge(req)
	switch {
	case errors.Is(err, gate.ErrEmptyAnswer):
		writeError(w, http.StatusBadRequest, "answer is required")
		return
	case errors.Is(err, gate.ErrBadLine):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		log.Printf("[judge] %v", err)
		writeError(w, http.StatusInternalServerError, "could not record evidence")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getFacts(w http.ResponseWriter, r *http.Request) {
	line := r.PathValue("line")
	if line != "" && !gate.ValidLine(line) {
		writeError(w, http.StatusBadRequest, "bad line name")
		return
	}
	snap := s.gate.Facts().Snapshot(line)
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"line": snap.Line, "version": snap.Version, "ontology": snap.Ontology})
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("X-Facts-Version", itoa(snap.Version))
	io.WriteString(w, snap.Source)
}

// putFacts replaces the facts with the YAML body. The reply names the parse
// error in full: the person writing facts is the person who must fix them,
// and "invalid" tells them nothing.
func (s *Server) putFacts(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	line := r.PathValue("line")
	if err := s.gate.Facts().Replace(line, string(body)); err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, gate.ErrSingleFile) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	snap := s.gate.Facts().Snapshot(line)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "line": snap.Line, "version": snap.Version, "facts_loaded": snap.Ontology != nil})
}

func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request) {
	line, seq, err := gate.ParseID(r.PathValue("line") + "/" + r.PathValue("seq"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	e, err := s.gate.Evidence().Get(line, seq)
	if errors.Is(err, gate.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such evidence")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": gate.ID(line, seq), "entry": e})
}

func (s *Server) verifyEvidence(w http.ResponseWriter, r *http.Request) {
	line := r.PathValue("line")
	res := s.gate.Evidence().Verify(line)
	out := map[string]any{"line": line, "verify": res}
	if head, ok := s.gate.Evidence().Head(line); ok {
		out["head"] = head
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) shadow(w http.ResponseWriter, r *http.Request) {
	line := r.PathValue("line")
	if line == "" {
		lines := s.gate.Lines()
		stats := make([]gate.LineStats, 0, len(lines))
		for _, l := range lines {
			stats = append(stats, s.gate.Stats(l))
		}
		writeJSON(w, http.StatusOK, map[string]any{"lines": stats, "min_samples": gate.MinShadowSamples})
		return
	}
	writeJSON(w, http.StatusOK, s.gate.Stats(line))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// 保留 %w 以便调用方识别超限错误并返回 413。
		return fmt.Errorf("bad json: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
