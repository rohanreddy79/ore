# Identity: users, tokens, SSH keys, SSH transport

Branch `slice/identity-ssh` · registers as `identity` · migration `0004_identity.sql` · owns `internal/identity/` (including `sshd/`).

## What landed

- **Principals** (`internal/ledger/migrations/0004_identity.sql`): `users` (site role `owner|member|guest`, status), `memberships` (user × org with an org role `owner|member|guest`), `agents` (org, name, human owner, budgets JSON, status; ADR 0009), `tokens` (SHA-256 of the secret, scopes, expiry, revocation, last use; belongs to exactly one user or agent), `ssh_keys` (user, normalized public key, SHA-256 fingerprint, title, last use). Users are site-wide, so `users` has no `org_id`; `memberships` and `agents` carry it, and `tokens`/`ssh_keys` carry `''` for site-wide user credentials or the agent's org for agent tokens.
- **Service** `s.Services["identity"]` (`*identity.Service`): store methods for everything above, `Resolve`/`ResolveSecret` (credential → principal), `CheckPolicy` (the org policy), `Authenticate` (the `web.Server` hook), `ActorForKey` and `Authorize` (what the SSH server needs).
- **Authentication hook** `s.Authenticate` replaces the single-token authenticator. The admin token printed by `ore serve` stays valid and maps to the bootstrap owner user `admin` (created on first start, site role `owner`).
- **REST** under `/api/v0/identity/`:

  | Route | Who | Does |
  |---|---|---|
  | `GET  me` | anyone | the resolved principal (actor, user or agent, token without secret, `admin`) |
  | `GET/POST users`, `GET users/{name}` | administrators (self for GET by name) | list, create (`name`, `email`, `role`, `memberships:[{org,role}]`) |
  | `GET memberships[?user=]` | self, administrators for others | list a user's orgs and roles |
  | `GET orgs/{org}/members`, `POST orgs/{org}/members`, `DELETE orgs/{org}/members/{user}` | members read; org owners and administrators write | list, add or change role (`user`, `role`), remove |
  | `GET tokens[?user=\|agent=org/name]`, `POST tokens`, `DELETE tokens/{id}` | self; administrators for any user; the agent's owner, org owners and administrators for agents | create returns the secret **once** in `token`; body: `name`, `scopes`, `expires_in` (`720h`) or `expires_at` (RFC 3339), `user` or `agent` |
  | `GET ssh-keys[?user=]`, `POST ssh-keys`, `DELETE ssh-keys/{id}` | self, administrators for others | body: `public_key` (one authorized_keys line), `title`, `user` |
  | `GET agents?org=`, `POST agents` | org owners and administrators create | body: `org`, `name`, `owner` (defaults to the caller), `budgets` (JSON object) |

  Errors: 400 invalid, 401 no or bad credential, 403 policy, 404 unknown, 409 duplicate.
- **Audit**: every change is an event in the same transaction: `io.ore.identity.user.created`, `.membership.added|changed|removed`, `.agent.created`, `.token.created` (never the secret or its hash), `.token.revoked`, `.ssh_key.added|removed`. Site-level events carry an empty org id.
- **Git over SSH** (`internal/identity/sshd`, golang.org/x/crypto/ssh): listens on `ORE_SSH_ADDR` (default `:2222`; `off` disables; when the default port is busy a free port is used and logged, an explicit address must bind). An ed25519 host key is generated into `<data>/ssh_host_ed25519_key` on first start (`.pub` next to it, fingerprint in the log). Public-key authentication only, against `ssh_keys`; the SSH user name (`git@`) is ignored. Only `git-upload-pack` and `git-receive-pack` (also `git upload-pack|receive-pack`) on `org/name.git` paths run; everything else is refused with a message and exit status 1; a shell request prints a greeting and exits 1. Pushes run `git receive-pack` with `ORE_ACTOR_KIND`, `ORE_ACTOR_ID`, `ORE_PROTECTED_REFS=refs/heads/<default>` and `ORE_VIA=ssh`, so the pre-receive hook protects the default branch exactly as over HTTP. Refs are snapshotted before and after each receive-pack (serialized per repository) and the differences are recorded with `RecordRefUpdate(..., "ssh", actor)` **before** the client gets the exit status, so `io.ore.ref.updated` is on the ledger when `git push` returns. `GIT_PROTOCOL` from the client is passed through, so protocol v2 works.
- `golang.org/x/crypto` is the one new dependency (`go.mod`, `go.sum`).

## The auth model

**Credentials.** A request carries at most one credential, found in this order with no fallback: `Authorization: Bearer <secret>`, basic auth (the password is the secret, the user name is informational), the `ore_token` cookie, the `token` query parameter. Over SSH the credential is the public key.

**Resolution.** The admin token (constant-time compare) → the bootstrap owner, actor `human/admin`, every scope. Otherwise the SHA-256 of the secret is looked up in `tokens`; the token must not be revoked or expired and its user or agent must be active. A user token → actor `human/<name>`; an agent token → actor `agent/<name>`. Secrets look like `ore_<40 hex>`; only the hash is stored; `prefix` (the first 10 characters) identifies a token in listings and events.

**Scopes** on tokens: `read`, `write`, `admin`, with `admin ⊇ write ⊇ read`. GET requests, `git-upload-pack` and `info/refs?service=git-upload-pack` need `read`; everything else needs `write`. User tokens default to `read,write`, agent tokens to `read` (ADR 0009: agents read by default; the owner grants `write`). A token can only mint tokens with scopes it has itself. Administrative routes need a site owner **and** the `admin` scope (the admin token has it); SSH keys carry every scope because the policy for keys is the org policy below.

**Policy** (`CheckPolicy`, applied by the HTTP hook per request and by the SSH server per command): administrators (admin token, site owners) pass everywhere; a user must be a member of the org named by the git path or by `/api/v0/repos/{org}/…`, and a `guest` may only read; an agent must belong to the org. Site-level routes (`/api/v0/orgs`, `/api/v0/repos`, `/api/v0/events`, the UI index, `/api/v0/identity/…`) check only the scope in v0. Over HTTP the hook can only answer 401 (the `Authenticate` contract), so a policy refusal shows as "authentication required"; the reason is in the server log. Over SSH the reason is printed to the client (`ore: alice is not a member of acme`).

**Actors on the ledger.** Actor ids are principal names (`human/alice`, `agent/fixer`, `human/admin`), which keeps the pre-existing `admin` actor and `ore demo` output unchanged. Basic auth with the admin token now records `admin` rather than whatever user name was typed.

## Add a key and clone over SSH

```bash
# as an administrator (the token printed by ore serve)
ore api POST /api/v0/identity/users '{"name":"alice","email":"alice@example.com","memberships":[{"org":"acme","role":"member"}]}'
ore api POST /api/v0/identity/tokens '{"name":"laptop","user":"alice"}'      # copy "token": it is shown once
# as alice
ORE_TOKEN=ore_… ore api POST /api/v0/identity/ssh-keys "{\"public_key\":\"$(cat ~/.ssh/id_ed25519.pub)\",\"title\":\"laptop\"}"
git clone ssh://git@localhost:2222/acme/app.git
git clone http://alice:ore_…@localhost:7777/acme/app.git                     # the same token over HTTP
ssh -T git@localhost -p 2222                                                 # "Hi alice! …"
ore events --type io.ore.ref.updated                                         # (human/alice), via "ssh"
```

The host key fingerprint is logged at start (`identity: git over ssh … host_key=SHA256:…`) and is in `<data>/ssh_host_ed25519_key.pub`.

## Tests

Unit (`internal/identity`, `internal/identity/sshd`): secret shape and hashing, scope parsing and implication, token defaults and expiry, idempotent revocation with a single event, OpenSSH fingerprints (checked against `ssh-keygen -lf`), key limits (malformed, oversized, two keys, small RSA, DSA, type mismatch), key store round trip and `ActorForKey`, resolution per carrier, resolution order without fallback, every refusal, `CheckPolicy` scenarios, request classification, the `Authenticate` hook, memberships, SSH command splitting, repository path parsing, host key generation and reuse.

End to end (`internal/identity/e2e_test.go`, a real `web.Server` with git smart HTTP and the SSH server on a random port, driven by real `git` and `ssh`): create a user and a token through the API, clone over HTTP with basic auth, add an SSH key, push over SSH to a branch, see `io.ore.ref.updated` with the user as actor and via `ssh`, then clone over SSH. Edge cases, each a named test:

- `TestE2E_ExpiredToken`: 401 on the API, clone refused; a token already expired at creation is a 400.
- `TestE2E_RevokedToken`: another user cannot revoke (403); after the owner revokes, 401 everywhere; one `token.revoked` event.
- `TestE2E_ReadOnlyTokenCannotPush`: clone works, push is refused and leaves no `ref.updated`, API writes are 401, it cannot mint a `write` token.
- `TestE2E_UnknownSSHKeyIsRefused`: `Permission denied (publickey)`.
- `TestE2E_SSHPushToProtectedBranchIsRefused`: `refs/heads/main is protected; push a branch and open a change instead` and `pre-receive hook declined`; nothing recorded; a branch still pushes.
- `TestE2E_RemovedMemberCannotPush`: after `DELETE orgs/acme/members/alice`, SSH push and clone are refused with `alice is not a member of acme`, HTTP push and repo API are 401, `me` still works; re-added as a guest she can clone but not push.
- `TestE2E_ConcurrentSSHPushesAreBothRecorded`: two pushes to two branches at once, both refs and both events.
- `TestE2E_SSHNonGitCommandIsRefused`: `ls`, a chained command, extra arguments, a `..` path and an unknown repository each exit 1 with the reason; a shell request exits 1 with the greeting.
- `TestE2E_BadKeyUploads`: oversized, malformed, prose, empty, wrong type, wrong field, a 200 KB body → 400; a duplicate → 409; remove works.
- `TestE2E_AdminTokenStillWorksForEverything`: `me` is `human/admin` with `admin: true`; every pre-existing API path; git over HTTP with any user name and the token; the protected branch stays protected for the admin too; identity administration; an SSH push as the admin user; `/login?token=` still sets the cookie.
- `TestE2E_AgentPrincipal`: a member cannot create agents; the admin creates one owned by alice; alice mints its tokens; the default token reads but cannot commit (401), a `read,write` token commits through the API and the event's actor is `agent/fixer`; agents cannot upload keys or administer.

The gate `scripts/e2e.sh` passes (gofmt, vet, every package, build, the Docker demo).

## Deferred

- SAML 2.0 / OIDC SSO, SCIM provisioning, passkeys (docs/03 §2.2); users are created by administrators through the API only.
- SSH certificates (CA, ≤366-day validity, source-address pinning) and IP allow-lists (docs/07 §2); only plain keys are accepted.
- Cedar policy evaluation (ADR 0011): the policy here is the fixed membership rule set in `CheckPolicy`; per-repository rules, required checks and deployment approvals come with the policy engine.
- 403 over HTTP: `web.Server.Authenticate` returns an actor or nil, so policy refusals are reported as 401 until the hook can carry a reason.
- Org scoping of site-level routes (`/api/v0/orgs`, `/api/v0/repos`, `/api/v0/events`): any member sees every org's events in v0, as before.
- Budgets are stored on agents but not enforced; the gateway slice owns agent write limits.
- Scope classification is by HTTP method, so a token with only `read` cannot use POST-only protocols such as `/mcp` (the MCP gateway slice): give such callers `read,write` and let the gateway and `ORE_MCP_READ_ONLY` gate the writes, or use a gateway session token, which the gateway authenticates before this hook runs.
- The HTTP and SSH push paths serialize per repository separately; a simultaneous HTTP and SSH push to the same repository is safe for git but may attribute a ref snapshot to the wrong transport.
