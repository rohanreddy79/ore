// SPDX-License-Identifier: AGPL-3.0-only

package web

// Repository pages: overview, issues, changes list, stacks, pipeline and agent runs.
const tmplRepo = `
{{define "repo"}}{{template "head" .}}
<h1>{{.Repo.Org}}/{{.Repo.Name}}</h1>
<p class="mut">clone: <code>git clone {{.BaseURL}}/{{.Repo.Org}}/{{.Repo.Name}}.git</code> · default branch <code>{{.Repo.DefaultBranch}}</code> is protected: it moves only through changes.</p>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/level" class="row">{{template "csrf" .CSRF}}<label for="level">Autonomy level</label>
<select id="level" name="level">{{range $i, $n := .Levels}}<option value="{{$i}}"{{if eq $i $.Repo.AutonomyLevel}} selected{{end}}>L{{$i}} {{$n}}</option>{{end}}</select>
<label for="level-reason" class="mut">reason</label><input id="level-reason" name="reason" size="24"> <button type="submit">Set</button></form>
<div class="cards">
<div class="card"><span class="mut">open issues</span><b>{{.OpenIssues}}</b><a href="{{.RepoPath}}/issues">all {{.Issues}}</a></div>
<div class="card"><span class="mut">open changes</span><b>{{.OpenChanges}}</b><a href="{{.RepoPath}}/changes">all {{.ChangeCount}}</a></div>
<div class="card"><span class="mut">latest pipeline run</span><b>{{with index .Runs 0}}{{template "status" .Status}}{{else}}–{{end}}</b><a href="{{.RepoPath}}/runs">runs</a></div>
<div class="card"><span class="mut">latest deployment</span><b>{{with index .Deployments 0}}{{template "status" .Status}}{{else}}–{{end}}</b><a href="{{.RepoPath}}/deployments">deployments</a></div>
{{with .Policy}}<div class="card"><span class="mut">review policy</span><b>{{.RequiredApprovals}} approval{{if ne .RequiredApprovals 1}}s{{end}}</b><span class="mut">default landing: {{.DefaultStrategy}}{{if .AllowAgentApprovals}} · agents may approve{{end}}</span></div>{{end}}
</div>
{{with .Fleet}}<h2>Agent fleet: budgets and evidence</h2><dl class="kv">{{range .}}<dt>{{.Key}}</dt><dd><code>{{.Value}}</code></dd>{{end}}</dl>{{end}}
{{with .DORA}}<h2>DORA, last {{.Days}} days <a class="mut" href="{{$.RepoPath}}/dora">details</a></h2>
<div class="cards"><div class="card"><span class="mut">deploys per week</span><b>{{printf "%.1f" .Deploys.PerWeek}}</b><span class="mut">{{.Deploys.Succeeded}} succeeded, {{.Deploys.Rollbacks}} rollbacks</span></div>
<div class="card"><span class="mut">lead time (median)</span><b>{{dur .LeadTime.MedianSeconds}}</b><span class="mut">p90 {{dur .LeadTime.P90Seconds}}</span></div>
<div class="card"><span class="mut">change failure rate</span><b>{{pct .ChangeFailure.Rate}}</b><span class="mut">{{.ChangeFailure.Failed}} of {{.ChangeFailure.Total}}</span></div>
<div class="card"><span class="mut">recovery time (median)</span><b>{{dur .Recovery.MedianSeconds}}</b><span class="mut">p90 {{dur .Recovery.P90Seconds}}</span></div></div>{{end}}
{{if .Environments}}<h2>Environments <a class="mut" href="{{.RepoPath}}/environments">manage</a></h2>
<div class="tbl"><table><thead><tr><th>Name</th><th>Kind</th><th>Target</th><th>Approvals</th><th>Health</th></tr></thead><tbody>{{range .Environments}}<tr><td>{{.Name}}</td><td>{{.Kind}}</td><td>{{.Target.Kind}}{{if .Target.Host}} {{.Target.User}}@{{.Target.Host}}{{end}}</td><td>{{.Policy.RequiredApprovals}}</td><td><code>{{.Policy.HealthPath}}</code>{{if .Policy.AutoRollback}} · auto-rollback{{end}}</td></tr>{{end}}</tbody></table></div>{{end}}
<h2>Act</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/agent" class="row">{{template "csrf" .CSRF}}<label for="prompt" class="mut">Run the agent with a prompt</label><input id="prompt" name="prompt" size="48" placeholder="what should the agent do?"> <button type="submit" class="primary">Run agent</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/runs" class="row">{{template "csrf" .CSRF}}<label for="run-ref" class="mut">Run the pipeline on</label><input id="run-ref" name="ref" value="{{.Repo.DefaultBranch}}" size="16"> <button type="submit">Run pipeline</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/deploy" class="row">{{template "csrf" .CSRF}}<label for="dep-ref" class="mut">Deploy</label><input id="dep-ref" name="ref" value="{{.Repo.DefaultBranch}}" size="16"> <label for="dep-env" class="mut">to</label><select id="dep-env" name="environment">{{if .Environments}}{{range .Environments}}<option>{{.Name}}</option>{{end}}{{else}}<option>preview</option><option>production</option>{{end}}</select> <button type="submit">Deploy</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/rollback" class="row">{{template "csrf" .CSRF}}<input type="hidden" name="environment" value="production"><button type="submit" class="danger">Roll back production</button></form>
<h2>Recent runs</h2>
<div class="tbl"><table><thead><tr><th>Run</th><th>Commit</th><th>Status</th><th>Created</th></tr></thead><tbody>{{range .Runs}}<tr><td><a href="{{$.RepoPath}}/runs/{{.ID}}">{{shortid .ID}}</a></td><td><code>{{short .CommitOID}}</code></td><td>{{template "status" .Status}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="4" class="mut">No pipeline runs yet.</td></tr>{{end}}</tbody></table></div>
<h2>Recent agent runs</h2>
<div class="tbl"><table><thead><tr><th>Run</th><th>Adapter</th><th>Step</th><th>Status</th><th>Branch</th></tr></thead><tbody>{{range .AgentRuns}}<tr><td><a href="{{$.RepoPath}}/agent/{{.ID}}">{{shortid .ID}}</a></td><td>{{.Adapter}}</td><td>{{.Step}}</td><td>{{template "status" .Status}}</td><td><code>{{.Branch}}</code></td></tr>{{else}}<tr><td colspan="5" class="mut">No agent runs yet.</td></tr>{{end}}</tbody></table></div>
<h2>Recent events for this repository <span id="live-ind" class="live"></span></h2>
{{template "eventtable" .}}
{{template "foot" .}}{{end}}

{{define "issues"}}{{template "head" .}}
<h1>Issues</h1>
<p><a href="{{.RepoPath}}/issues"{{if or (eq .State "") (eq .State "all")}} aria-current="page"{{end}}>all</a> · <a href="{{.RepoPath}}/issues?state=open"{{if eq .State "open"}} aria-current="page"{{end}}>open</a> · <a href="{{.RepoPath}}/issues?state=closed"{{if eq .State "closed"}} aria-current="page"{{end}}>closed</a></p>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/issues" class="stack">{{template "csrf" .CSRF}}<label for="title">Title<input id="title" name="title" required></label><label for="body">Details<textarea id="body" name="body"></textarea></label><div><button type="submit" class="primary">File issue</button></div></form>
<div class="tbl"><table><thead><tr><th>#</th><th>Title</th><th>State</th><th>Created</th></tr></thead><tbody>{{range .Issues}}<tr><td><a href="{{$.RepoPath}}/issues/{{.Number}}">{{.Number}}</a></td><td><a href="{{$.RepoPath}}/issues/{{.Number}}">{{.Title}}</a></td><td>{{template "status" .State}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="4" class="mut">No issues.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .Pager}}
{{template "foot" .}}{{end}}

{{define "issue"}}{{template "head" .}}
<h1>#{{.Issue.Number}} {{.Issue.Title}} {{template "status" .Issue.State}}</h1>
<p class="mut">opened {{.Issue.CreatedAt}}{{if .Issue.ClosedAt}} · closed {{deref .Issue.ClosedAt}}{{end}}{{if .GitHubURL}} · <a href="{{.GitHubURL}}">on GitHub</a>{{end}}</p>
{{if .Issue.Body}}<div class="comment"><div class="body">{{.Issue.Body}}</div></div>{{end}}
<div class="row">{{if eq .Issue.State "open"}}<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/agent" class="inline">{{template "csrf" .CSRF}}<input type="hidden" name="issue" value="{{.Issue.Number}}"><button type="submit" class="primary">Run agent on this issue</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/issues/{{.Issue.Number}}/close" class="inline">{{template "csrf" .CSRF}}<label for="close-reason" class="mut">reason</label> <input id="close-reason" name="reason" size="20"> <button type="submit">Close</button></form>{{end}}</div>
<h2>Comments ({{len .Comments}})</h2>
{{range .Comments}}<div class="comment"><div class="meta">{{template "actor" .Actor}}<span>{{.CreatedAt}}</span>{{if .SessionID}}<span class="pill">gateway {{shortid .SessionID}}</span>{{end}}</div><div class="body">{{.Body}}</div></div>{{else}}<p class="mut">No comments yet.</p>{{end}}
{{if .CommentsAvailable}}<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/issues/{{.Issue.Number}}/comments" class="stack">{{template "csrf" .CSRF}}<label for="comment">Comment<textarea id="comment" name="body" required></textarea></label><div><button type="submit">Post comment</button></div></form>{{else}}<p class="mut">Comments need the gateway extension (its migration creates the comments table).</p>{{end}}
{{if .AgentRuns}}<h2>Agent runs on this issue</h2><div class="tbl"><table><thead><tr><th>Run</th><th>Status</th><th>Step</th><th>Branch</th><th>Error</th></tr></thead><tbody>{{range .AgentRuns}}<tr><td><a href="{{$.RepoPath}}/agent/{{.ID}}">{{shortid .ID}}</a></td><td>{{template "status" .Status}}</td><td>{{.Step}}</td><td><code>{{.Branch}}</code></td><td class="mut">{{.Error}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{template "foot" .}}{{end}}

{{define "changes"}}{{template "head" .}}
<h1>Changes</h1>
<p><a href="{{.RepoPath}}/changes"{{if or (eq .State "") (eq .State "all")}} aria-current="page"{{end}}>all</a> · <a href="{{.RepoPath}}/changes?state=open"{{if eq .State "open"}} aria-current="page"{{end}}>open</a> · <a href="{{.RepoPath}}/changes?state=merged"{{if eq .State "merged"}} aria-current="page"{{end}}>merged</a> · <a href="{{.RepoPath}}/changes?state=closed"{{if eq .State "closed"}} aria-current="page"{{end}}>closed</a></p>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/changes" class="row">{{template "csrf" .CSRF}}<label for="source" class="mut">Open a change from branch</label><input id="source" name="source" list="branches" size="24" required><datalist id="branches">{{range .Branches}}<option value="{{.}}">{{end}}</datalist> <label for="ctitle" class="mut">title</label><input id="ctitle" name="title" size="32"> <button type="submit" class="primary">Open change</button></form>
<div class="tbl"><table><thead><tr><th>#</th><th>Title</th><th>Source → target</th><th>State</th><th>Created</th></tr></thead><tbody>{{range .Changes}}<tr><td><a href="{{$.RepoPath}}/changes/{{.Number}}">{{.Number}}</a></td><td><a href="{{$.RepoPath}}/changes/{{.Number}}">{{.Title}}</a></td><td><code>{{.SourceRef}}</code> → <code>{{.TargetRef}}</code></td><td>{{template "status" .State}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{else}}<tr><td colspan="5" class="mut">No changes.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .Pager}}
{{template "foot" .}}{{end}}

{{define "stacks"}}{{template "head" .}}
<h1>Stacks</h1>
<p class="mut">A stack is an ordered list of changes, each targeting the branch below; it lands bottom-up with a server-side rebase.</p>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/stacks" class="stack">{{template "csrf" .CSRF}}<label for="branches">Branches, bottom first, one per line<textarea id="branches" name="branches" required></textarea></label><label for="target">Target ref (default: the default branch)<input id="target" name="target"></label><div><button type="submit" class="primary">Open stack</button></div></form>
<div class="tbl"><table><thead><tr><th>Stack</th><th>Target</th><th>Changes</th><th>State</th><th>Updated</th></tr></thead><tbody>{{range .Stacks}}<tr><td><a href="{{$.RepoPath}}/stacks/{{.ID}}">{{shortid .ID}}</a></td><td><code>{{.TargetRef}}</code></td><td>{{range .Changes}}<a href="{{$.RepoPath}}/changes/{{.Number}}">#{{.Number}}</a> {{template "status" .State}} {{end}}</td><td>{{template "status" .State}}{{if .Error}} <span class="bad">{{.Error}}</span>{{end}}</td><td class="mut">{{.UpdatedAt}}</td></tr>{{else}}<tr><td colspan="5" class="mut">No stacks.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .Pager}}
{{template "foot" .}}{{end}}

{{define "stack"}}{{template "head" .}}
<h1>Stack {{shortid .Stack.ID}} {{template "status" .Stack.State}}</h1>
<p class="mut">target <code>{{.Stack.TargetRef}}</code> · opened by {{template "actor" .Stack.CreatedBy}} {{.Stack.CreatedAt}}{{if .Stack.Error}} · <span class="bad">{{.Stack.Error}}</span>{{end}}</p>
<div class="tbl"><table><thead><tr><th>Order</th><th>Change</th><th>Source → target</th><th>State</th></tr></thead><tbody>{{range $i, $c := .Stack.Changes}}<tr><td>{{add $i 1}}</td><td><a href="{{$.RepoPath}}/changes/{{$c.Number}}">#{{$c.Number}} {{$c.Title}}</a></td><td><code>{{$c.SourceRef}}</code> → <code>{{$c.TargetRef}}</code></td><td>{{template "status" $c.State}}{{if and $.Stack.FailedChangeNumber (eq $.Stack.FailedChangeNumber $c.Number)}} <span class="pill bad">failed here</span>{{end}}</td></tr>{{end}}</tbody></table></div>
{{if ne .Stack.State "landed"}}<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/stacks/{{.Stack.ID}}/land" class="row">{{template "csrf" .CSRF}}<label for="strategy" class="mut">Land bottom-up with</label><select id="strategy" name="strategy">{{range .Strategies}}<option>{{.}}</option>{{end}}</select> <button type="submit" class="primary">Land stack</button></form>{{end}}
{{template "foot" .}}{{end}}

{{define "runs"}}{{template "head" .}}
<h1>Pipeline runs</h1>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/runs" class="row">{{template "csrf" .CSRF}}<label for="ref" class="mut">Run on</label><input id="ref" name="ref" value="{{.Repo.DefaultBranch}}" size="20"> <button type="submit" class="primary">Run pipeline</button></form>
<div class="tbl"><table><thead><tr><th>Run</th><th>Commit</th><th>Status</th><th>Change</th><th>Created</th><th>Finished</th></tr></thead><tbody>{{range .Runs}}<tr><td><a href="{{$.RepoPath}}/runs/{{.ID}}">{{shortid .ID}}</a></td><td><code>{{short .CommitOID}}</code></td><td>{{template "status" .Status}}</td><td class="mut">{{if .ChangeID}}{{shortid (deref .ChangeID)}}{{end}}</td><td class="mut">{{.CreatedAt}}</td><td class="mut">{{deref .FinishedAt}}</td></tr>{{else}}<tr><td colspan="6" class="mut">No runs.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .Pager}}
{{template "foot" .}}{{end}}

{{define "logview"}}<div class="row"><label for="logsearch" class="mut">Search the log</label><input id="logsearch" type="search" placeholder="type to filter lines" autocomplete="off"> <span id="logcount" class="mut"></span> <span id="log-live" class="live"></span>{{if .LogTruncated}}<span class="pill warn">page shows the first 2 MiB; the stream continues</span>{{end}}</div>
<pre id="log" class="log" tabindex="0" aria-live="polite" aria-label="log" data-log-stream="{{.StreamURL}}" data-offset="{{.LogOffset}}" data-live="{{if .Live}}1{{else}}0{{end}}">{{.Log}}</pre>{{end}}

{{define "run"}}{{template "head" .}}
<h1>Pipeline run {{shortid .Run.ID}} <span id="run-status">{{template "status" .Run.Status}}</span></h1>
<p class="mut">commit <code>{{.Run.CommitOID}}</code>{{with .Change}} · change <a href="{{$.RepoPath}}/changes/{{.Number}}">#{{.Number}} {{.Title}}</a>{{end}} · created {{.Run.CreatedAt}}{{if .Run.FinishedAt}} · finished {{deref .Run.FinishedAt}}{{end}}</p>
{{if .Plans}}<h2>Compiled plan</h2>{{range .Plans}}<div class="card"><p><b>{{.Source}}</b> <span class="mut">front-end {{.Frontend}}</span>{{if .Refused}} <span class="pill bad">refused: {{.Refused}}</span>{{end}}</p>
{{if .IR.Tasks}}<div class="tbl"><table><thead><tr><th>Task</th><th>Needs</th><th>Image</th><th>Caches</th></tr></thead><tbody>{{range .IR.Tasks}}<tr><td><code>{{.ID}}</code>{{if .Name}} {{.Name}}{{end}}</td><td class="mut">{{join .Needs ", "}}</td><td class="mut">{{.Executor.Image}}</td><td class="mut">{{range .Inputs.Caches}}<code>{{.Key}}</code> {{end}}</td></tr>{{end}}</tbody></table></div>{{end}}
{{if .Divergences}}<h3>Divergences ({{len .Divergences}})</h3><div class="tbl"><table><thead><tr><th>Code</th><th>Message</th><th>Where</th></tr></thead><tbody>{{range .Divergences}}<tr><td><code>{{.Code}}</code></td><td>{{.Message}}</td><td class="mut">{{.Location}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="mut">No divergences: fully supported.</p>{{end}}</div>{{end}}{{end}}
<h2>Tasks</h2>
<div class="tbl"><table><thead><tr><th>Task</th><th>Status</th><th>Exit</th><th>Duration</th><th>Cache</th><th>Hash</th><th>Notes</th></tr></thead><tbody>{{range .Tasks}}<tr><td><code>{{.ID}}</code>{{if .Name}} {{.Name}}{{end}}{{if .Image}}<div class="mut">{{.Image}}</div>{{end}}</td><td>{{template "status" .Status}}</td><td>{{.ExitCode}}</td><td>{{ms .DurationMS}}</td><td>{{if .CacheHit}}<span class="pill ok">hit</span>{{end}}</td><td class="mut"><code>{{short .ContentHash}}</code></td><td class="mut">{{.Reason}}{{range .Divergences}}<div><code>{{.Code}}</code> {{.Message}}</div>{{end}}{{range .Annotations}}<div class="sev-{{.Level}}">{{.Level}}: {{.Message}}{{if .File}} ({{.File}}:{{.Line}}){{end}}</div>{{end}}</td></tr>{{else}}<tr><td colspan="7" class="mut">No task records yet.</td></tr>{{end}}</tbody></table></div>
{{if .Artifacts}}<h2>Artifacts</h2><div class="tbl"><table><thead><tr><th>Name</th><th>Task</th><th>Files</th><th>Size</th><th>Digest</th></tr></thead><tbody>{{range .Artifacts}}<tr><td><a href="{{.Href}}" download>{{.Name}}</a></td><td class="mut">{{.TaskID}}</td><td>{{.Files}}</td><td>{{bytes .Size}}</td><td class="mut"><code>{{trunc .Digest 19}}</code></td></tr>{{end}}</tbody></table></div>{{end}}
<h2>Log</h2>
{{template "logview" .}}
{{template "foot" .}}{{end}}

{{define "agentruns"}}{{template "head" .}}
<h1>Agent runs</h1>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/agent" class="row">{{template "csrf" .CSRF}}<label for="prompt" class="mut">Prompt</label><input id="prompt" name="prompt" size="48" required> <button type="submit" class="primary">Run agent</button></form>
<div class="tbl"><table><thead><tr><th>Run</th><th>Adapter</th><th>Step</th><th>Status</th><th>Branch</th><th>Created</th><th>Error</th></tr></thead><tbody>{{range .Runs}}<tr><td><a href="{{$.RepoPath}}/agent/{{.ID}}">{{shortid .ID}}</a></td><td>{{.Adapter}}</td><td>{{.Step}}</td><td>{{template "status" .Status}}</td><td><code>{{.Branch}}</code></td><td class="mut">{{.CreatedAt}}</td><td class="mut">{{trunc .Error 80}}</td></tr>{{else}}<tr><td colspan="7" class="mut">No agent runs.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .Pager}}
{{template "foot" .}}{{end}}

{{define "agentrun"}}{{template "head" .}}
<h1>Agent run {{shortid .Run.ID}} <span id="run-status">{{template "status" .Run.Status}}</span></h1>
<p class="mut">adapter {{.Run.Adapter}} · step <b>{{.Run.Step}}</b>{{if .Run.Branch}} · branch <code>{{.Run.Branch}}</code>{{end}}{{with .Issue}} · issue <a href="{{$.RepoPath}}/issues/{{.Number}}">#{{.Number}}</a>{{end}}{{with .Change}} · change <a href="{{$.RepoPath}}/changes/{{.Number}}">#{{.Number}}</a>{{end}}{{if .Run.PipelineRunID}} · <a href="{{$.RepoPath}}/runs/{{deref .Run.PipelineRunID}}">pipeline run</a>{{end}}{{if .Run.DeploymentID}} · <a href="{{$.RepoPath}}/deployments/{{deref .Run.DeploymentID}}">deployment</a>{{end}}</p>
{{if .Run.Error}}<p class="flash bad">{{.Run.Error}}</p>{{end}}
<h2>Log</h2>
{{template "logview" .}}
{{template "foot" .}}{{end}}
`
