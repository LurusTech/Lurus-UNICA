package replay

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
)

// HoldoutPercent is the share of conversations reserved for validation. The
// miner never sees them; the gate is judged only on them.
const HoldoutPercent = 30

// Split names one side of the frozen partition.
type Split string

const (
	SplitHoldout Split = "holdout"
	SplitTrain   Split = "train"
	SplitAll     Split = "all"
)

// ParseSplit validates a -split flag value.
func ParseSplit(s string) (Split, error) {
	switch Split(s) {
	case SplitHoldout, SplitTrain, SplitAll:
		return Split(s), nil
	}
	return "", fmt.Errorf("split %q: want holdout, train or all", s)
}

// InHoldout reports whether a conversation belongs to the held-out side.
//
// The split is a function of the conversation ID alone — SHA-256, first eight
// bytes, modulo 10000 — so it is frozen by construction: the same conversation
// lands on the same side in every export, on every machine, forever, and a
// conversation exported later can never leak from holdout into training by a
// change of sampling. Splitting by conversation rather than by turn keeps a
// conversation's turns together, so a phrasing learned from one turn cannot
// be scored on its neighbour.
func InHoldout(conversationID string) bool {
	sum := sha256.Sum256([]byte(conversationID))
	return binary.BigEndian.Uint64(sum[:8])%10000 < HoldoutPercent*100
}

// Select keeps the cases on the requested side.
func Select(cases []Case, split Split) []Case {
	if split == SplitAll {
		return cases
	}
	want := split == SplitHoldout
	var out []Case
	for _, c := range cases {
		if InHoldout(c.ConversationID) == want {
			out = append(out, c)
		}
	}
	return out
}

// Manifest is the frozen list of conversation IDs a baseline was measured on.
// It is the only artefact of a real export that may be committed: IDs, never
// content. A later run restricted to the manifest measures the same set even
// after the export window has moved on.
type Manifest map[string]bool

// BuildManifest lists the conversations of cases on the given side.
func BuildManifest(convs []Conversation, split Split) Manifest {
	m := Manifest{}
	for _, c := range convs {
		if split == SplitAll || InHoldout(c.ConversationID) == (split == SplitHoldout) {
			m[c.ConversationID] = true
		}
	}
	return m
}

// WriteManifest writes one ID per line, sorted.
func WriteManifest(path string, m Manifest) error {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return os.WriteFile(path, []byte(strings.Join(ids, "\n")+"\n"), 0o644)
}

// ReadManifest reads a manifest; blank lines and # comments are ignored.
func ReadManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := Manifest{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m[line] = true
	}
	return m, sc.Err()
}

// Restrict keeps cases whose conversation is in the manifest, and reports how
// many manifest conversations contributed no case — a shrinking set is a
// finding in itself, not something to paper over.
func (m Manifest) Restrict(cases []Case) (kept []Case, missing int) {
	seen := map[string]bool{}
	for _, c := range cases {
		if m[c.ConversationID] {
			kept = append(kept, c)
			seen[c.ConversationID] = true
		}
	}
	return kept, len(m) - len(seen)
}
