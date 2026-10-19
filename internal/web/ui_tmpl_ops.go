// SPDX-License-Identifier: AGPL-3.0-only

package web

// Operations pages: deployments, environments, DORA, GitHub, gateway.
const tmplOps = `
{{define "deployments"}}{{template "head" .}}
<h1>Deployments</h1>
<form method="get" action="{{.RepoPath}}/deployments" class="row"><label for="envf" class="mut">Environment</label><select id="envf" name="environment"><option value="">all</option>{{range .Environments}}<option{{if eq .Name $.Environment}} selected{{end}}>{{.Name}}</option>{{end}}{{if not .Environments}}<option{{if eq .Environment "preview"}} selected{{end}}>preview</option><option{{if eq .Environment "production"}} selected{{end}}>production</option>{{end}}</select> <button type="submit">Filter</button> <a class="mut" href="{{.RepoPath}}/environments">environments</a></form>
<div class="tbl"><table><thead><tr><th>Deployment</th><th>Environment</th><th>Commit</th><th>Status</th><th>URL</th><th>Actor</th><th>Created</th></tr></thead><tbody>{{range .Deployments}}<tr><td><a href="{{$.RepoPath}}/deployments/{{.ID}}">{{shortid .ID}}</a>{{if .RollbackOf}} <span class="pill">rollback</span>{{end}}</td><td>{{.Environment}}</td><td><code>{{short .CommitOID}}</code></td><td>{{template "status" .Status}}</td><td>{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{end}}</td><td>{{template "actor" .Actor}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="7" class="mut">No deployments.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .Pager}}
{{template "foot" .}}{{end}}

{{define "deployment"}}{{template "head" .}}
<h1>Deployment {{shortid .D.ID}} to {{.D.Environment}} {{template "status" .D.Status}}</h1>
<dl class="kv"><dt>commit</dt><dd><code>{{.D.CommitOID}}</code></dd><dt>requested by</dt><dd>{{template "actor" .D.Actor}} at {{.D.CreatedAt}}</dd>{{if .D.URL}}<dt>url</dt><dd><a href="{{.D.URL}}">{{.D.URL}}</a></dd>{{end}}{{if .D.Image}}<dt>image</dt><dd><code>{{.D.Image}}</code></dd>{{end}}{{if .D.PreviousID}}<dt>replaced</dt><dd><a href="{{$.RepoPath}}/deployments/{{deref .D.PreviousID}}">{{shortid (deref .D.PreviousID)}}</a></dd>{{end}}{{if .D.RollbackOf}}<dt>rollback of</dt><dd><a href="{{$.RepoPath}}/deployments/{{deref .D.RollbackOf}}">{{shortid (deref .D.RollbackOf)}}</a></dd>{{end}}{{if .D.FinishedAt}}<dt>finished</dt><dd>{{deref .D.FinishedAt}}</dd>{{end}}</dl>
{{with .Env}}<p class="mut">environment <b>{{.Name}}</b> ({{.Kind}}, target {{.Target.Kind}}) · {{.Policy.RequiredApprovals}} approval{{if ne .Policy.RequiredApprovals 1}}s{{end}} required{{if .Policy.Approvers}} from {{join .Policy.Approvers ", "}}{{end}} · health <code>{{.Policy.HealthPath}}</code>{{if .Policy.AutoRollback}} · auto-rollback{{end}} · <a href="{{$.RepoPath}}/environments">manage</a></p>{{end}}
{{if .Awaiting}}<div class="callout"><b>Awaiting approval.</b>{{if .CanApprove}}
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/deployments/{{.D.ID}}/approve" class="row">{{template "csrf" .CSRF}}<label for="acomment" class="mut">comment</label><input id="acomment" name="comment" size="30"> <button type="submit" class="primary">Approve</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/deployments/{{.D.ID}}/reject" class="row">{{template "csrf" .CSRF}}<label for="rcomment" class="mut">comment</label><input id="rcomment" name="comment" size="30"> <button type="submit" class="danger">Reject</button></form>{{else}} <span class="mut">You are not one of this environment's approvers.</span>{{end}}</div>{{end}}
{{with .D.Rollout}}<h2>Rollout</h2><p>source ref <code>{{.SourceRef}}</code>{{if .Failure}} · <span class="bad">{{.Failure}}</span>{{end}}{{if .RecoverySeconds}} · recovered in {{dur (deref_f .RecoverySeconds)}}{{end}}</p>
<h3>Policy decision: {{template "status" .Decision.Outcome}}{{if .Decision.Bypass}} <span class="pill warn">bypass: {{.Decision.Bypass}}</span>{{end}}</h3>
<div class="tbl"><table><thead><tr><th>Rule</th><th>Result</th><th>Detail</th></tr></thead><tbody>{{range .Decision.Checks}}<tr><td><code>{{.Rule}}</code></td><td>{{if .Pass}}<span class="pill ok">pass</span>{{else}}<span class="pill bad">fail</span>{{end}}</td><td class="mut">{{.Detail}}</td></tr>{{end}}</tbody></table></div>
<p class="mut">{{.Decision.Approvals}} of {{.Decision.RequiredApprovals}} approvals{{if .Decision.WaitUntil}} · wait until {{.Decision.WaitUntil}}{{end}} · evaluated {{.Decision.EvaluatedAt}}</p>
{{with .Health}}<h3>Health</h3><p>{{if .Skipped}}<span class="mut">no probe (target none)</span>{{else}}{{if .Healthy}}<span class="pill ok">healthy</span>{{else}}<span class="pill bad">unhealthy</span>{{end}} <code>{{.URL}}</code> · {{.Attempts}} attempts in {{ms .ElapsedMS}}{{if .LastStatus}} · last status {{.LastStatus}}{{end}}{{if .LastError}} · {{.LastError}}{{end}}{{end}}</p>{{end}}{{end}}
{{if .D.Approvals}}<h2>Approvals</h2><div class="tbl"><table><thead><tr><th>Approver</th><th>Decision</th><th>Comment</th><th>When</th></tr></thead><tbody>{{range .D.Approvals}}<tr><td>{{.Approver}}</td><td>{{template "status" .Decision}}</td><td>{{.Comment}}</td><td class="mut">{{.At}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{with .D.TTL}}<h2>Preview lifetime</h2><p>expires {{.ExpiresAt}} ({{.Extensions}} extensions)</p>
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/deployments/{{.DeploymentID}}/extend" class="row">{{template "csrf" $.CSRF}}<label for="hours" class="mut">Extend by</label><input id="hours" name="hours" value="2" size="4" inputmode="decimal"> hours <button type="submit">Extend</button></form>{{end}}
{{template "foot" .}}{{end}}

{{define "environments"}}{{template "head" .}}
<h1>Environments</h1>
{{if not .Available}}<p class="mut">The delivery environments extension is not attached; deployments use the implicit preview and production environments from the overview page.</p>{{else}}
{{range .Envs}}<div class="card"><h3>{{.Name}} <span class="pill">{{.Kind}}</span> <span class="pill">{{.Target.Kind}}{{if .Target.Host}} {{.Target.User}}@{{.Target.Host}}{{if .Target.Port}}:{{.Target.Port}}{{end}}{{end}}</span></h3>
<p class="mut">{{.Policy.RequiredApprovals}} approval{{if ne .Policy.RequiredApprovals 1}}s{{end}}{{if .Policy.Approvers}} from {{join .Policy.Approvers ", "}}{{end}}{{if .Policy.AllowedRefs}} · refs {{join .Policy.AllowedRefs ", "}}{{end}}{{if .Policy.WaitSeconds}} · wait {{.Policy.WaitSeconds}}s{{end}}{{if .Policy.RequireGreenPipeline}} · green pipeline required{{end}} · agents {{if .Policy.AllowAgentDeploys}}allowed{{else}}refused{{end}} · health <code>{{.Policy.HealthPath}}</code> within {{.Policy.HealthTimeoutSeconds}}s{{if .Policy.AutoRollback}} · auto-rollback{{end}}{{if .Policy.PreviewTTLSeconds}} · preview TTL {{.Policy.PreviewTTLSeconds}}s{{end}}</p>
{{with .Current}}<p>current: <a href="{{$.RepoPath}}/deployments/{{.ID}}">{{shortid .ID}}</a> <code>{{short .CommitOID}}</code> {{template "status" .Status}}{{if .URL}} <a href="{{.URL}}">{{.URL}}</a>{{end}}</p>{{else}}<p class="mut">nothing deployed</p>{{end}}
{{range .Awaiting}}<div class="callout">awaiting approval: <a href="{{$.RepoPath}}/deployments/{{.ID}}">{{shortid .ID}}</a> <code>{{short .CommitOID}}</code> by {{template "actor" .Actor}}
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/deployments/{{.ID}}/approve" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="primary">Approve</button></form>
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/deployments/{{.ID}}/reject" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="danger">Reject</button></form></div>{{end}}
<div class="row"><form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/environments/{{.Name}}/deploy" class="inline">{{template "csrf" $.CSRF}}<label for="ref-{{.Name}}" class="mut">deploy</label> <input id="ref-{{.Name}}" name="ref" value="{{$.Repo.DefaultBranch}}" size="16"> <button type="submit" class="primary">Request deployment</button></form>
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/environments/{{.Name}}/rollback" class="inline">{{template "csrf" $.CSRF}}<button type="submit">Roll back</button></form>
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/environments/{{.Name}}/stop" class="inline">{{template "csrf" $.CSRF}}<button type="submit">Stop</button></form>
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/environments/{{.Name}}/delete" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="danger">Delete</button></form></div></div>{{else}}<p class="mut">No environment records yet; deployments go through the implicit local-docker path. Create one to add policy.</p>{{end}}
<h2>Create an environment</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/environments" class="stack">{{template "csrf" .CSRF}}
<label for="ename">Name<input id="ename" name="name" required></label>
<label for="ekind">Kind<select id="ekind" name="kind">{{range .Kinds}}<option>{{.}}</option>{{end}}</select></label>
<label for="etarget">Target<select id="etarget" name="target_kind"><option value="local-docker">local-docker</option><option value="ssh-docker">ssh-docker</option><option value="none">none (record only)</option></select></label>
<div class="row"><label for="ehost" class="mut">ssh host</label><input id="ehost" name="host"> <label for="euser" class="mut">user</label><input id="euser" name="user" size="8"> <label for="eport" class="mut">port</label><input id="eport" name="port" size="4" inputmode="numeric"> <label for="ekey" class="mut">key ref</label><input id="ekey" name="key_ref" placeholder="file:/path | env:VAR | secret:NAME"></div>
<div class="row"><label for="eappr" class="mut">required approvals</label><input id="eappr" name="required_approvals" size="3" inputmode="numeric" value="0"> <label for="eapprovers" class="mut">approvers (comma separated)</label><input id="eapprovers" name="approvers"> <label for="erefs" class="mut">allowed refs</label><input id="erefs" name="allowed_refs" placeholder="main, refs/tags/v*"></div>
<div class="row"><label for="ewait" class="mut">wait seconds</label><input id="ewait" name="wait_seconds" size="4" inputmode="numeric"> <label for="ehealth" class="mut">health path</label><input id="ehealth" name="health_path" value="/"> <label for="ehto" class="mut">health timeout s</label><input id="ehto" name="health_timeout_seconds" size="4" inputmode="numeric"> <label for="ettl" class="mut">preview TTL s</label><input id="ettl" name="preview_ttl_seconds" size="6" inputmode="numeric"></div>
<div class="row"><label><input type="checkbox" name="auto_rollback" checked> auto-rollback on failed health</label> <label><input type="checkbox" name="require_green_pipeline"> require a green pipeline</label> <label><input type="checkbox" name="allow_agent_deploys"> allow agent deploys</label></div>
<div><button type="submit" class="primary">Create environment</button></div></form>{{end}}
{{template "foot" .}}{{end}}

{{define "dora"}}{{template "head" .}}
<h1>DORA</h1>
<p><a href="{{.RepoPath}}/dora?days=7"{{if eq .Days 7}} aria-current="page"{{end}}>7 days</a> · <a href="{{.RepoPath}}/dora?days=30"{{if eq .Days 30}} aria-current="page"{{end}}>30 days</a> · <a href="{{.RepoPath}}/dora?days=90"{{if eq .Days 90}} aria-current="page"{{end}}>90 days</a></p>
{{with .DORA}}<p class="mut">{{.Since}} → {{.Until}} · production deployments computed from the ledger</p>
<div class="cards"><div class="card"><span class="mut">deployment frequency</span><b>{{printf "%.2f" .Deploys.PerDay}}/day</b><span class="mut">{{printf "%.1f" .Deploys.PerWeek}}/week · {{.Deploys.Total}} started, {{.Deploys.Succeeded}} succeeded, {{.Deploys.Rollbacks}} rollbacks</span></div>
<div class="card"><span class="mut">lead time for changes</span><b>{{dur .LeadTime.MedianSeconds}}</b><span class="mut">median of {{.LeadTime.Samples}} · p90 {{dur .LeadTime.P90Seconds}}</span></div>
<div class="card"><span class="mut">change failure rate</span><b>{{pct .ChangeFailure.Rate}}</b><span class="mut">{{.ChangeFailure.Failed}} failed of {{.ChangeFailure.Total}}</span></div>
<div class="card"><span class="mut">failed-deployment recovery time</span><b>{{dur .Recovery.MedianSeconds}}</b><span class="mut">median of {{.Recovery.Samples}} · p90 {{dur .Recovery.P90Seconds}}</span></div></div>
<p class="mut">Lead time: change opened → the production success that shipped it. Change failure: non-rollback deployments that failed, were rolled back or have a rollback pointing at them. Recovery: the recovery time recorded on the rollback, else the time to the next success.</p>{{else}}<p class="mut">The delivery environments extension is not attached, so there is no DORA view.</p>{{end}}
{{template "foot" .}}{{end}}

{{define "github"}}{{template "head" .}}
<h1>GitHub</h1>
{{if not .Available}}<p class="mut">The GitHub adapter is not attached to this server.</p>{{else if .Error}}<p class="flash bad">{{.Error}}</p>{{end}}
{{with .Link}}<div class="card"><p>Linked to <a href="{{.HTMLURL}}">{{.Owner}}/{{.Name}}</a> · default branch <code>{{.DefaultBranch}}</code> · linked {{.CreatedAt}}</p>
<dl class="kv"><dt>last sync</dt><dd>{{if .LastSyncStatus}}{{template "status" .LastSyncStatus}}{{else}}<span class="mut">never</span>{{end}} {{.LastSyncAt}}{{if .LastSyncError}} <span class="bad">{{.LastSyncError}}</span>{{end}}</dd><dt>next sync</dt><dd>{{.NextSyncAt}}</dd><dt>webhook URL</dt><dd><code>{{$.WebhookURL}}</code></dd><dt>webhook secret</dt><dd><details><summary>show</summary><code>{{.Secret}}</code></details></dd></dl>
<div class="row"><form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/github/sync" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="primary">Sync now</button></form>
<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/github/unlink" class="inline">{{template "csrf" $.CSRF}}<button type="submit" class="danger">Unlink</button></form></div>
<p class="mut">GitHub is the source of truth for refs, issues and pull requests on a linked repository; mirrored refs live under <code>refs/github/*</code>; pipeline results are posted as the <code>ore/pipeline</code> status.</p></div>
{{else}}{{if .Available}}<h2>Link a GitHub repository</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/github/link" class="row">{{template "csrf" .CSRF}}<label for="owner" class="mut">owner</label><input id="owner" name="owner" required> <label for="gname" class="mut">name</label><input id="gname" name="name" required> <button type="submit" class="primary">Link and mirror</button></form>
<p class="mut">Needs <code>ORE_GITHUB_TOKEN</code> on the server. After linking, add a webhook on GitHub with the URL and secret shown here (events: pushes, issues, pull requests).</p>{{end}}{{end}}
<h2>GitHub events</h2>
<div class="tbl"><table><thead><tr><th>Seq</th><th>Time</th><th>Type</th><th>Subject</th><th>Actor</th><th>Data</th></tr></thead><tbody>{{template "eventrows" .Events}}{{if not .Events}}<tr><td colspan="6" class="mut">None yet.</td></tr>{{end}}</tbody></table></div>
{{template "foot" .}}{{end}}

{{define "gateway"}}{{template "head" .}}
<h1>Gateway sessions</h1>
<p class="mut">Agents write through the safe-outputs gateway: one session per agent and repository, schema-validated requests, policy, rate limits, every decision on the ledger.</p>
{{if .NewToken}}<div class="callout"><b>Session {{.NewSession.ID}} opened for {{.NewSession.AgentID}}.</b> Its token is shown once:<br><code>{{.NewToken}}</code></div>{{end}}
{{if not .Available}}<p class="mut">The gateway extension is not attached to this server.</p>{{else if .Forbidden}}<p class="mut">Listing and opening sessions needs an administrator token.</p>{{else if .Error}}<p class="flash bad">{{.Error}}</p>{{end}}
{{if .Available}}<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/gateway/sessions" class="row">{{template "csrf" .CSRF}}<label for="agent_id" class="mut">Open a session for agent</label><input id="agent_id" name="agent_id" placeholder="claude-code" required> <button type="submit" class="primary">Open session</button></form>
<div class="tbl"><table><thead><tr><th>Session</th><th>Agent</th><th>State</th><th>Writes</th><th>Refused</th><th>Opened</th><th></th></tr></thead><tbody>{{range .Sessions}}<tr><td><code>{{.ID}}</code></td><td>{{.AgentID}}</td><td>{{template "status" .State}}</td><td>{{.Writes}} <span class="mut">({{.WindowWrites}} in window)</span></td><td>{{.Refused}}</td><td class="mut">{{.CreatedAt}}</td><td>{{if eq .State "open"}}<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/gateway/sessions/{{.ID}}/close" class="inline">{{template "csrf" $.CSRF}}<button type="submit">Close</button></form>{{end}}</td></tr>{{else}}<tr><td colspan="7" class="mut">No sessions.</td></tr>{{end}}</tbody></table></div>{{end}}
<h2>Connect an MCP client</h2>
<p>The MCP endpoint is <code>{{.MCPURL}}</code> (Streamable HTTP, JSON-RPC over POST, bearer token). With a session token the tools are scoped to this repository and writes go through the gateway; with the admin token the tools read every repository and the write tools refuse.</p>
<pre>claude mcp add --transport http ore {{.MCPURL}} --header "Authorization: Bearer &lt;session token&gt;"
curl -s {{.MCPURL}} -H "Authorization: Bearer &lt;token&gt;" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'</pre>
<h2>Session events</h2>
<div class="tbl"><table><thead><tr><th>Seq</th><th>Time</th><th>Type</th><th>Subject</th><th>Actor</th><th>Data</th></tr></thead><tbody>{{template "eventrows" .Events}}{{if not .Events}}<tr><td colspan="6" class="mut">None yet.</td></tr>{{end}}</tbody></table></div>
{{template "foot" .}}{{end}}
`
