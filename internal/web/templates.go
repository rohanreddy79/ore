// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// The UI is one template set: a layout (head/foot), small components
// (status pills, pagers, navigation, event rows) and one template per page.
// Everything is inline: no fonts, no scripts, no stylesheets from anywhere else.

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
	"join":  strings.Join,
	"add":   func(a, b int) int { return a + b },
	"deref": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"dur": func(sec float64) string {
		if sec <= 0 {
			return "–"
		}
		d := time.Duration(sec * float64(time.Second))
		switch {
		case d < time.Minute:
			return fmt.Sprintf("%.1fs", d.Seconds())
		case d < time.Hour:
			return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
		case d < 48*time.Hour:
			return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
		}
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	},
	"ms": func(ms int64) string {
		if ms <= 0 {
			return "–"
		}
		if ms < 1000 {
			return fmt.Sprintf("%dms", ms)
		}
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	},
	"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"bytes": func(n int64) string {
		switch {
		case n < 1<<10:
			return fmt.Sprintf("%d B", n)
		case n < 1<<20:
			return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
		}
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	},
	"json": func(v any) string {
		var raw []byte
		switch t := v.(type) {
		case json.RawMessage:
			raw = t
		case []byte:
			raw = t
		case string:
			raw = []byte(t)
		default:
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return fmt.Sprint(v)
			}
			return string(b)
		}
		var out bytes.Buffer
		if json.Indent(&out, raw, "", "  ") != nil {
			return string(raw)
		}
		return out.String()
	},
	"href": subjectHref,
	"deref_f": func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"deref_int": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"hasPrefix": strings.HasPrefix,
	"trunc": func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "…"
	},
	"level": func(i int) string {
		if i >= 0 && i < len(Levels) {
			return Levels[i]
		}
		return ""
	},
}

var tmpl = template.Must(template.New("ui").Funcs(funcs).Parse(uiTemplates + tmplRepo + tmplChange + tmplOps + tmplIdentity + tmplLedger))

const uiTemplates = `
{{define "csrf"}}<input type="hidden" name="csrf" value="{{.}}">{{end}}

{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="dark light">
<title>{{.Title}} · Ore</title>
<style>
:root{--bg:#0f1115;--fg:#e6e6e6;--mut:#9aa3ad;--line:#262b33;--acc:#f0a35e;--ok:#5cb85c;--bad:#e5484d;--warn:#e0b341;--card:#161a21;--code:#11151b;--focus:#7fb3ff}
@media (prefers-color-scheme: light){:root:not([data-theme=dark]){--bg:#fafafa;--fg:#161616;--mut:#5c6670;--line:#e3e6ea;--acc:#b8601c;--ok:#2e7d32;--bad:#c62828;--warn:#8a5d00;--card:#fff;--code:#f3f4f6;--focus:#1a5fb4}}
:root[data-theme=light]{--bg:#fafafa;--fg:#161616;--mut:#5c6670;--line:#e3e6ea;--acc:#b8601c;--ok:#2e7d32;--bad:#c62828;--warn:#8a5d00;--card:#fff;--code:#f3f4f6;--focus:#1a5fb4}
*{box-sizing:border-box}html{-webkit-text-size-adjust:100%}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 ui-sans-serif,system-ui,sans-serif}
a{color:var(--acc);text-decoration:none}a:hover,a:focus{text-decoration:underline}:focus-visible{outline:2px solid var(--focus);outline-offset:2px}
.skip{position:absolute;left:-999px;top:0;background:var(--card);padding:6px 10px;z-index:9}.skip:focus{left:8px}
header{display:flex;flex-wrap:wrap;gap:8px 16px;align-items:center;padding:8px 16px;border-bottom:1px solid var(--line)}
header b{font-size:18px;letter-spacing:.5px}header nav a{margin-right:12px}header .who{margin-left:auto;display:flex;gap:8px;align-items:center;font-size:14px}
main{max-width:1200px;margin:0 auto;padding:12px 16px}footer{max-width:1200px;margin:24px auto;padding:0 16px;font-size:13px}
h1{font-size:20px;margin:8px 0 12px;overflow-wrap:anywhere}h2{font-size:14px;margin:22px 0 8px;color:var(--mut);text-transform:uppercase;letter-spacing:.06em}h3{font-size:14px;margin:12px 0 6px}
.tbl{overflow-x:auto;-webkit-overflow-scrolling:touch}table{width:100%;border-collapse:collapse;font-size:14px}th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--mut);font-weight:500;white-space:nowrap}
code,pre,kbd{font:13px/1.45 ui-monospace,SFMono-Regular,Menlo,monospace}pre{background:var(--code);border:1px solid var(--line);padding:10px;overflow:auto;border-radius:6px;max-height:70vh;white-space:pre-wrap;overflow-wrap:anywhere}
pre.diff,pre.log{white-space:pre;max-height:none}pre.log{max-height:75vh}
.pill{display:inline-block;padding:0 8px;border-radius:999px;font-size:12px;border:1px solid var(--line);color:var(--mut);white-space:nowrap}
.ok{color:var(--ok);border-color:var(--ok)}.bad{color:var(--bad);border-color:var(--bad)}.warn{color:var(--warn);border-color:var(--warn)}
form{margin:0}form.inline{display:inline}form.stack{display:grid;gap:8px;max-width:640px;margin:8px 0}form.stack label{display:block;font-size:13px;color:var(--mut)}form.stack label>input,form.stack label>select,form.stack label>textarea{display:block;margin-top:3px;width:100%;color:var(--fg)}form.row{display:flex;flex-wrap:wrap;gap:6px;align-items:center;margin:6px 0}
button,input,select,textarea{font:inherit;padding:5px 10px;border-radius:6px;border:1px solid var(--line);background:var(--card);color:var(--fg);max-width:100%}button{cursor:pointer}button.primary{border-color:var(--acc);color:var(--acc)}button.danger{border-color:var(--bad);color:var(--bad)}textarea{width:100%;min-height:70px;font-family:ui-monospace,Menlo,monospace}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:10px;margin:10px 0}.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 12px}.card b{display:block;font-size:22px}.card .mut{font-size:13px}
.mut{color:var(--mut)}.flash{padding:8px 12px;border-radius:6px;border:1px solid var(--line);margin:8px 0}.flash.ok{border-color:var(--ok)}.flash.bad{border-color:var(--bad);color:var(--bad)}
.tabs ul,.files ul{list-style:none;margin:0;padding:0;display:flex;gap:2px;flex-wrap:nowrap;overflow-x:auto;border-bottom:1px solid var(--line)}.tabs a{display:block;padding:6px 10px;white-space:nowrap;border-bottom:2px solid transparent}.tabs a.on{border-bottom-color:var(--acc);color:var(--fg)}
.files ul{flex-wrap:wrap;border:0;gap:4px 14px;font-size:13px}.crumbs{margin:4px 0;font-size:14px}
.diff .a{color:var(--ok)}.diff .d{color:var(--bad)}.diff .h{color:var(--mut)}section.file{margin:12px 0}section.file h3{margin:8px 0 4px;overflow-wrap:anywhere}details.big>summary,details.lane>summary{cursor:pointer;padding:6px 8px;background:var(--card);border:1px solid var(--line);border-radius:6px}details.lane{margin:8px 0}
.comment{border:1px solid var(--line);border-radius:6px;padding:8px 10px;margin:6px 0;background:var(--card)}.comment .meta{font-size:13px;color:var(--mut);display:flex;gap:8px;flex-wrap:wrap}.comment .body{white-space:pre-wrap;overflow-wrap:anywhere;margin-top:4px}
.pager{display:flex;gap:12px;align-items:center;margin:10px 0;font-size:14px}.callout{border:1px solid var(--acc);border-radius:8px;padding:10px 12px;margin:10px 0}.callout code{user-select:all;overflow-wrap:anywhere}
dl.kv{display:grid;grid-template-columns:max-content 1fr;gap:4px 12px;margin:6px 0}dl.kv dt{color:var(--mut)}dl.kv dd{margin:0;overflow-wrap:anywhere}
.sev-critical,.sev-high{color:var(--bad)}.sev-medium{color:var(--warn)}.sev-low,.sev-info{color:var(--mut)}
.live{font-size:12px;color:var(--mut)}.live.on::before{content:"● ";color:var(--ok)}
@media (max-width:640px){header{padding:8px 10px}main{padding:8px 10px}h1{font-size:18px}th,td{padding:5px 6px}.cards{grid-template-columns:repeat(2,1fr)}}
</style>
<script>try{var t=localStorage.getItem('ore-theme');if(t)document.documentElement.setAttribute('data-theme',t)}catch(e){}</script>
</head><body><a class="skip" href="#main">Skip to content</a>
<header><b><a href="/">ORE</a></b><nav aria-label="Site"><a href="/">Repositories</a><a href="/events">Ledger</a><a href="/identity">Identity</a></nav>
<div class="who">{{if .Actor}}<span class="pill" title="signed in as">{{.Actor.Kind}}/{{.Actor.ID}}</span><form method="post" action="/logout" class="inline">{{template "csrf" .CSRF}}<button type="submit">Log out</button></form>{{else}}<a href="/login">Sign in</a>{{end}}<button type="button" id="theme" aria-label="Toggle dark or light theme" title="Toggle theme">◐</button></div></header>
<main id="main">{{if .Flash}}<p class="flash ok" role="status">{{.Flash}}</p>{{end}}{{if .FlashErr}}<p class="flash bad" role="alert">{{.FlashErr}}</p>{{end}}
{{if .Repo}}<p class="crumbs"><a href="/">Repositories</a> › <a href="{{.RepoPath}}">{{.Repo.Org}}/{{.Repo.Name}}</a> <span class="pill">L{{.Repo.AutonomyLevel}} {{level .Repo.AutonomyLevel}}</span></p>
<nav class="tabs" aria-label="Repository"><ul>{{range .Tabs}}<li><a href="{{.Href}}"{{if eq .Key $.Tab}} class="on" aria-current="page"{{end}}>{{.Name}}</a></li>{{end}}</ul></nav>{{end}}{{end}}

{{define "foot"}}</main><footer class="mut">Ore v0 · <a href="/api/v0/events">API</a> · <a href="/events">ledger</a></footer>
<script>
(function(){
var root=document.documentElement,btn=document.getElementById('theme');
if(btn){btn.addEventListener('click',function(){var cur=root.getAttribute('data-theme')||(window.matchMedia&&matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');var next=cur==='dark'?'light':'dark';root.setAttribute('data-theme',next);try{localStorage.setItem('ore-theme',next)}catch(e){}});}
function el(tag,cls,text){var e=document.createElement(tag);if(cls)e.className=cls;if(text!=null)e.textContent=text;return e;}
function href(s){var p=s.split('/');if(p.length<2)return '';if(p[0]==='users'||p[0]==='orgs')return '/identity/users';if(p[0]==='tokens')return '/identity/tokens';var b='/'+p[0]+'/'+p[1];if(p.length>=4&&/^(issues|changes|runs|deployments|agent|stacks)$/.test(p[2]))return b+'/'+p[2]+'/'+p[3];if(p.length>=3&&p[2]==='gateway')return b+'/gateway';if(p.length>=3&&p[2]==='environments')return b+'/environments';return b;}
function pill(st){var c='pill';if(/succeeded|merged|completed|closed|landed|healthy|ok/.test(st))c+=' ok';else if(/failed|error|stopped|refused|rejected|reverted/.test(st))c+=' bad';else if(/running|started|blocked|awaiting|verifying|queued/.test(st))c+=' warn';return c;}
document.querySelectorAll('[data-stream]').forEach(function(tb){
  var url=tb.getAttribute('data-stream'),es=null,box=document.getElementById(tb.getAttribute('data-indicator')||''),toggle=document.getElementById(tb.getAttribute('data-toggle')||'');
  function start(){if(es||!window.EventSource)return;es=new EventSource(url);if(box){box.classList.add('on');box.textContent='live';}
    es.onmessage=function(m){var ev;try{ev=JSON.parse(m.data)}catch(e){return}var tr=el('tr');tr.appendChild(el('td',null,String(ev.seq)));tr.appendChild(el('td','mut',ev.time));var tt=el('td');tt.appendChild(el('code',null,ev.type));tr.appendChild(tt);var ts=el('td');var h=href(ev.subject||'');if(h){var a=el('a',null,ev.subject);a.href=h;ts.appendChild(a);}else{ts.textContent=ev.subject;}tr.appendChild(ts);tr.appendChild(el('td','mut',(ev.oreactor||{}).kind+'/'+(ev.oreactor||{}).id));var d=el('td','mut');var det=el('details');det.appendChild(el('summary',null,'data'));var pre=el('pre',null,JSON.stringify(ev.data,null,2));det.appendChild(pre);d.appendChild(det);tr.appendChild(d);tb.insertBefore(tr,tb.firstChild);if(tb.children.length>500)tb.removeChild(tb.lastChild);};
    es.onerror=function(){if(box){box.classList.remove('on');box.textContent='reconnecting…';}};}
  function stop(){if(es){es.close();es=null;}if(box){box.classList.remove('on');box.textContent='paused';}}
  if(toggle){toggle.addEventListener('change',function(){toggle.checked?start():stop();});if(toggle.checked)start();}else start();
});
var log=document.getElementById('log');
if(log){var lines=log.textContent?log.textContent.replace(/\n$/,'').split('\n'):[],q='',search=document.getElementById('logsearch'),count=document.getElementById('logcount'),status=document.getElementById('run-status'),stick=true;
  function render(){var shown=lines;if(q){var lq=q.toLowerCase();shown=lines.filter(function(l){return l.toLowerCase().indexOf(lq)>=0});}log.textContent=shown.join('\n')+(shown.length?'\n':'');if(count)count.textContent=q?shown.length+' of '+lines.length+' lines':lines.length+' lines';if(stick)log.scrollTop=log.scrollHeight;}
  if(search){var t;search.addEventListener('input',function(){clearTimeout(t);t=setTimeout(function(){q=search.value;render();},60);});}
  log.addEventListener('scroll',function(){stick=log.scrollTop+log.clientHeight>=log.scrollHeight-8;});
  render();
  var surl=log.getAttribute('data-log-stream');
  if(surl&&log.getAttribute('data-live')==='1'&&window.EventSource){var off=log.getAttribute('data-offset')||'0';var es=new EventSource(surl+(surl.indexOf('?')>=0?'&':'?')+'offset='+off);var ind=document.getElementById('log-live');if(ind){ind.classList.add('on');ind.textContent='live';}
    es.onmessage=function(m){lines.push(m.data);if(q&&m.data.toLowerCase().indexOf(q.toLowerCase())<0){if(count)count.textContent=lines.length+' lines';return;}log.appendChild(document.createTextNode(m.data+'\n'));if(count)count.textContent=(q?'':'')+lines.length+' lines';if(stick)log.scrollTop=log.scrollHeight;};
    es.addEventListener('status',function(m){if(status){status.textContent=m.data;status.className=pill(m.data);}});
    es.addEventListener('done',function(m){if(status){status.textContent=m.data;status.className=pill(m.data);}if(ind){ind.classList.remove('on');ind.textContent='finished';}es.close();});
    es.onerror=function(){if(ind){ind.classList.remove('on');ind.textContent='reconnecting…';}};}
}
})();
</script></body></html>{{end}}

{{define "status"}}{{if or (eq . "succeeded") (eq . "merged") (eq . "completed") (eq . "closed") (eq . "landed") (eq . "approved") (eq . "ok")}}<span class="pill ok">{{.}}</span>{{else if or (eq . "failed") (eq . "error") (eq . "stopped") (eq . "refused") (eq . "rejected") (eq . "reverted") (eq . "revert_failed") (eq . "aborted") (eq . "rolled_back")}}<span class="pill bad">{{.}}</span>{{else if or (eq . "running") (eq . "blocked") (eq . "started") (eq . "awaiting_approval") (eq . "verifying") (eq . "queued") (eq . "landing") (eq . "deferred")}}<span class="pill warn">{{.}}</span>{{else}}<span class="pill">{{.}}</span>{{end}}{{end}}

{{define "pager"}}{{if gt .Pages 1}}<nav class="pager" aria-label="Pages">{{if .HasPrev}}<a href="{{.Prev}}">‹ previous</a>{{end}}<span class="mut">page {{.Page}} of {{.Pages}} · {{.From}}–{{.To}} of {{.Total}}</span>{{if .HasNext}}<a href="{{.Next}}">next ›</a>{{end}}</nav>{{else if .Total}}<p class="mut">{{.Total}} in total</p>{{end}}{{end}}

{{define "actor"}}<span class="mut">{{.Kind}}/{{.ID}}</span>{{end}}

{{define "eventrows"}}{{range .}}<tr><td>{{.Seq}}</td><td class="mut">{{.Time}}</td><td><code>{{.Type}}</code></td><td>{{with href .Subject}}<a href="{{.}}">{{end}}{{.Subject}}{{if href .Subject}}</a>{{end}}</td><td>{{template "actor" .OreActor}}</td><td class="mut"><details><summary>data</summary><pre>{{json .Data}}</pre></details></td></tr>{{end}}{{end}}

{{define "eventtable"}}<div class="tbl"><table><thead><tr><th>Seq</th><th>Time</th><th>Type</th><th>Subject</th><th>Actor</th><th>Data</th></tr></thead><tbody id="live-events" data-stream="{{.Stream}}" data-indicator="live-ind" data-toggle="live-toggle">{{template "eventrows" .Events}}</tbody></table></div>{{end}}

{{define "error"}}{{template "head" .}}
<h1>{{.Code}} {{.Title}}</h1>
<p>{{.Message}}</p>
<p><a href="/">Back to the repositories</a>{{if .Repo}} · <a href="{{.RepoPath}}">{{.Repo.Org}}/{{.Repo.Name}}</a>{{end}}</p>
{{template "foot" .}}{{end}}

{{define "login"}}{{template "head" .}}
<h1>Sign in</h1>
{{if .Actor}}<p>You are signed in as <b>{{.Actor.Kind}}/{{.Actor.ID}}</b>. <a href="{{.Next}}">Continue</a> or log out above.</p>{{end}}
{{if .Error}}<p class="flash bad" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/login" class="stack">{{template "csrf" .CSRF}}<input type="hidden" name="next" value="{{.Next}}">
<label for="token">Token (the admin token printed by <code>ore serve</code>, or a personal token from <a href="/identity/tokens">Identity</a>)<input id="token" name="token" type="password" autocomplete="current-password" required autofocus></label>
<div><button type="submit" class="primary">Sign in</button></div></form>
<p class="mut">The token is kept in an HttpOnly cookie on this browser only. Identity tokens and the admin token both work here, over the API and over git.</p>
{{template "foot" .}}{{end}}

{{define "index"}}{{template "head" .}}
<h1>Repositories</h1>
{{if not .Repos}}<p class="mut">No repositories yet. Create one with <code>ore repo create org/name</code> or run <code>ore demo</code>.</p>{{end}}
<div class="tbl"><table><thead><tr><th>Repository</th><th>Default branch</th><th>Autonomy</th><th>Created</th></tr></thead><tbody>
{{range .Repos}}<tr><td><a href="/{{.Org}}/{{.Name}}">{{.Org}}/{{.Name}}</a></td><td><code>{{.DefaultBranch}}</code></td><td>L{{.AutonomyLevel}} {{level .AutonomyLevel}}</td><td class="mut">{{.CreatedAt}}</td></tr>{{end}}</tbody></table></div>
<h2>Recent ledger events <span id="live-ind" class="live"></span></h2>
{{template "eventtable" .}}
{{template "foot" .}}{{end}}
`
