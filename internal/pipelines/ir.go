// SPDX-License-Identifier: AGPL-3.0-only

package pipelines

// This file holds the Go form of the Pipeline IR (spec/pipeline-ir/
// pipeline-ir.schema.json): a typed, content-addressed DAG of tasks that
// authoring front-ends (the Actions YAML compiler in internal/pipelines/
// actions, later GitLab CI and the SDKs) emit and the control plane plans,
// caches and runs. Field names and JSON keys follow the schema exactly.

// IRVersion is the IR schema version this binary emits.
const IRVersion = "0"

// Trust scopes, from most to least trusted. A task may read caches from its
// own scope and the scopes above it, and write only to its own scope
// (docs/04 A.4, A.5).
const (
	TrustDefault   = "default"
	TrustProtected = "protected"
	TrustInternal  = "internal"
	TrustFork      = "fork"
)

// TrustRank orders scopes: lower is more trusted. Unknown scopes rank as fork.
func TrustRank(scope string) int {
	switch scope {
	case TrustDefault:
		return 0
	case TrustProtected:
		return 1
	case TrustInternal:
		return 2
	default:
		return 3
	}
}

// CanReadScope reports whether a task running at scope reader may read a
// cache entry written at scope entry.
func CanReadScope(reader, entry string) bool { return TrustRank(entry) <= TrustRank(reader) }

// IR is one compiled pipeline.
type IR struct {
	Version       string                  `json:"version"`
	ID            string                  `json:"id"`
	Repository    RepositoryRef           `json:"repository"`
	Trigger       Trigger                 `json:"trigger"`
	Concurrency   *Concurrency            `json:"concurrency,omitempty"`
	PolicyContext *PolicyContext          `json:"policy_context,omitempty"`
	Environment   *EnvironmentRef         `json:"environment,omitempty"`
	Tasks         []IRTask                `json:"tasks"`
	Source        *Source                 `json:"source,omitempty"`
	Env           map[string]string       `json:"env,omitempty"`
	Params        map[string]any          `json:"params,omitempty"`
	Inputs        map[string]TriggerInput `json:"-"`
}

// RepositoryRef names the repository by immutable id.
type RepositoryRef struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id,omitempty"`
}

// Trigger is the event that starts the pipeline. Events carries the
// structured filters the compiler derived from the workflow's `on` block;
// Filters.CEL is the same selection as one CEL expression.
type Trigger struct {
	Event   string                  `json:"event"`
	Filters *TriggerFilters         `json:"filters,omitempty"`
	Inputs  map[string]TriggerInput `json:"inputs,omitempty"`
	Events  []TriggerEvent          `json:"events,omitempty"`
}

// TriggerFilters narrows the trigger.
type TriggerFilters struct {
	CEL string `json:"cel,omitempty"`
}

// TriggerInput is a declared dispatch input.
type TriggerInput struct {
	Type     string `json:"type,omitempty"`
	Default  any    `json:"default,omitempty"`
	Options  []any  `json:"options,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// TriggerEvent is one source event with its filters (branches, paths, tags
// and activity types as written, globs included).
type TriggerEvent struct {
	Name           string   `json:"name"`
	Branches       []string `json:"branches,omitempty"`
	BranchesIgnore []string `json:"branches_ignore,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	TagsIgnore     []string `json:"tags_ignore,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	PathsIgnore    []string `json:"paths_ignore,omitempty"`
	Types          []string `json:"types,omitempty"`
	Cron           []string `json:"cron,omitempty"`
}

// Concurrency groups runs.
type Concurrency struct {
	Group            string `json:"group,omitempty"`
	CancelInProgress bool   `json:"cancel_in_progress,omitempty"`
}

// PolicyContext is attached by the planner, never authored.
type PolicyContext struct {
	ActorKind               string `json:"actor_kind,omitempty"`
	RefTrust                string `json:"ref_trust,omitempty"`
	ProvenanceHasAgentHunks bool   `json:"provenance_has_agent_hunks,omitempty"`
}

// EnvironmentRef binds the pipeline to a delivery environment.
type EnvironmentRef struct {
	ID string `json:"id"`
}

// Source says where the IR came from.
type Source struct {
	Frontend        string       `json:"frontend,omitempty"`
	Path            string       `json:"path,omitempty"`
	CompilerVersion string       `json:"compiler_version,omitempty"`
	Divergences     []Divergence `json:"divergences"`
}

// Divergence is a feature of the source the compiler could not map exactly.
// It is recorded on the run and surfaced in the UI; never a silent skip.
type Divergence struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Location string `json:"location,omitempty"`
}

// IRTask is one node of the DAG.
type IRTask struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Needs       []string          `json:"needs,omitempty"`
	Condition   string            `json:"condition,omitempty"`
	MatrixKey   map[string]any    `json:"matrix_key,omitempty"`
	Inputs      Inputs            `json:"inputs"`
	Steps       []Step            `json:"steps"`
	Executor    Executor          `json:"executor"`
	Outputs     *Outputs          `json:"outputs,omitempty"`
	Limits      *Limits           `json:"limits,omitempty"`
	Permissions map[string]string `json:"permissions,omitempty"`
	ContentHash string            `json:"content_hash,omitempty"`
}

// Inputs are the resolved, hashed inputs of a task.
type Inputs struct {
	Source      *SourceInput   `json:"source,omitempty"`
	Image       *ImageInput    `json:"image,omitempty"`
	Actions     []ActionRef    `json:"actions,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Secrets     []SecretRef    `json:"secrets,omitempty"`
	Caches      []CacheRef     `json:"caches,omitempty"`
	ArtifactsIn []ArtifactIn   `json:"artifacts_in,omitempty"`
	Services    []Service      `json:"services,omitempty"`
}

// SourceInput is the commit the task sees.
type SourceInput struct {
	Commit     string   `json:"commit"`
	Paths      []string `json:"paths,omitempty"`
	Submodules string   `json:"submodules,omitempty"`
	LFS        bool     `json:"lfs,omitempty"`
}

// ImageInput is the OCI image, by digest when it could be resolved.
type ImageInput struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`
}

// ActionRef is a `uses:` reference resolved to a SHA.
type ActionRef struct {
	Ref            string `json:"ref"`
	Repository     string `json:"repository"`
	Path           string `json:"path,omitempty"`
	SHA            string `json:"sha"`
	Resolution     string `json:"resolution"`
	VendoredDigest string `json:"vendored_digest,omitempty"`
}

// Resolution kinds for ActionRef.Resolution.
const (
	ResolutionSHAPinned          = "sha-pinned"
	ResolutionTagImmutable       = "tag-immutable"
	ResolutionTagMutableResolved = "tag-mutable-resolved"
	ResolutionBranchResolved     = "branch-resolved"
	ResolutionVendored           = "vendored"
	// ResolutionUnresolved is Ore's marker for a reference the resolver could
	// not reach (network); it is not in the schema enum and always comes with
	// a divergence.
	ResolutionUnresolved = "unresolved"
)

// SecretRef is a secret by reference only.
type SecretRef struct {
	Path  string `json:"path"`
	Mount string `json:"mount,omitempty"`
	Name  string `json:"name,omitempty"`
}

// CacheRef declares a cache the task restores.
type CacheRef struct {
	Key         string   `json:"key"`
	RestoreKeys []string `json:"restore_keys,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Scope       string   `json:"scope,omitempty"`
}

// ArtifactIn declares an artifact the task downloads.
type ArtifactIn struct {
	FromTask string `json:"from_task"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
}

// Service is a sidecar container.
type Service struct {
	Name   string            `json:"name"`
	Image  string            `json:"image"`
	Digest string            `json:"digest,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Ports  []string          `json:"ports,omitempty"`
}

// Step is a run or uses step (lesson steps are ADR 0010 and not emitted here).
type Step struct {
	ID               string            `json:"id,omitempty"`
	Name             string            `json:"name,omitempty"`
	Run              string            `json:"run,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Uses             string            `json:"uses,omitempty"`
	With             map[string]any    `json:"with,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Condition        string            `json:"condition,omitempty"`
	ContinueOnError  bool              `json:"continue_on_error,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
}

// Executor is where the task runs.
type Executor struct {
	Kind   string   `json:"kind"`
	OS     string   `json:"os,omitempty"`
	Arch   string   `json:"arch,omitempty"`
	Size   string   `json:"size,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Egress *Egress  `json:"egress,omitempty"`
}

// Egress is the task's network policy.
type Egress struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow,omitempty"`
}

// Outputs declares what the task produces.
type Outputs struct {
	Artifacts    []ArtifactOut     `json:"artifacts,omitempty"`
	Caches       []CacheOut        `json:"caches,omitempty"`
	Attestations []string          `json:"attestations,omitempty"`
	Values       map[string]string `json:"values,omitempty"`
	Summary      bool              `json:"summary,omitempty"`
}

// ArtifactOut declares an uploaded artifact.
type ArtifactOut struct {
	Name          string   `json:"name"`
	Paths         []string `json:"paths"`
	RetentionDays int      `json:"retention_days,omitempty"`
}

// CacheOut declares a cache the task saves.
type CacheOut struct {
	Key   string   `json:"key"`
	Paths []string `json:"paths"`
}

// Limits bound the task.
type Limits struct {
	TimeoutSeconds    int     `json:"timeout_seconds,omitempty"`
	Retries           int     `json:"retries,omitempty"`
	BudgetVCPUMinutes float64 `json:"budget_vcpu_minutes,omitempty"`
}

// DefaultTaskTimeoutSeconds matches the schema default (6 hours).
const DefaultTaskTimeoutSeconds = 21600
