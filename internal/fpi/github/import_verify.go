// SPDX-License-Identifier: AGPL-3.0-only

package github

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/orevcs/ore/internal/gitx"
	"github.com/orevcs/ore/internal/ledger"
)

// VerifySection is the result for one object kind.
type VerifySection struct {
	Checked    int      `json:"checked"`
	Matched    int      `json:"matched"`
	Mismatched []string `json:"mismatched"`
	Missing    []string `json:"missing"`
}

func (s *VerifySection) ok() bool { return len(s.Mismatched) == 0 && len(s.Missing) == 0 }

// VerifyReport re-checks the import against GitHub. It is signed with the
// server's ed25519 import key so the ledger entry can be verified later.
type VerifyReport struct {
	JobID      string        `json:"job_id"`
	Source     string        `json:"source"`
	VerifiedAt string        `json:"verified_at"`
	Refs       VerifySection `json:"refs"`
	Issues     VerifySection `json:"issues"`
	Releases   VerifySection `json:"releases"`
	OK         bool          `json:"ok"`
	SHA256     string        `json:"sha256,omitempty"`
	PublicKey  string        `json:"public_key,omitempty"`
	Signature  string        `json:"signature,omitempty"`
}

// signingKey loads or creates the import key under the data directory.
func (svc *Service) signingKey() (ed25519.PrivateKey, error) {
	path := filepath.Join(svc.DataDir, "import-signing.key")
	if b, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err == nil && len(seed) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(seed), nil
		}
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(svc.DataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// canonical serializes the report without its signature fields.
func (v VerifyReport) canonical() []byte {
	v.SHA256, v.Signature, v.PublicKey = "", "", ""
	b, _ := json.Marshal(v)
	return b
}

// VerifySignature checks a report's signature against its public key.
func (v VerifyReport) VerifySignature() bool {
	pub, err := hex.DecodeString(v.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(v.Signature)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(v.canonical())
	return hex.EncodeToString(sum[:]) == v.SHA256 && ed25519.Verify(ed25519.PublicKey(pub), sum[:], sig)
}

// Verify re-checks every ref, issue and release of a job against GitHub and
// records the signed report on the ledger as io.ore.import.verified.
func (svc *Service) Verify(ctx context.Context, repo ledger.Repo, job ImportJob) (VerifyReport, error) {
	if svc.Client == nil {
		return VerifyReport{}, ErrNoToken
	}
	rep := VerifyReport{JobID: job.ID, Source: job.Source, VerifiedAt: ledger.Now()}
	for _, s := range []*VerifySection{&rep.Refs, &rep.Issues, &rep.Releases} {
		s.Mismatched, s.Missing = []string{}, []string{}
	}
	link, err := svc.store.linkByRepo(ctx, repo.ID)
	if err != nil {
		return rep, fmt.Errorf("repository is not linked: %w", err)
	}
	rp := svc.repoPath(repo)
	// Refs: ls-remote against the local refs.
	env, err := svc.Client.gitEnv(link.CloneURL)
	if err != nil {
		return rep, err
	}
	out, err := gitx.Run(ctx, "", env, "ls-remote", "--heads", "--tags", link.CloneURL)
	if err != nil {
		return rep, fmt.Errorf("ls-remote: %w", err)
	}
	local, err := gitx.Refs(ctx, rp)
	if err != nil {
		return rep, err
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || strings.HasSuffix(f[1], "^{}") {
			continue
		}
		rep.Refs.Checked++
		switch got, ok := local[f[1]]; {
		case !ok:
			rep.Refs.Missing = append(rep.Refs.Missing, f[1])
		case got != f[0]:
			rep.Refs.Mismatched = append(rep.Refs.Mismatched, fmt.Sprintf("%s: github %s, ore %s", f[1], shortSHA(f[0]), shortSHA(got)))
		default:
			rep.Refs.Matched++
		}
	}
	// Issues: every GitHub issue has a mapping and the same title and state.
	for page := 1; page < 100000; page++ {
		var items []ghIssueFull
		if _, err := svc.Client.getPage(ctx, repoPath(job.Owner, job.Name)+"/issues?state=all&sort=created&direction=asc", page, &items); err != nil {
			return rep, err
		}
		for _, gi := range items {
			if gi.PullRequest != nil {
				continue
			}
			rep.Issues.Checked++
			m, ok, err := svc.store.mapping(ctx, repo.ID, "issue", fmt.Sprint(gi.Number))
			if err != nil {
				return rep, err
			}
			if !ok {
				rep.Issues.Missing = append(rep.Issues.Missing, "#"+strconv.Itoa(gi.Number))
				continue
			}
			issue, err := svc.DB.Issue(ctx, repo, m.TargetNumber)
			if err != nil {
				rep.Issues.Missing = append(rep.Issues.Missing, fmt.Sprintf("#%d (ore #%d)", gi.Number, m.TargetNumber))
				continue
			}
			state := gi.State
			if state != "closed" {
				state = "open"
			}
			if issue.Title != gi.Title || issue.State != state {
				rep.Issues.Mismatched = append(rep.Issues.Mismatched, fmt.Sprintf("#%d: github %q %s, ore %q %s", gi.Number, gi.Title, state, issue.Title, issue.State))
				continue
			}
			rep.Issues.Matched++
		}
		if len(items) < perPage {
			break
		}
	}
	// Releases: recorded, tag mirrored, assets on disk with the recorded size.
	rels, err := listAll[ghRelease](ctx, svc.Client, repoPath(job.Owner, job.Name)+"/releases")
	if err != nil {
		return rep, err
	}
	imported, err := svc.store.releases(ctx, repo.ID)
	if err != nil {
		return rep, err
	}
	byID := map[int64]ImportedRelease{}
	for _, r := range imported {
		byID[r.GitHubID] = r
	}
	for _, gr := range rels {
		rep.Releases.Checked++
		ir, ok := byID[gr.ID]
		if !ok {
			rep.Releases.Missing = append(rep.Releases.Missing, gr.TagName)
			continue
		}
		var problems []string
		if _, ok := local["refs/tags/"+gr.TagName]; !ok && !gr.Draft {
			problems = append(problems, "tag not mirrored")
		}
		assets := map[int64]ImportedAsset{}
		for _, a := range ir.Assets {
			assets[a.GitHubID] = a
		}
		for _, ga := range gr.Assets {
			a, ok := assets[ga.ID]
			switch {
			case !ok || a.Status != "ok":
				problems = append(problems, "asset "+ga.Name+" not downloaded")
			default:
				st, err := os.Stat(a.Path)
				if err != nil || st.Size() != ga.Size {
					problems = append(problems, "asset "+ga.Name+" size differs or file missing")
				}
			}
		}
		if len(problems) > 0 {
			rep.Releases.Mismatched = append(rep.Releases.Mismatched, gr.TagName+": "+strings.Join(problems, "; "))
			continue
		}
		rep.Releases.Matched++
	}
	rep.OK = rep.Refs.ok() && rep.Issues.ok() && rep.Releases.ok()
	key, err := svc.signingKey()
	if err != nil {
		return rep, err
	}
	sum := sha256.Sum256(rep.canonical())
	rep.SHA256 = hex.EncodeToString(sum[:])
	rep.PublicKey = hex.EncodeToString(key.Public().(ed25519.PublicKey))
	rep.Signature = hex.EncodeToString(ed25519.Sign(key, sum[:]))
	job.Verification = &rep
	if err := svc.store.saveJob(ctx, repo, &job, "io.ore.import.verified", map[string]any{"report": rep, "ok": rep.OK, "sha256": rep.SHA256, "signature": rep.Signature, "public_key": rep.PublicKey}); err != nil {
		return rep, err
	}
	return rep, nil
}
