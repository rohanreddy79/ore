# Forge Provider Interface (FPI) v0 sketch

The seam between the Ore platform and whatever stores repositories. Four implementations are planned: Forgejo headless (Phase 0), GitHub adapter (Phase 0/1), GitLab adapter (Phase 2), native object-storage core (Phase 3). The Go interface below is the in-process form; the same surface is exposed as a Connect-RPC service for out-of-process providers (the native core in Rust).

```go
package fpi

type Provider interface {
    Repos
    Refs
    Objects
    Principals
    Collaboration
    Packages
    Migrations
    Hooks
    Capabilities() Capabilities // what this provider can do natively vs. what Ore emulates
}

type Repos interface {
    Create(ctx, org ID, spec RepoSpec) (Repo, error)
    Get(ctx, id ID) (Repo, error)
    Fork(ctx, id ID, into ID) (Repo, error)
    Mirror(ctx, id ID, remote Remote) error
    Archive(ctx, id ID) error
    Settings(ctx, id ID, patch RepoSettings) error
}

type Refs interface {
    List(ctx, repo ID, prefix string) ([]Ref, error)
    Update(ctx, repo ID, name string, old, new OID, opts UpdateOpts) error // compare-and-swap; policy context attached
    Delete(ctx, repo ID, name string, old OID) error
}

type Objects interface {
    Blob(ctx, repo ID, oid OID) (io.ReadCloser, error)
    Tree(ctx, repo ID, oid OID) (Tree, error)
    Commit(ctx, repo ID, oid OID) (Commit, error)
    Log(ctx, repo ID, q LogQuery) (Iterator[Commit], error)
    Diff(ctx, repo ID, a, b OID, opts DiffOpts) (Diff, error)
    RangeDiff(ctx, repo ID, r1, r2 Range) (RangeDiff, error) // interdiff between patch sets
    Blame(ctx, repo ID, oid OID, path string) (Blame, error)
    MergeBase(ctx, repo ID, a, b OID) (OID, error)
    Merge(ctx, repo ID, into, from OID, strategy MergeStrategy) (MergeResult, error) // used by the merge queue
    CherryPick(ctx, repo ID, commit, onto OID) (OID, error)
    ObjectFormat(ctx, repo ID) (Hash, error) // sha1 | sha256
}

type Principals interface { // read-through; Ore is the source of truth and provisions down
    EnsureUser(ctx, u User) error
    EnsureOrg(ctx, o Org) error
    SetPermission(ctx, repo ID, principal ID, level Permission) error
}

type Collaboration interface { // read-through for issues and PRs; Ore's Changes own review state
    Issues(ctx, repo ID, q IssueQuery) (Iterator[Issue], error)
    PullRequests(ctx, repo ID, q PRQuery) (Iterator[PullRequest], error)
    CreateIssue(ctx, repo ID, i IssueSpec) (Issue, error)
    Comment(ctx, target ID, body string, actor Actor) (Comment, error)
}

type Packages interface {
    Formats() []string // Forgejo: 20+; native core: OCI first
    Publish(ctx, repo ID, pkg PackageSpec, r io.Reader) (Package, error)
}

type Migrations interface {
    Plan(ctx, src Source) (FidelityReport, error)
    Run(ctx, src Source, into ID, opts MigrateOpts) (Iterator[Progress], error)
    Verify(ctx, src Source, repo ID) (VerifyReport, error)
}

type Hooks interface {
    // Ore registers these; the provider must call them in-band for pre-receive
    // (policy, secret scanning, size limits) and out-of-band for post-receive (ledger).
    PreReceive(fn func(ctx, PushContext) Decision)
    PostReceive(fn func(ctx, PushResult))
}
```

Capabilities a provider may lack are emulated by Ore where feasible (e.g. range-diff on the GitHub adapter is computed from mirrored objects; reftable and SHA-256 are native-core only) and reported in the UI otherwise.
