// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/orevcs/ore/internal/ledger"
)

// ArtifactStore keeps run artifacts as zip files under <Dir>/<run id>/ with
// a row per artifact in the ledger (io.ore.artifact.published).
type ArtifactStore struct {
	Dir string
	DB  *ledger.DB
}

// Artifact is one uploaded artifact.
type Artifact struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	RepoID    string `json:"repo_id"`
	RunID     string `json:"run_id"`
	TaskID    string `json:"task_id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Files     int    `json:"files"`
	Digest    string `json:"digest"`
	Path      string `json:"-"`
	CreatedAt string `json:"created_at"`
}

// ErrArtifactExists is returned when a run already has an artifact by that name.
var ErrArtifactExists = errors.New("artifact already exists")

// ValidArtifactName refuses names that could escape the store or are
// otherwise not usable as a file name: path separators, "..", control
// characters, a leading dot, or more than 128 characters.
func ValidArtifactName(name string) error {
	if name == "" {
		return errors.New("artifact name is empty")
	}
	if len(name) > 128 {
		return errors.New("artifact name is longer than 128 characters")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("artifact name %q must not start with a dot", name)
	}
	for _, c := range name {
		switch {
		case c == '/' || c == '\\' || c == ':' || c == '*' || c == '?' || c == '"' || c == '<' || c == '>' || c == '|':
			return fmt.Errorf("artifact name %q contains %q", name, c)
		case c < 0x20 || c == 0x7f:
			return fmt.Errorf("artifact name %q contains a control character", name)
		}
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("artifact name %q contains '..'", name)
	}
	return nil
}

// Put zips files (relative to root, slash-separated) into an artifact named
// name for the run. Entries are stored relative to the least common
// ancestor directory of the files, like upload-artifact does, so a single
// directory upload round-trips to its contents.
func (a *ArtifactStore) Put(ctx context.Context, repo ledger.Repo, runID, taskID, name, root string, files []string, overwrite bool, actor ledger.Actor) (Artifact, error) {
	if err := ValidArtifactName(name); err != nil {
		return Artifact{}, err
	}
	if !safeSegment(runID) {
		return Artifact{}, fmt.Errorf("artifact: bad run id %q", runID)
	}
	if len(files) == 0 {
		return Artifact{}, errors.New("artifact has no files")
	}
	for _, f := range files {
		if path.IsAbs(f) || f == ".." || strings.HasPrefix(f, "../") || strings.Contains(f, "/../") {
			return Artifact{}, fmt.Errorf("artifact path %q escapes the workspace", f)
		}
	}
	base := commonDir(files)
	dir := filepath.Join(a.Dir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Artifact{}, err
	}
	if existing, err := a.Get(ctx, runID, name); err == nil {
		if !overwrite {
			return Artifact{}, fmt.Errorf("%w: %s", ErrArtifactExists, name)
		}
		if _, err := a.DB.Exec(ctx, `DELETE FROM artifacts WHERE id = ?`, existing.ID); err != nil {
			return Artifact{}, err
		}
		os.Remove(existing.Path)
	}
	art := Artifact{ID: ledger.NewID("art"), OrgID: repo.OrgID, RepoID: repo.ID, RunID: runID, TaskID: taskID, Name: name, Files: len(files), CreatedAt: ledger.Now()}
	art.Path = filepath.Join(dir, art.ID+".zip")
	f, err := os.Create(art.Path)
	if err != nil {
		return Artifact{}, err
	}
	h := sha256.New()
	zw := zip.NewWriter(io.MultiWriter(f, h))
	for _, rel := range files {
		src := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(src)
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(art.Path)
			return Artifact{}, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = strings.TrimPrefix(strings.TrimPrefix(rel, base), "/")
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err == nil {
			var in *os.File
			in, err = os.Open(src)
			if err == nil {
				_, err = io.Copy(w, in)
				in.Close()
			}
		}
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(art.Path)
			return Artifact{}, err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return Artifact{}, err
	}
	if err := f.Close(); err != nil {
		return Artifact{}, err
	}
	st, _ := os.Stat(art.Path)
	art.Size = st.Size()
	art.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return Artifact{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id, org_id, repo_id, run_id, task_id, name, size, files, digest, path, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		art.ID, art.OrgID, art.RepoID, art.RunID, art.TaskID, art.Name, art.Size, art.Files, art.Digest, art.Path, art.CreatedAt); err != nil {
		os.Remove(art.Path)
		return Artifact{}, err
	}
	if _, err := tx.Emit(repo.OrgID, "io.ore.artifact.published", repo.Org+"/"+repo.Name+"/runs/"+runID+"/artifacts/"+name, actor, art); err != nil {
		return Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		os.Remove(art.Path)
		return Artifact{}, err
	}
	return art, nil
}

// commonDir returns the deepest directory shared by all paths ("" for the root).
func commonDir(files []string) string {
	var common []string
	for i, f := range files {
		d := path.Dir(f)
		var segs []string
		if d != "." {
			segs = strings.Split(d, "/")
		}
		if i == 0 {
			common = segs
			continue
		}
		n := 0
		for n < len(common) && n < len(segs) && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	return strings.Join(common, "/")
}

const artifactCols = `id, org_id, repo_id, run_id, task_id, name, size, files, digest, path, created_at`

func scanArtifact(s interface{ Scan(...any) error }) (Artifact, error) {
	var a Artifact
	err := s.Scan(&a.ID, &a.OrgID, &a.RepoID, &a.RunID, &a.TaskID, &a.Name, &a.Size, &a.Files, &a.Digest, &a.Path, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ledger.ErrNotFound
	}
	return a, err
}

// Get finds an artifact of a run by name.
func (a *ArtifactStore) Get(ctx context.Context, runID, name string) (Artifact, error) {
	return scanArtifact(a.DB.QueryRow(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE run_id = ? AND name = ?`, runID, name))
}

// List returns a run's artifacts in upload order.
func (a *ArtifactStore) List(ctx context.Context, runID string) ([]Artifact, error) {
	rows, err := a.DB.Query(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE run_id = ? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, art)
	}
	return out, rows.Err()
}

// Extract unzips an artifact into dest, refusing entries that would escape it.
func (a *ArtifactStore) Extract(art Artifact, dest string) (int, error) {
	zr, err := zip.OpenReader(art.Path)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, zf := range zr.File {
		name := path.Clean("/" + zf.Name)
		if name == "/" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if zf.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return n, err
		}
		rc, err := zf.Open()
		if err != nil {
			return n, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode().Perm()|0o600)
		if err != nil {
			rc.Close()
			return n, err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
