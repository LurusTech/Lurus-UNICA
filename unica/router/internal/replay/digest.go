package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// OntologyDigest is the hex sha256 of an ontology's compiled JSON, compacted
// first. evolve enqueue compares the digest propose computed on the fresh
// compile with the one the gate recomputes from a run file, and a run file is
// written indented: without compacting, the same ontology would hash two ways
// and every honest verdict would be refused.
func OntologyDigest(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		// Not JSON at all; hash what was given, which matches nothing real.
		return sha256Hex(raw)
	}
	return sha256Hex(buf.Bytes())
}

// sha256Hex hex-encodes the sha256 of data. Shared by OntologyDigest and the
// run's configuration fingerprint (Fingerprint, in run.go), which is the same
// "does this JSON match byte for byte" question asked of guardrail config
// instead of an ontology.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
