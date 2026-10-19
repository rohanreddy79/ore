# internal/identity — users, memberships, agents, tokens, SSH keys, git over SSH

Phase 0 of the identity layer (docs/03 §2.2, docs/07 §2, ADR 0009). It registers as `identity`, sets `web.Server.Authenticate`, attaches `s.Services["identity"]` (a `*Service`) and `s.Services["identity.sshd"]` (the `*sshd.Server`), serves `/api/v0/identity/`, and listens for git over SSH on `ORE_SSH_ADDR` (default `:2222`). The admin token printed by `ore serve` stays valid and maps to the bootstrap owner user `admin`.

What landed, the auth model, how to add a key and clone over SSH, the edge cases covered by tests and what is deferred: `docs/changes/identity-ssh.md`.

Files: `identity.go` (types, users, memberships, agents), `tokens.go` (secrets, scopes, token store), `sshkeys.go` (key parsing, fingerprints, key store), `auth.go` (principal resolution, policy, the Authenticate hook), `routes.go` (REST), `register.go` (the extension), `sshd/` (the SSH server). Deferred: SAML/OIDC/SCIM, passkeys, SSH certificates, Cedar policy evaluation, IP allow-lists.
