// SPDX-License-Identifier: AGPL-3.0-only

package web

// Identity pages: who you are, tokens, SSH keys, agents, users.
const tmplIdentity = `
{{define "idnav"}}<nav class="tabs" aria-label="Identity"><ul><li><a href="/identity"{{if eq .IdTab "me"}} class="on" aria-current="page"{{end}}>Me</a></li><li><a href="/identity/tokens"{{if eq .IdTab "tokens"}} class="on" aria-current="page"{{end}}>Tokens</a></li><li><a href="/identity/keys"{{if eq .IdTab "keys"}} class="on" aria-current="page"{{end}}>SSH keys</a></li><li><a href="/identity/agents"{{if eq .IdTab "agents"}} class="on" aria-current="page"{{end}}>Agents</a></li><li><a href="/identity/users"{{if eq .IdTab "users"}} class="on" aria-current="page"{{end}}>Users</a></li></ul></nav>
{{if not .Available}}<p class="mut">The identity extension is not attached to this server: the admin token is the only credential.</p>{{end}}{{if .Error}}<p class="flash bad">{{.Error}}</p>{{end}}{{end}}

{{define "identity"}}{{template "head" .}}
<h1>Identity</h1>{{template "idnav" .}}
{{with .Me}}<div class="card"><p>You are <b>{{.Actor.Kind}}/{{.Actor.ID}}</b>{{if .Admin}} <span class="pill ok">administrator</span>{{end}}</p>
{{with .User}}<dl class="kv"><dt>user</dt><dd>{{.Name}}</dd><dt>email</dt><dd>{{.Email}}</dd><dt>site role</dt><dd>{{.Role}}</dd><dt>status</dt><dd>{{.Status}}</dd><dt>created</dt><dd>{{.CreatedAt}}</dd></dl>{{end}}
{{with .Agent}}<dl class="kv"><dt>agent</dt><dd>{{.Org}}/{{.Name}}</dd><dt>owner</dt><dd>{{.Owner}}</dd><dt>status</dt><dd>{{.Status}}</dd><dt>budgets</dt><dd><code>{{json .Budgets}}</code></dd></dl>{{end}}
{{with .Token}}<p class="mut">credential: token <code>{{.Prefix}}…</code> ({{.Name}}) scopes {{join .Scopes ", "}}{{if .ExpiresAt}} · expires {{deref .ExpiresAt}}{{end}}</p>{{else}}<p class="mut">credential: the admin token or an SSH key</p>{{end}}</div>
<h2>Memberships</h2>
<div class="tbl"><table><thead><tr><th>Organization</th><th>Role</th></tr></thead><tbody>{{range $.Memberships}}<tr><td>{{.Org}}</td><td>{{.Role}}</td></tr>{{else}}<tr><td colspan="2" class="mut">No explicit memberships{{if .Admin}} (administrators have access everywhere){{end}}.</td></tr>{{end}}</tbody></table></div>{{end}}
{{template "foot" .}}{{end}}

{{define "tokens"}}{{template "head" .}}
<h1>Tokens</h1>{{template "idnav" .}}
{{with .NewToken}}<div class="callout"><b>Token {{.Name}} created.</b> Copy it now; it is shown once and only its hash is stored:<br><code>{{.Secret}}</code><br><span class="mut">scopes {{join .Scopes ", "}}{{if .ExpiresAt}} · expires {{deref .ExpiresAt}}{{end}}</span></div>{{end}}
{{if .Available}}<div class="tbl"><table><thead><tr><th>Token</th><th>Name</th><th>Scopes</th><th>Expires</th><th>Last used</th><th>Status</th><th></th></tr></thead><tbody>{{range .Tokens}}<tr><td><code>{{.Prefix}}…</code></td><td>{{.Name}}</td><td>{{join .Scopes ", "}}</td><td class="mut">{{if .ExpiresAt}}{{deref .ExpiresAt}}{{else}}never{{end}}</td><td class="mut">{{deref .LastUsedAt}}</td><td>{{if .RevokedAt}}<span class="pill bad">revoked</span>{{else}}<span class="pill ok">active</span>{{end}}</td><td>{{if not .RevokedAt}}<form method="post" action="/ui/identity/tokens/{{.ID}}/revoke" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="danger">Revoke</button></form>{{end}}</td></tr>{{else}}<tr><td colspan="7" class="mut">No tokens.</td></tr>{{end}}</tbody></table></div>
<h2>Create a token</h2>
<form method="post" action="/ui/identity/tokens" class="stack">{{template "csrf" .CSRF}}
<label for="tname">Name<input id="tname" name="name" placeholder="laptop" required></label>
<fieldset class="row"><legend class="mut">Scopes (write implies read, admin implies write)</legend><label><input type="checkbox" name="scopes" value="read" checked> read</label> <label><input type="checkbox" name="scopes" value="write" checked> write</label> <label><input type="checkbox" name="scopes" value="admin"> admin</label></fieldset>
<div class="row"><label for="texp" class="mut">expires in</label><input id="texp" name="expires_in" placeholder="720h" size="8">{{if and .Me .Me.Admin}} <label for="tuser" class="mut">for user</label><input id="tuser" name="user" placeholder="(yourself)" size="12">{{end}} <label for="tagent" class="mut">or for agent</label><input id="tagent" name="agent" placeholder="org/name" size="14"></div>
<div><button type="submit" class="primary">Create token</button></div></form>
<p class="mut">Use it as <code>Authorization: Bearer &lt;token&gt;</code>, as the password of git over HTTP, or to sign in here.</p>{{end}}
{{template "foot" .}}{{end}}

{{define "keys"}}{{template "head" .}}
<h1>SSH keys</h1>{{template "idnav" .}}
{{if .HostKey}}<p class="mut">This server's host key: <code>{{.HostKey}}</code></p>{{end}}
{{if and .Available .Me .Me.User}}<div class="tbl"><table><thead><tr><th>Fingerprint</th><th>Title</th><th>Added</th><th>Last used</th><th></th></tr></thead><tbody>{{range .Keys}}<tr><td><code>{{.Fingerprint}}</code></td><td>{{.Title}}</td><td class="mut">{{.CreatedAt}}</td><td class="mut">{{deref .LastUsedAt}}</td><td><form method="post" action="/ui/identity/keys/{{.ID}}/remove" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="danger">Remove</button></form></td></tr>{{else}}<tr><td colspan="5" class="mut">No keys.</td></tr>{{end}}</tbody></table></div>
<h2>Add a key</h2>
<form method="post" action="/ui/identity/keys" class="stack">{{template "csrf" .CSRF}}
<label for="pubkey">Public key (one authorized_keys line, e.g. the contents of ~/.ssh/id_ed25519.pub)<textarea id="pubkey" name="public_key" required></textarea></label>
<label for="ktitle">Title<input id="ktitle" name="title" placeholder="laptop"></label>
{{if .Me.Admin}}<label for="kuser">For user (administrators only; blank for yourself)<input id="kuser" name="user"></label>{{end}}
<div><button type="submit" class="primary">Add key</button></div></form>
<p class="mut">Then: <code>git clone ssh://git@&lt;host&gt;:2222/&lt;org&gt;/&lt;repo&gt;.git</code>. Only git-upload-pack and git-receive-pack run over SSH.</p>{{else if .Available}}<p class="mut">Agents do not have SSH keys.</p>{{end}}
{{template "foot" .}}{{end}}

{{define "agents"}}{{template "head" .}}
<h1>Agent principals</h1>{{template "idnav" .}}
{{if .Available}}<p class="mut">Agents are principals with a human owner and budgets (ADR 0009). Their tokens default to read-only; the owner grants write.</p>
<p>{{range .Orgs}}<a href="/identity/agents?org={{.Name}}"{{if eq .Name $.Org}} aria-current="page"{{end}}>{{.Name}}</a> {{end}}{{if and .Me .Me.Admin}}<a href="/identity/agents"{{if eq .Org ""}} aria-current="page"{{end}}>all</a>{{end}}</p>
<div class="tbl"><table><thead><tr><th>Agent</th><th>Owner</th><th>Status</th><th>Budgets</th><th>Created</th></tr></thead><tbody>{{range .Agents}}<tr><td>{{.Org}}/{{.Name}}</td><td>{{.Owner}}</td><td>{{template "status" .Status}}</td><td><code>{{json .Budgets}}</code></td><td class="mut">{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="5" class="mut">No agents.</td></tr>{{end}}</tbody></table></div>
<h2>Create an agent</h2>
<form method="post" action="/ui/identity/agents" class="stack">{{template "csrf" .CSRF}}
<label for="aorg">Organization<select id="aorg" name="org">{{range .Orgs}}<option{{if eq .Name $.Org}} selected{{end}}>{{.Name}}</option>{{end}}</select></label>
<label for="aname">Name<input id="aname" name="name" placeholder="fixer" required></label>
<label for="aowner">Owner (a user name; blank for yourself)<input id="aowner" name="owner"></label>
<label for="abudgets">Budgets (JSON object)<textarea id="abudgets" name="budgets" placeholder='{"ci_minutes": 60, "writes_per_hour": 20}'></textarea></label>
<div><button type="submit" class="primary">Create agent</button></div></form>{{end}}
{{template "foot" .}}{{end}}

{{define "users"}}{{template "head" .}}
<h1>Users</h1>{{template "idnav" .}}
{{if .Available}}<div class="tbl"><table><thead><tr><th>Name</th><th>Email</th><th>Site role</th><th>Status</th><th>Created</th></tr></thead><tbody>{{range .Users}}<tr><td>{{.Name}}</td><td>{{.Email}}</td><td>{{.Role}}</td><td>{{template "status" .Status}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="5" class="mut">{{if and .Me .Me.Admin}}No users.{{else}}Listing users needs an administrator.{{end}}</td></tr>{{end}}</tbody></table></div>
{{if and .Me .Me.Admin}}<h2>Create a user</h2>
<form method="post" action="/ui/identity/users" class="stack">{{template "csrf" .CSRF}}
<label for="uname">Name<input id="uname" name="name" required pattern="[a-z0-9._-]+"></label>
<label for="uemail">Email<input id="uemail" name="email" type="email"></label>
<label for="urole">Site role<select id="urole" name="role"><option>member</option><option>guest</option><option>owner</option></select></label>
<div class="row"><label for="uorg" class="mut">organization</label><select id="uorg" name="org"><option value="">none</option>{{range .Orgs}}<option>{{.Name}}</option>{{end}}</select> <label for="uorgrole" class="mut">role there</label><select id="uorgrole" name="org_role"><option>member</option><option>guest</option><option>owner</option></select></div>
<div><button type="submit" class="primary">Create user</button></div></form>
<p class="mut">Then mint the user's first token on the <a href="/identity/tokens">tokens page</a> with "for user".</p>{{end}}{{end}}
{{template "foot" .}}{{end}}
`
