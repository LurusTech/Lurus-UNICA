package gate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kefu/unica/pkg/compliance"
)

// EvidenceStore keeps every verdict as a row of the same hash chain the
// router's answer_audit table uses (pkg/compliance), one chain per product
// line. A row's hash covers its content and the previous row's hash, so a
// verdict edited or removed after the fact stops the chain verifying from
// that point on.
//
// Rows live in memory and, when a directory is given, are appended to
// <dir>/<line>.jsonl as they are sealed. On start the files are read back
// so the chain continues from where it stopped rather than from genesis
// — a restart that reset seq to 1 would make every saved head digest
// unverifiable.
type EvidenceStore struct {
	dir string

	mu    sync.Mutex
	lines map[string]*chain
}

type chain struct {
	rows []compliance.AuditEntry
	head string
	file *os.File
	// broken 表示追加失败后回滚也失败，文件状态未知；
	// 此后拒绝追加，直到进程重启重新校验。
	broken bool
}

// ErrNotFound is returned for an evidence id no chain holds.
var ErrNotFound = errors.New("evidence not found")

// NewEvidenceStore creates a store; dir may be empty for memory only.
func NewEvidenceStore(dir string) *EvidenceStore {
	return &EvidenceStore{dir: dir, lines: map[string]*chain{}}
}

// Open creates a store and reads back whatever <dir> already holds. A file
// that does not verify is an error: continuing a chain that is already
// broken would seal new rows onto a lie.
func Open(dir string) (*EvidenceStore, error) {
	s := NewEvidenceStore(dir)
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		line := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		rows, err := readRows(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if res := compliance.Verify(rows); !res.OK {
			return nil, fmt.Errorf("%s: evidence chain does not verify at seq %d: %s", path, res.FirstBadSeq, res.Problem)
		}
		c := &chain{rows: rows, head: compliance.GenesisHash(line)}
		if len(rows) > 0 {
			c.head = rows[len(rows)-1].RowHash
		}
		s.lines[line] = c
	}
	return s, nil
}

func readRows(path string) ([]compliance.AuditEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []compliance.AuditEntry
	good := 0 // 最后一个完整行之后的字节偏移
	pos := 0
	for n := 1; pos < len(data); n++ {
		end := bytes.IndexByte(data[pos:], '\n')
		last := end < 0 // 没有换行结尾的最后一段
		var raw []byte
		next := len(data)
		if last {
			raw = data[pos:]
		} else {
			raw = data[pos : pos+end]
			next = pos + end + 1
		}
		text := strings.TrimSpace(string(raw))
		if text != "" {
			var e compliance.AuditEntry
			if err := json.Unmarshal([]byte(text), &e); err != nil {
				if last {
					// 崩溃留下的尾部半行：丢弃并截断，其前的链照常校验。
					log.Printf("gate: evidence %s: discarding torn tail line %d (%d bytes): %v", path, n, len(raw), err)
					if terr := os.Truncate(path, int64(good)); terr != nil {
						return nil, fmt.Errorf("truncate torn tail: %w", terr)
					}
					return rows, nil
				}
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			rows = append(rows, e)
		}
		pos = next
		if !last {
			good = next
		} else {
			good = len(data)
		}
	}
	return rows, nil
}

// Append seals the entry onto its line's chain and returns its id.
func (s *EvidenceStore) Append(e *compliance.AuditEntry) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.chainFor(e.ProductLineID)
	if err != nil {
		return "", err
	}
	e.Seq = int64(len(c.rows)) + 1
	e.Seal(c.head)
	if c.file != nil {
		b, err := json.Marshal(e)
		if err != nil {
			return "", err
		}
		// 记下写前偏移；写或同步失败就截回去，不留半行或重复 seq。
		info, err := c.file.Stat()
		if err != nil {
			return "", err
		}
		off := info.Size()
		_, werr := c.file.Write(append(b, '\n'))
		if werr == nil {
			werr = c.file.Sync()
		}
		if werr != nil {
			if terr := c.file.Truncate(off); terr != nil {
				c.file.Close()
				c.file = nil
				c.broken = true
				return "", fmt.Errorf("append failed (%v) and rollback failed: %w", werr, terr)
			}
			return "", werr
		}
	}
	c.rows = append(c.rows, *e)
	c.head = e.RowHash
	return ID(e.ProductLineID, e.Seq), nil
}

func (s *EvidenceStore) chainFor(line string) (*chain, error) {
	c := s.lines[line]
	if c == nil {
		c = &chain{head: compliance.GenesisHash(line)}
		s.lines[line] = c
	}
	if c.broken {
		return nil, fmt.Errorf("evidence chain %q is broken after a failed rollback; restart the gate", line)
	}
	if s.dir != "" && c.file == nil {
		f, err := os.OpenFile(filepath.Join(s.dir, line+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		c.file = f
	}
	return c, nil
}

// ID renders an evidence id: the line and the seq, which together name one
// row of one chain.
func ID(line string, seq int64) string { return line + "/" + strconv.FormatInt(seq, 10) }

// ParseID splits an evidence id.
func ParseID(id string) (line string, seq int64, err error) {
	i := strings.LastIndex(id, "/")
	if i <= 0 {
		return "", 0, fmt.Errorf("bad evidence id %q", id)
	}
	seq, err = strconv.ParseInt(id[i+1:], 10, 64)
	if err != nil || seq < 1 {
		return "", 0, fmt.Errorf("bad evidence id %q", id)
	}
	return id[:i], seq, nil
}

// Get returns one row.
func (s *EvidenceStore) Get(line string, seq int64) (compliance.AuditEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.lines[line]
	if c == nil || seq < 1 || int(seq) > len(c.rows) {
		return compliance.AuditEntry{}, ErrNotFound
	}
	return c.rows[seq-1], nil
}

// Verify re-checks a whole chain and reports its head.
func (s *EvidenceStore) Verify(line string) compliance.VerifyResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.lines[line]
	if c == nil {
		return compliance.VerifyResult{OK: true}
	}
	return compliance.Verify(c.rows)
}

// Head renders the external anchor of a chain: one line a customer saves
// outside the gate so a rewritten chain can be caught against it.
func (s *EvidenceStore) Head(line string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.lines[line]
	if c == nil || len(c.rows) == 0 {
		return "", false
	}
	last := c.rows[len(c.rows)-1]
	return compliance.HeadDigest(line, last.Seq, last.RowHash), true
}

// Close releases the append files.
func (s *EvidenceStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, c := range s.lines {
		if c.file != nil {
			if err := c.file.Close(); err != nil && first == nil {
				first = err
			}
			c.file = nil
		}
	}
	return first
}

// replayInto rebuilds the gate's shadow counters from rows read back at
// start, so a restart does not reset "this month the gate would have held N"
// to zero. Only what the row records is counted; a row cannot be re-judged
// because the facts it was judged under may since have changed.
func (s *EvidenceStore) replayInto(g *Gate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for line, c := range s.lines {
		for _, e := range c.rows {
			g.recordFromRow(line, e)
		}
	}
}

// recordFromRow is record for a row read from disk.
func (g *Gate) recordFromRow(line string, e compliance.AuditEntry) {
	var rec struct {
		Findings      []struct{ Kind string } `json:"findings"`
		WouldWithhold bool                    `json:"would_withhold"`
		Speaker       string                  `json:"speaker"`
	}
	_ = json.Unmarshal(e.Commitments, &rec)
	var viol []struct{ Kind string }
	_ = json.Unmarshal(e.Violations, &viol)

	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stats[line]
	if st == nil {
		st = &LineStats{ProductLine: line, ByKind: map[string]int{}}
		g.stats[line] = st
	}
	st.Judged++
	if rec.Speaker == SpeakerHuman {
		st.Human++
	} else {
		st.AI++
	}
	switch {
	case e.Enforced:
		st.Withheld++
	case rec.WouldWithhold:
		st.WouldWithhold++
	case len(rec.Findings) > 0 || len(viol) > 0:
		st.Flagged++
	}
	for _, v := range viol {
		st.ByKind[v.Kind]++
	}
	for _, f := range rec.Findings {
		if f.Kind == "ungrounded_figure" {
			continue
		}
		st.ByKind[f.Kind]++
	}
}
