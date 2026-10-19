// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"html/template"
	"strings"
)

var funcs = template.FuncMap{
	"short": func(s string) string {
		if len(s) > 10 {
			return s[:10]
		}
		return s
	},
	"shortid": func(s string) string {
		if i := strings.IndexByte(s, '_'); i >= 0 && len(s) > i+7 {
			return s[:i+7]
		}
		return s
	},
	"lower": strings.ToLower,
}

var tmpl = template.Must(template.New("ui").Funcs(funcs).Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Ore</title>
<style>
:root{--bg:#0f1115;--fg:#e6e6e6;--mut:#9aa3ad;--line:#262b33;--acc:#f0a35e;--ok:#4caf50;--bad:#e5484d;--card:#161a21}
@media (prefers-color-scheme: light){:root{--bg:#fafafa;--fg:#161616;--mut:#5c6670;--line:#e3e6ea;--acc:#b8601c;--card:#fff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 ui-sans-serif,system-ui,sans-serif}
a{color:var(--acc);text-decoration:none}a:hover{text-decoration:underline}
header{display:flex;gap:16px;align-items:center;padding:10px 20px;border-bottom:1px solid var(--line)}
header b{font-size:18px;letter-spacing:.5px}main{max-width:1100px;margin:0 auto;padding:16px 20px}
h1{font-size:22px;margin:8px 0 16px}h2{font-size:16px;margin:24px 0 8px;color:var(--mut);text-transform:uppercase;letter-spacing:.06em}
table{width:100%;border-collapse:collapse;font-size:14px}th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--mut);font-weight:500}
code,pre{font:13px/1.45 ui-monospace,SFMono-Regular,Menlo,monospace}pre{background:var(--card);border:1px solid var(--line);padding:12px;overflow:auto;border-radius:6px;max-height:70vh}
.pill{display:inline-block;padding:1px 8px;border-radius:999px;font-size:12px;border:1px solid var(--line);color:var(--mut)}
.ok{color:var(--ok);border-color:var(--ok)}.bad{color:var(--bad);border-color:var(--bad)}.warn{color:var(--acc);border-color:var(--acc)}
form.inline{display:inline}button,input,select{font:inherit;padding:4px 10px;border-radius:6px;border:1px solid var(--line);background:var(--card);color:var(--fg)}button{cursor:pointer}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px}.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px}
.card b{display:block;font-size:24px}.mut{color:var(--mut)}.diff{white-space:pre}.diff .a{color:var(--ok)}.diff .d{color:var(--bad)}.diff .h{color:var(--mut)}
</style></head><body><header><b><a href="/">ORE</a></b><span class="mut">the autonomous software forge · v0</span><span style="flex:1"></span><a href="/events">ledger</a></header><main>{{end}}
{{define "foot"}}</main></body></html>{{end}}

{{define "status"}}{{if or (eq . "succeeded") (eq . "merged") (eq . "completed") (eq . "closed")}}<span class="pill ok">{{.}}</span>{{else if or (eq . "failed") (eq . "error") (eq . "stopped")}}<span class="pill bad">{{.}}</span>{{else if or (eq . "running") (eq . "blocked") (eq . "started")}}<span class="pill warn">{{.}}</span>{{else}}<span class="pill">{{.}}</span>{{end}}{{end}}

{{define "index"}}{{template "head" .}}
<h1>Repositories</h1>
{{if not .Repos}}<p class="mut">No repositories yet. Create one with <code>ore repo create org/name</code> or run <code>ore demo</code>.</p>{{end}}
<table><tr><th>Repository</th><th>Default branch</th><th>Autonomy</th><th>Created</th></tr>
{{range .Repos}}<tr><td><a href="/{{.Org}}/{{.Name}}">{{.Org}}/{{.Name}}</a></td><td><code>{{.DefaultBranch}}</code></td><td>L{{.AutonomyLevel}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{end}}</table>
<h2>Recent ledger events</h2>
<table><tr><th>Time</th><th>Type</th><th>Subject</th><th>Actor</th></tr>
{{range .Events}}<tr><td class="mut">{{.Time}}</td><td><code>{{.Type}}</code></td><td>{{.Subject}}</td><td class="mut">{{.OreActor.Kind}}/{{.OreActor.ID}}</td></tr>{{end}}</table>
{{template "foot" .}}{{end}}

{{define "repo"}}{{template "head" .}}
<h1>{{.Repo.Org}}/{{.Repo.Name}} <span class="pill">L{{.Repo.AutonomyLevel}} {{index .Levels .Repo.AutonomyLevel}}</span></h1>
<p class="mut">clone: <code>git clone {{.BaseURL}}/{{.Repo.Org}}/{{.Repo.Name}}.git</code> · default branch <code>{{.Repo.DefaultBranch}}</code> is protected: it moves only through changes.</p>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/level" class="inline">Autonomy level:
<select name="level">{{range $i, $n := .Levels}}<option value="{{$i}}" {{if eq $i $.Repo.AutonomyLevel}}selected{{end}}>L{{$i}} {{$n}}</option>{{end}}</select>
<input name="reason" placeholder="reason" size="24"> <button>Set</button></form>
<div class="cards" style="margin-top:12px">
<div class="card"><span class="mut">open issues</span><b>{{.OpenIssues}}</b></div><div class="card"><span class="mut">open changes</span><b>{{.OpenChanges}}</b></div>
<div class="card"><span class="mut">pipeline runs</span><b>{{len .Runs}}</b></div><div class="card"><span class="mut">deployments</span><b>{{len .Deployments}}</b></div></div>

<h2>Issues</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/issues" class="inline"><input name="title" placeholder="new issue title" size="40"> <input name="body" placeholder="details" size="40"> <button>File issue</button></form>
<table><tr><th>#</th><th>Title</th><th>State</th><th></th></tr>
{{range .Issues}}<tr><td>{{.Number}}</td><td>{{.Title}}<div class="mut">{{.Body}}</div></td><td>{{template "status" .State}}</td>
<td>{{if eq .State "open"}}<form method="post" action="/ui/{{$.Repo.Org}}/{{$.Repo.Name}}/agent" class="inline"><input type="hidden" name="issue" value="{{.Number}}"><button>Run agent</button></form>{{end}}</td></tr>{{end}}</table>

<h2>Changes</h2>
<table><tr><th>#</th><th>Title</th><th>Source → target</th><th>State</th><th>Provenance</th></tr>
{{range .Changes}}<tr><td><a href="/{{$.Repo.Org}}/{{$.Repo.Name}}/changes/{{.Number}}">{{.Number}}</a></td><td>{{.Title}}</td><td><code>{{.SourceRef}}</code> → <code>{{.TargetRef}}</code></td><td>{{template "status" .State}}</td><td class="mut"><code>{{.Provenance}}</code></td></tr>{{end}}</table>

<h2>Pipeline runs</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/runs" class="inline"><input name="ref" value="{{.Repo.DefaultBranch}}" size="16"> <button>Run pipeline</button></form>
<table><tr><th>Run</th><th>Commit</th><th>Status</th><th>Tasks</th><th>Created</th></tr>
{{range .Runs}}<tr><td><a href="/{{$.Repo.Org}}/{{$.Repo.Name}}/runs/{{.ID}}">{{shortid .ID}}</a></td><td><code>{{short .CommitOID}}</code></td><td>{{template "status" .Status}}</td><td class="mut"><code>{{.Tasks}}</code></td><td class="mut">{{.CreatedAt}}</td></tr>{{end}}</table>

<h2>Deployments</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/deploy" class="inline"><input name="ref" value="{{.Repo.DefaultBranch}}" size="16"> <select name="environment"><option>preview</option><option>production</option></select> <button>Deploy</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/rollback" class="inline"><input type="hidden" name="environment" value="production"><button>Roll back production</button></form>
<table><tr><th>Deployment</th><th>Env</th><th>Commit</th><th>Status</th><th>URL</th><th>Actor</th></tr>
{{range .Deployments}}<tr><td>{{shortid .ID}}</td><td>{{.Environment}}</td><td><code>{{short .CommitOID}}</code></td><td>{{template "status" .Status}}</td><td>{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{end}}</td><td class="mut">{{.Actor.Kind}}/{{.Actor.ID}}</td></tr>{{end}}</table>

<h2>Agent runs</h2>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/agent" class="inline"><input name="prompt" placeholder="prompt for the agent" size="60"> <button>Run agent</button></form>
<table><tr><th>Run</th><th>Adapter</th><th>Step</th><th>Status</th><th>Branch</th><th>Error</th></tr>
{{range .AgentRuns}}<tr><td><a href="/{{$.Repo.Org}}/{{$.Repo.Name}}/agent/{{.ID}}">{{shortid .ID}}</a></td><td>{{.Adapter}}</td><td>{{.Step}}</td><td>{{template "status" .Status}}</td><td><code>{{.Branch}}</code></td><td class="mut">{{.Error}}</td></tr>{{end}}</table>
{{template "foot" .}}{{end}}

{{define "change"}}{{template "head" .}}
<p><a href="/{{.Repo.Org}}/{{.Repo.Name}}">{{.Repo.Org}}/{{.Repo.Name}}</a></p>
<h1>Change #{{.Change.Number}}: {{.Change.Title}} {{template "status" .Change.State}}</h1>
<p class="mut">change-id <code>{{.Change.ChangeID}}</code> · <code>{{.Change.SourceRef}}</code> → <code>{{.Change.TargetRef}}</code> · base <code>{{short .Change.BaseOID}}</code> head <code>{{short .Change.HeadOID}}</code>{{if .Change.MergeOID}} · merged as <code>{{short .Change.MergeOID}}</code>{{end}} · provenance <code>{{.Change.Provenance}}</code></p>
{{if eq .Change.State "open"}}<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/changes/{{.Change.Number}}/merge" class="inline"><button>Merge server-side</button></form>
<form method="post" action="/ui/{{.Repo.Org}}/{{.Repo.Name}}/changes/{{.Change.Number}}/verify" class="inline"><button>Run pipeline on head</button></form>{{end}}
{{if .Change.Body}}<pre>{{.Change.Body}}</pre>{{end}}
<h2>Verification</h2>
<table><tr><th>Run</th><th>Status</th><th>Tasks</th></tr>{{range .Runs}}<tr><td><a href="/{{$.Repo.Org}}/{{$.Repo.Name}}/runs/{{.ID}}">{{shortid .ID}}</a></td><td>{{template "status" .Status}}</td><td class="mut"><code>{{.Tasks}}</code></td></tr>{{end}}</table>
<h2>Diff</h2>
<pre class="diff">{{range .DiffLines}}<span class="{{.Class}}">{{.Text}}</span>
{{end}}</pre>
{{template "foot" .}}{{end}}

{{define "log"}}{{template "head" .}}
<p><a href="/{{.Repo.Org}}/{{.Repo.Name}}">{{.Repo.Org}}/{{.Repo.Name}}</a></p>
<h1>{{.Title}} {{template "status" .Status}}</h1>
{{if .Error}}<p class="bad">{{.Error}}</p>{{end}}
<pre>{{.Log}}</pre>
{{template "foot" .}}{{end}}

{{define "events"}}{{template "head" .}}
<h1>Ledger</h1>
<p class="mut">Every transition in Ore is a CloudEvent written in the same transaction as the entity it describes. Replay from any sequence number with <code>GET /api/v0/events?after=N</code>.</p>
<table><tr><th>Seq</th><th>Time</th><th>Type</th><th>Subject</th><th>Actor</th><th>Data</th></tr>
{{range .Events}}<tr><td>{{.Seq}}</td><td class="mut">{{.Time}}</td><td><code>{{.Type}}</code></td><td>{{.Subject}}</td><td class="mut">{{.OreActor.Kind}}/{{.OreActor.ID}}</td><td class="mut"><code>{{printf "%s" .Data}}</code></td></tr>{{end}}</table>
{{template "foot" .}}{{end}}
`))
