// SPDX-License-Identifier: AGPL-3.0-only

package web

// The ledger page: filters, cursors, replay and the live tail.
const tmplLedger = `
{{define "events"}}{{template "head" .}}
<h1>Ledger</h1>
<p class="mut">Every transition in Ore is a CloudEvent written in the same transaction as the entity it describes. Head is sequence <b>{{.Latest}}</b>.</p>
<form method="get" action="/events" class="row"><label for="ftype" class="mut">type</label><input id="ftype" name="type" value="{{.Filter.Type}}" placeholder="io.ore.change. (prefix) or exact" size="26">
<label for="fsubject" class="mut">subject starts with</label><input id="fsubject" name="subject" value="{{.Filter.SubjectPrefix}}" placeholder="acme/app/" size="18">
<label for="fkind" class="mut">actor kind</label><select id="fkind" name="actor_kind"><option value="">any</option>{{range .Kinds}}<option{{if eq . $.Filter.ActorKind}} selected{{end}}>{{.}}</option>{{end}}</select>
<label for="forg" class="mut">org</label><input id="forg" name="org" value="{{.Org}}" size="10">
<label for="flimit" class="mut">per page</label><input id="flimit" name="limit" value="{{.Filter.Limit}}" size="4" inputmode="numeric">
<button type="submit">Filter</button> <a href="/events">reset</a></form>
<p class="pager">{{if .Newest}}<a href="{{.Newest}}">⇈ newest</a>{{end}}{{if .Newer}}<a href="{{.Newer}}">‹ newer</a>{{end}}{{if .Older}}<a href="{{.Older}}">older ›</a>{{end}}
{{if .Live}}<label class="mut"><input type="checkbox" id="live-toggle" checked> live tail</label> <span id="live-ind" class="live"></span>{{else}}<span class="mut">live tail is off while paging; go to newest to resume</span>{{end}}
{{if .Events}}<span class="mut">replay cursor: <a href="/api/v0/events?after={{.ReplayFrom}}{{if .Filter.Type}}&amp;type={{.Filter.Type}}{{end}}"><code>GET /api/v0/events?after={{.ReplayFrom}}</code></a></span>{{end}}</p>
<div class="tbl"><table><thead><tr><th>Seq</th><th>Time</th><th>Type</th><th>Subject</th><th>Actor</th><th>Data</th></tr></thead><tbody id="live-events"{{if .Live}} data-stream="/api/v0/events/stream?after={{.Latest}}{{if .StreamQuery}}&amp;{{.StreamQuery}}{{end}}" data-indicator="live-ind" data-toggle="live-toggle"{{end}}>{{template "eventrows" .Events}}{{if not .Events}}<tr><td colspan="6" class="mut">No events match.</td></tr>{{end}}</tbody></table></div>
<p class="pager">{{if .Newer}}<a href="{{.Newer}}">‹ newer</a>{{end}}{{if .Older}}<a href="{{.Older}}">older ›</a>{{end}}</p>
{{template "foot" .}}{{end}}
`
