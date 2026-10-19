// SPDX-License-Identifier: AGPL-3.0-only

// Package webapp serves Ore's primary user interface: a TypeScript single-page
// application (ui/) built by Vite into dist/ and embedded in the binary. It
// mounts under /app/ with client-side routing, adds the few endpoints the app
// needs that the REST API does not have (a cookie session, server-sent event
// streams for the ledger and logs, commit details, search, the feature list)
// and registers itself through internal/registry as "webapp" without editing
// internal/web.
//
// On a checkout without Node the dist directory holds only .gitkeep; the
// package still builds and /app/ explains how to build the UI.
package webapp

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Prefix is where the application is mounted.
const Prefix = "/app/"

// Info is what the extension publishes as s.Services["webapp"] so that other
// packages (the server-rendered index, for one) can learn whether the app is
// built and where it lives.
type Info struct {
	Built  bool   `json:"built"`
	Prefix string `json:"prefix"`
}

// assets returns the built app as a file system rooted at dist/, and whether
// it contains an index.html (i.e. whether `npm run build` ran).
func assets() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}

// Built reports whether the UI is embedded in this binary.
func Built() bool {
	_, ok := assets()
	return ok
}

// appHandler serves the embedded app: hashed assets with long caching, the
// shell (index.html) for every client-side route, and the "build the UI" page
// when the app is not built.
func appHandler() http.Handler {
	files, built := assets()
	if !built {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(notBuiltPage))
		})
	}
	fileServer := http.FileServer(http.FS(files))
	index, _ := fs.ReadFile(files, "index.html")
	modTime := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, Prefix)
		rel = path.Clean("/" + rel)[1:]
		if rel != "" && rel != "index.html" {
			if st, err := fs.Stat(files, rel); err == nil && !st.IsDir() {
				if strings.HasPrefix(rel, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=300")
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + rel
				fileServer.ServeHTTP(w, r2)
				return
			}
			// A bundle-looking path that is not in the bundle (assets/… or a
			// top-level file such as favicon.svg) is a 404, not the shell: the
			// browser must not render HTML as a script. Deeper paths are client
			// routes even when they end in an extension (/app/o/r/blob/main/README.md).
			if (strings.HasPrefix(rel, "assets/") || !strings.Contains(rel, "/")) && isAssetExt(path.Ext(rel)) {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "index.html", modTime, strings.NewReader(string(index)))
	})
}

func isAssetExt(ext string) bool {
	switch ext {
	case ".js", ".mjs", ".css", ".map", ".png", ".jpg", ".jpeg", ".svg", ".ico", ".woff", ".woff2", ".json", ".txt", ".webmanifest":
		return true
	}
	return false
}

const notBuiltPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Build the UI · Ore</title>
<style>:root{color-scheme:light dark}body{margin:0;font:15px/1.6 ui-sans-serif,system-ui,sans-serif;background:#f7f6f3;color:#1b1a17;display:grid;place-items:center;min-height:100vh}
@media(prefers-color-scheme:dark){body{background:#121110;color:#ece9e3}}main{max-width:560px;padding:32px}h1{font-size:22px;margin:0 0 12px}p{margin:0 0 12px}
pre{background:rgba(127,127,127,.12);padding:12px 14px;border-radius:8px;overflow:auto;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}a{color:#c2410c}code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}</style></head>
<body><main><h1>The Ore web app is not built into this binary</h1>
<p>This binary was compiled from a checkout without the built UI. Build it once with Node.js 20.19 or newer, then rebuild <code>ore</code>:</p>
<pre>cd ui &amp;&amp; npm ci &amp;&amp; npm run build
cd .. &amp;&amp; CGO_ENABLED=0 go build -o bin/ore ./cmd/ore</pre>
<p>For development run <code>npm run dev</code> in <code>ui/</code>; it proxies the API to a running <code>ore serve</code>.</p>
<p>The server-rendered pages still work: <a href="/">repositories</a> · <a href="/events">ledger</a>.</p></main></body></html>`
