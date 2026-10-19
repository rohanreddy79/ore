// SPDX-License-Identifier: AGPL-3.0-only

package workspace

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// Queue is .ore/pending.json: saves made while the server was unreachable,
// in order. Each entry is a complete commit request; the manifest already
// reflects it, so later edits diff against the queued content.
type Queue struct {
	Saves []PendingSave `json:"saves"`
}

// PendingSave is one queued save.
type PendingSave struct {
	Message string `json:"message"`
	Branch  string `json:"branch"`
	// Base is the compare-and-swap base of the first queued save (the
	// branch's start when it is new); later entries build on the previous
	// queued commit, whose id is unknown until it is sent.
	Base     string     `json:"base,omitempty"`
	Merge    string     `json:"merge,omitempty"`
	Files    []FileEdit `json:"files"`
	QueuedAt string     `json:"queued_at"`
}

// FileEdit is one file of a commit request, as the API takes it.
type FileEdit struct {
	Path     string `json:"path"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Delete   bool   `json:"delete,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

// FileEdits converts scanned edits into the API's shape; content that is not
// valid UTF-8 travels as base64.
func FileEdits(edits []Edit) []FileEdit {
	out := make([]FileEdit, 0, len(edits))
	for _, e := range edits {
		f := FileEdit{Path: e.Path, Mode: e.Mode}
		if e.Kind == "deleted" {
			f.Delete = true
			f.Mode = ""
		} else if utf8.Valid(e.Content) {
			f.Content = string(e.Content)
		} else {
			f.Content = base64.StdEncoding.EncodeToString(e.Content)
			f.Encoding = "base64"
		}
		out = append(out, f)
	}
	return out
}

// LoadQueue reads the pending saves (none when the file is absent).
func LoadQueue(root string) (*Queue, error) {
	b, err := os.ReadFile(filepath.Join(root, PendingFile))
	if os.IsNotExist(err) {
		return &Queue{}, nil
	}
	if err != nil {
		return nil, err
	}
	var q Queue
	if err := json.Unmarshal(b, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

// Save writes the queue, removing the file when it is empty.
func (q *Queue) Save(root string) error {
	p := filepath.Join(root, PendingFile)
	if len(q.Saves) == 0 {
		err := os.Remove(p)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Files counts the files across the queued saves.
func (q *Queue) Files() int {
	n := 0
	for _, s := range q.Saves {
		n += len(s.Files)
	}
	return n
}
