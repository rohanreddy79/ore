import { BASE_URL, TOKEN } from '../playwright.config'

export async function api<T = unknown>(method: string, path: string, body?: unknown, base = BASE_URL, token = TOKEN): Promise<T> {
  const res = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
  const text = await res.text()
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text.slice(0, 300)}`)
  return (text ? JSON.parse(text) : undefined) as T
}

export const DEMO_FILES: Record<string, string> = {
  'go.mod': 'module hello\n\ngo 1.25\n',
  'main.go': `package main

import (
	"fmt"
	"net/http"
	"os"
)

func greeting() string { return "Hello, World!" }

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, greeting()) })
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.ListenAndServe(":"+port, nil)
}
`,
  'main_test.go': `package main

import "testing"

func TestGreeting(t *testing.T) {
	if got := greeting(); got != "Hello, Ore!" {
		t.Fatalf("greeting() = %q, want %q", got, "Hello, Ore!")
	}
}
`,
  '.ore/pipeline.yaml': `version: 0
tasks:
  - id: test
    image: golang:1.25-alpine
    run: go vet ./... && go test ./...
    env:
      GOTOOLCHAIN: local
      GOFLAGS: -buildvcs=false
`,
  'README.md': `# app

A tiny service used to demonstrate **Ore**.

- [x] pipeline
- [ ] greeting fixed by an agent

<script>alert('xss')</script>

> Markdown is sanitized before it renders.
`,
  'docs/guide.md': '# Guide\n\nHow to deploy the app.\n\n1. Build\n2. Ship\n3. Observe\n',
  'docs/old-name.md': '# Old name\n\nThis file is moved on a branch to exercise rename detection.\n',
  'docs/a-very-long-file-name-that-keeps-going-and-going-to-test-truncation-in-headers-trees-and-lists-0123456789.md': '# Long\n\nA long name.\n',
}

/** Waits for the page's main heading and a short quiet period; networkidle never settles while the ledger stream is open. */
export async function settle(page: import('@playwright/test').Page) {
  await page.locator('main h1, .login-card h1').first().waitFor({ state: 'visible' })
  await page.waitForTimeout(400)
}

/** Drops keyboard focus so single-key shortcuts apply to the page, without clicking anything. */
export async function unfocus(page: import('@playwright/test').Page) {
  await page.keyboard.press('Escape')
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
}
