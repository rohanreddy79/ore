// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ContentHash computes the task's content hash: SHA-256 over the canonical
// JSON of its resolved inputs, steps, executor and matrix coordinates
// (docs/04 A.2). Secrets contribute by path only, never by value, so a
// rotated secret does not change the hash and a hash never leaks one.
// Identical hashes within one trust scope are cache hits.
func ContentHash(t *IRTask) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	// encoding/json sorts map keys, so the encoding is canonical for equal
	// values regardless of construction order.
	_ = enc.Encode(struct {
		Inputs    Inputs         `json:"inputs"`
		Steps     []Step         `json:"steps"`
		Executor  Executor       `json:"executor"`
		MatrixKey map[string]any `json:"matrix_key,omitempty"`
	}{t.Inputs, t.Steps, t.Executor, t.MatrixKey})
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Hash computes and stores ContentHash on every task of the IR.
func (ir *IR) Hash() {
	for i := range ir.Tasks {
		ir.Tasks[i].ContentHash = ContentHash(&ir.Tasks[i])
	}
}
