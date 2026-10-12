package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/kefu/unica/pkg/domain"
)

// FactsStore holds the business's facts, one ontology per product line,
// each authored as YAML and replaced whole. There is no merge and no
// partial edit — the file is the truth and the gate only ever reads it or
// swaps it for a new one that parsed.
//
// Lines exist because the first buyer is not one shop but an agency running
// dozens: each shop's facts are its own file, judged under its own name,
// recorded in its own evidence chain. A single shop never sees the
// distinction — it uses the default line and one file.
//
// Three backings:
//   - memory: facts live until the process exits (a trial);
//   - one file: the default line only, written back atomically on PUT;
//   - a directory: <dir>/<line>.yaml per line, every file loaded at start.
type FactsStore struct {
	path  string
	isDir bool

	// wmu 串行化 Replace 的“写盘+换内存”，否则同一店并发 PUT 会并发 rename
	// 同一目标（Windows 下 Access is denied），且盘上与内存可能落到不同版本。
	wmu   sync.Mutex
	mu    sync.RWMutex
	lines map[string]*factsLine
}

type factsLine struct {
	source  string
	ont     *domain.Ontology
	version int
}

// FactsSnapshot is what one judgement runs against: the ontology, the exact
// YAML it was parsed from (for the evidence digest), and the store's own
// version counter for the line, which increments on every accepted
// replacement. Version is the gate's, not the YAML's `version:` field: an
// operator who forgets to bump the file still gets rows that tell two edits
// apart. Zero with a nil ontology means the line has no facts.
type FactsSnapshot struct {
	Line     string
	Ontology *domain.Ontology
	Source   string
	Version  int
}

// lineNamePattern bounds a line name so it is safe as a file name and an
// evidence id component.
// 只允许小写：大小写不敏感的文件系统上 Shop 与 shop 会共用同一文件。
var lineNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// winReserved 匹配 Windows 保留设备名（取第一个点之前、不分大小写）。
var winReserved = regexp.MustCompile(`^(con|prn|aux|nul|com[1-9]|lpt[1-9])$`)

// ValidLine reports whether a line name may be used. 名字必须已是小写，
// 不会被悄悄转换；结尾为点号、或首段为 Windows 保留设备名的一律拒绝。
func ValidLine(line string) bool {
	if !lineNamePattern.MatchString(line) || strings.HasSuffix(line, ".") {
		return false
	}
	base, _, _ := strings.Cut(line, ".")
	return !winReserved.MatchString(strings.ToLower(base))
}

// ErrSingleFile is returned when a line other than the default is written
// to a store backed by one file.
var ErrSingleFile = fmt.Errorf("facts store is a single file and holds the %q line only; start with -facts <directory> for per-line facts", DefaultLine)

// NewMemoryFacts is a store with no file behind it.
func NewMemoryFacts() *FactsStore { return &FactsStore{lines: map[string]*factsLine{}} }

// OpenFacts loads the facts at path: a directory of <line>.yaml files, or a
// single file holding the default line. A missing path is not an error —
// the gate starts with no facts and a PUT creates it — but a file that does
// not parse is, because a gate that silently ran without the facts it was
// pointed at would be worse than one that refused to start.
func OpenFacts(path string) (*FactsStore, error) {
	s := &FactsStore{path: path, lines: map[string]*factsLine{}}
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		// A path with a .yaml/.yml suffix is a file to be created on PUT;
		// anything else is a directory to be created on PUT.
		s.isDir = !hasYAMLSuffix(path)
		return s, nil
	case err != nil:
		return nil, err
	case info.IsDir():
		s.isDir = true
		matches, err := filepath.Glob(filepath.Join(path, "*.yaml"))
		if err != nil {
			return nil, err
		}
		for _, p := range matches {
			line := strings.TrimSuffix(filepath.Base(p), ".yaml")
			if !ValidLine(line) {
				return nil, fmt.Errorf("%s: %q is not a usable line name", p, line)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			if err := s.load(line, string(data)); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
		}
		return s, nil
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := s.load(DefaultLine, string(data)); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return s, nil
	}
}

func hasYAMLSuffix(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".yaml" || ext == ".yml"
}

// load parses and installs a line's facts without writing anything.
func (s *FactsStore) load(line, yamlSource string) error {
	var ont *domain.Ontology
	if strings.TrimSpace(yamlSource) != "" {
		o, err := domain.ParseYAML([]byte(yamlSource))
		if err != nil {
			return err
		}
		ont = o
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lines[line]
	if l == nil {
		l = &factsLine{}
		s.lines[line] = l
	}
	l.ont, l.source = ont, yamlSource
	l.version++
	return nil
}

// Snapshot returns a line's current facts. A line never written yields an
// empty snapshot, not an error: no facts is a valid state the gate judges
// under (firewall and redlines only). The ontology pointer is shared and
// must be treated as read-only, which every reader in pkg/domain does.
func (s *FactsStore) Snapshot(line string) FactsSnapshot {
	if line == "" {
		line = DefaultLine
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.lines[line]
	if l == nil {
		return FactsSnapshot{Line: line}
	}
	return FactsSnapshot{Line: line, Ontology: l.ont, Source: l.source, Version: l.version}
}

// Replace parses and validates the YAML and, only if both succeed, makes it
// the line's current facts and writes it to disk. An empty document clears
// the line's facts; it is written as such so the clearing survives a
// restart too.
func (s *FactsStore) Replace(line, yamlSource string) error {
	if line == "" {
		line = DefaultLine
	}
	if !ValidLine(line) {
		return fmt.Errorf("line name %q: use lowercase letters, digits, _ . - (max 64; no trailing dot, no reserved device names)", line)
	}
	if s.path != "" && !s.isDir && line != DefaultLine {
		return ErrSingleFile
	}
	// Parse before taking the lock or touching the disk.
	if strings.TrimSpace(yamlSource) != "" {
		if _, err := domain.ParseYAML([]byte(yamlSource)); err != nil {
			return err
		}
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.path != "" {
		if err := writeAtomic(s.fileFor(line), []byte(yamlSource)); err != nil {
			return err
		}
	}
	return s.load(line, yamlSource)
}

func (s *FactsStore) fileFor(line string) string {
	if s.isDir {
		return filepath.Join(s.path, line+".yaml")
	}
	return s.path
}

// Lines lists the lines that hold facts, sorted.
func (s *FactsStore) Lines() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.lines))
	for k, l := range s.lines {
		if l.ont != nil {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Path is the backing file or directory, empty for a memory store.
func (s *FactsStore) Path() string { return s.path }

// PerLine reports whether the store can hold more than the default line.
func (s *FactsStore) PerLine() bool { return s.path == "" || s.isDir }

// writeAtomic writes via a sibling temp file and rename, so a crash mid-write
// leaves the previous facts intact rather than a truncated file the next
// start would refuse.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".facts-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	// 先落盘再关闭：否则断电后 rename 过的文件可能是空的。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
