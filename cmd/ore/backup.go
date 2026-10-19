// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// A backup is a directory (or an S3 prefix) holding:
//
//	manifest.json          format, version, dialect, repositories, files with SHA-256
//	ore.db                 a consistent VACUUM INTO copy (SQLite ledgers only)
//	ledger.jsonl           the portable dump (every dialect; what Postgres restores from)
//	repos/<org>/<name>.bundle   `git bundle --all` per repository
//	files/token, files/secrets.key, ...   the admin token and the secrets key
//
// `ore restore --from DIR` rebuilds a data directory from it, into an empty
// directory and an empty database only.

const backupFormat = "ore-backup/1"

var sidecarFiles = []string{"token", "secrets.key", "actions-refs.json"}

type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifestRepo struct {
	Org    string `json:"org"`
	Name   string `json:"name"`
	Bundle string `json:"bundle,omitempty"`
	Empty  bool   `json:"empty,omitempty"`
	Head   string `json:"head,omitempty"`
}

type manifest struct {
	Format     string         `json:"format"`
	OreVersion string         `json:"ore_version"`
	CreatedAt  string         `json:"created_at"`
	Dialect    string         `json:"dialect"`
	Database   []string       `json:"database"` // files that hold the ledger: ore.db and/or ledger.jsonl
	Repos      []manifestRepo `json:"repos"`
	Files      []manifestFile `json:"files"`
}

// runBackup implements `ore backup --to DIR|s3://bucket/prefix`.
func runBackup(args []string, w io.Writer) error {
	fl := parseFlags(args)
	cfg, err := loadConfig(fl)
	if err != nil {
		return err
	}
	dest := fl.str("to", "")
	if dest == "" {
		return errors.New("usage: ore backup --to DIR|s3://bucket/prefix [--data DIR] [--db URL]")
	}
	log := newLogger(cfg, os.Stderr)
	m, where, err := backup(context.Background(), cfg, dest, log)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "backup written to %s: %d repositories, %d files, ledger %s (%s)\n", where, len(m.Repos), len(m.Files), strings.Join(m.Database, "+"), m.Dialect)
	return nil
}

// backup writes a backup of cfg's data directory and database to dest and
// returns the manifest. For s3:// destinations it stages locally and uploads.
func backup(ctx context.Context, cfg config, dest string, log *slog.Logger) (manifest, string, error) {
	var m manifest
	s3t, isS3 := parseS3(dest)
	var stage string
	if isS3 {
		dir, err := os.MkdirTemp("", "ore-backup-*")
		if err != nil {
			return m, "", err
		}
		defer os.RemoveAll(dir)
		stage = dir
	} else {
		if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
			return m, "", fmt.Errorf("backup: destination %s is not empty; back up into a new directory", dest)
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return m, "", err
		}
		stage = dest
	}
	m = manifest{Format: backupFormat, OreVersion: version, CreatedAt: ledger.Now()}

	// the ledger: a consistent copy and the portable dump
	db, err := ledger.OpenWith(ctx, cfg.dbTarget(), ledger.Options{NoMigrate: true})
	if err != nil {
		return m, "", fmt.Errorf("backup: %w", err)
	}
	m.Dialect = db.Dialect()
	if db.Dialect() == ledger.DialectSQLite {
		if err := db.VacuumInto(ctx, filepath.Join(stage, "ore.db")); err != nil {
			db.Close()
			return m, "", fmt.Errorf("backup: sqlite copy: %w", err)
		}
		m.Database = append(m.Database, "ore.db")
	}
	f, err := os.Create(filepath.Join(stage, "ledger.jsonl"))
	if err != nil {
		db.Close()
		return m, "", err
	}
	if err := db.Dump(ctx, f); err != nil {
		f.Close()
		db.Close()
		return m, "", fmt.Errorf("backup: ledger dump: %w", err)
	}
	f.Close()
	db.Close()
	m.Database = append(m.Database, "ledger.jsonl")

	// repositories as bundles
	repos, err := filepath.Glob(filepath.Join(cfg.Data, "repos", "*", "*.git"))
	if err != nil {
		return m, "", err
	}
	sort.Strings(repos)
	for _, rp := range repos {
		org := filepath.Base(filepath.Dir(rp))
		name := strings.TrimSuffix(filepath.Base(rp), ".git")
		mr := manifestRepo{Org: org, Name: name}
		if _, err := os.ReadDir(rp); err != nil {
			return m, "", fmt.Errorf("backup: repository %s/%s is not readable: %w", org, name, err)
		}
		refs, err := gitx.Refs(ctx, rp)
		if err != nil {
			return m, "", fmt.Errorf("backup: repository %s/%s: %w", org, name, err)
		}
		if head, err := gitx.Run(ctx, rp, nil, "symbolic-ref", "HEAD"); err == nil {
			mr.Head = strings.TrimSpace(head)
		}
		if len(refs) == 0 {
			mr.Empty = true
		} else {
			rel := filepath.Join("repos", org, name+".bundle")
			if err := os.MkdirAll(filepath.Dir(filepath.Join(stage, rel)), 0o755); err != nil {
				return m, "", err
			}
			if _, err := gitx.Run(ctx, rp, nil, "bundle", "create", filepath.Join(stage, rel), "--all"); err != nil {
				return m, "", fmt.Errorf("backup: repository %s/%s: %w", org, name, err)
			}
			mr.Bundle = rel
		}
		m.Repos = append(m.Repos, mr)
	}

	// the token, the secrets key and other small sidecar files
	for _, name := range sidecarFiles {
		src := filepath.Join(cfg.Data, name)
		b, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Join(stage, "files"), 0o700); err != nil {
			return m, "", err
		}
		if err := os.WriteFile(filepath.Join(stage, "files", name), b, 0o600); err != nil {
			return m, "", err
		}
	}

	// the manifest with checksums
	if err := walkFiles(stage, func(rel string, data []byte) error {
		sum := sha256.Sum256(data)
		m.Files = append(m.Files, manifestFile{Path: rel, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
		return nil
	}); err != nil {
		return m, "", err
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), append(mb, '\n'), 0o644); err != nil {
		return m, "", err
	}
	if !isS3 {
		return m, dest, nil
	}
	client, err := newS3ClientFromEnv()
	if err != nil {
		return m, "", err
	}
	if err := client.uploadDir(ctx, s3t, stage, func(key string, size int) { log.Info("backup: upload", "key", key, "bytes", size) }); err != nil {
		return m, "", err
	}
	return m, dest, nil
}

// walkFiles calls fn with every regular file under dir (manifest.json excepted), relative paths with slashes.
func walkFiles(dir string, fn func(rel string, data []byte) error) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "manifest.json" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return fn(rel, data)
	})
}

// runRestore implements `ore restore --from DIR [--data DIR] [--db URL]`.
func runRestore(args []string, w io.Writer) error {
	fl := parseFlags(args)
	cfg, err := loadConfig(fl)
	if err != nil {
		return err
	}
	from := fl.str("from", "")
	if from == "" {
		return errors.New("usage: ore restore --from DIR [--data DIR] [--db URL]")
	}
	log := newLogger(cfg, os.Stderr)
	m, err := restore(context.Background(), cfg, from, log)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "restored %d repositories and the ledger (%s) from %s into %s\n", len(m.Repos), m.CreatedAt, from, cfg.Data)
	return nil
}

// ErrRestoreTargetNotEmpty is returned when the data directory already has content.
var errRestoreTargetNotEmpty = errors.New("refusing to restore into a non-empty data directory")

// restore rebuilds cfg.Data (and the database) from a backup directory.
func restore(ctx context.Context, cfg config, from string, log *slog.Logger) (manifest, error) {
	var m manifest
	if strings.HasSuffix(from, "manifest.json") {
		from = filepath.Dir(from)
	}
	mb, err := os.ReadFile(filepath.Join(from, "manifest.json"))
	if err != nil {
		return m, fmt.Errorf("restore: %w", err)
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		return m, fmt.Errorf("restore: manifest: %w", err)
	}
	if m.Format != backupFormat {
		return m, fmt.Errorf("restore: unknown backup format %q", m.Format)
	}
	// verify every checksum before touching anything
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(from, filepath.FromSlash(f.Path)))
		if err != nil {
			return m, fmt.Errorf("restore: %w", err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return m, fmt.Errorf("restore: checksum mismatch for %s", f.Path)
		}
	}
	if entries, err := os.ReadDir(cfg.Data); err == nil && len(entries) > 0 {
		return m, fmt.Errorf("%w: %s has %d entries", errRestoreTargetNotEmpty, cfg.Data, len(entries))
	}
	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return m, err
	}
	// sidecar files
	for _, name := range sidecarFiles {
		b, err := os.ReadFile(filepath.Join(from, "files", name))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(cfg.Data, name), b, 0o600); err != nil {
			return m, err
		}
	}
	// the ledger: copy the SQLite file when both sides are SQLite, else load the portable dump
	target := cfg.dbTarget()
	useCopy := cfg.dbDialect() == ledger.DialectSQLite && contains(m.Database, "ore.db")
	if useCopy {
		dst := ledger.SQLitePath(target)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return m, err
		}
		if err := copyFile(filepath.Join(from, "ore.db"), dst); err != nil {
			return m, fmt.Errorf("restore: ledger: %w", err)
		}
	}
	db, err := ledger.Open(ctx, target) // applies any migrations newer than the backup
	if err != nil {
		return m, fmt.Errorf("restore: %w", err)
	}
	if !useCopy {
		f, err := os.Open(filepath.Join(from, "ledger.jsonl"))
		if err != nil {
			db.Close()
			return m, fmt.Errorf("restore: %w", err)
		}
		err = db.Load(ctx, f)
		f.Close()
		if err != nil {
			db.Close()
			return m, fmt.Errorf("restore: ledger: %w", err)
		}
	}
	// repositories
	for _, r := range m.Repos {
		rp := filepath.Join(cfg.Data, "repos", r.Org, r.Name+".git")
		if err := os.MkdirAll(filepath.Dir(rp), 0o755); err != nil {
			db.Close()
			return m, err
		}
		branch := "main"
		if repo, err := db.RepoByName(ctx, r.Org, r.Name); err == nil {
			branch = repo.DefaultBranch
		}
		if r.Empty || r.Bundle == "" {
			if err := gitx.InitBare(ctx, rp, branch); err != nil {
				db.Close()
				return m, fmt.Errorf("restore: repository %s/%s: %w", r.Org, r.Name, err)
			}
			continue
		}
		bundle := filepath.Join(from, filepath.FromSlash(r.Bundle))
		cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--bare", bundle, rp)
		if out, err := cmd.CombinedOutput(); err != nil {
			db.Close()
			return m, fmt.Errorf("restore: repository %s/%s: %v: %s", r.Org, r.Name, err, strings.TrimSpace(string(out)))
		}
		_, _ = gitx.Run(ctx, rp, nil, "remote", "remove", "origin")
		head := r.Head
		if head == "" {
			head = "refs/heads/" + branch
		}
		_, _ = gitx.Run(ctx, rp, nil, "symbolic-ref", "HEAD", head)
	}
	db.Close()
	log.Info("restore complete", "from", from, "data", cfg.Data, "repos", len(m.Repos), "database", target)
	return m, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---- scheduled backups ----

var backupDirRE = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

// backupWorker writes a backup every cfg.BackupInterval into
// cfg.BackupTo/<timestamp> and keeps the newest cfg.BackupKeep local ones.
func (s *server) backupWorker(ctx context.Context) {
	iv := s.cfg.BackupInterval
	if iv <= 0 {
		iv = 24 * time.Hour
	}
	s.log.Info("scheduled backups on", "to", s.cfg.BackupTo, "interval", iv.String(), "keep", s.cfg.BackupKeep)
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.backupOnce(ctx)
		}
	}
}

// backupOnce runs one scheduled backup and prunes old local ones.
func (s *server) backupOnce(ctx context.Context) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	dest := strings.TrimRight(s.cfg.BackupTo, "/") + "/" + stamp
	if _, isS3 := parseS3(s.cfg.BackupTo); !isS3 {
		dest = filepath.Join(s.cfg.BackupTo, stamp)
	}
	start := time.Now()
	m, where, err := backup(ctx, s.cfg, dest, s.log)
	if err != nil {
		s.log.Error("scheduled backup failed", "to", dest, "err", err)
		return
	}
	s.log.Info("scheduled backup written", "to", where, "repos", len(m.Repos), "files", len(m.Files), "duration_ms", time.Since(start).Milliseconds())
	if _, isS3 := parseS3(s.cfg.BackupTo); isS3 || s.cfg.BackupKeep <= 0 {
		return
	}
	pruned, err := pruneBackups(s.cfg.BackupTo, s.cfg.BackupKeep)
	if err != nil {
		s.log.Warn("backup retention", "err", err)
	}
	for _, p := range pruned {
		s.log.Info("backup pruned", "dir", p)
	}
}

// pruneBackups removes timestamped backup directories beyond the newest keep.
func pruneBackups(root string, keep int) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var stamps []string
	for _, e := range entries {
		if e.IsDir() && backupDirRE.MatchString(e.Name()) {
			stamps = append(stamps, e.Name())
		}
	}
	sort.Strings(stamps)
	var pruned []string
	for len(stamps) > keep {
		p := filepath.Join(root, stamps[0])
		if err := os.RemoveAll(p); err != nil {
			return pruned, err
		}
		pruned = append(pruned, p)
		stamps = stamps[1:]
	}
	return pruned, nil
}
