import { describe, expect, it } from 'vitest'
import { excerpt, renderMarkdown } from './markdown'

const PAYLOADS = [
  `<script>alert(1)</script>`,
  `<img src=x onerror="alert(1)">`,
  `[click](javascript:alert(1))`,
  `<a href="javascript:alert(1)">x</a>`,
  `<iframe src="https://evil.example"></iframe>`,
  `<svg onload=alert(1)></svg>`,
  `<div style="background:url(javascript:alert(1))">x</div>`,
  `<form action="https://evil.example"><input name=p type=password></form>`,
  `<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`,
  `<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
  `<object data="x"></object><embed src="x">`,
  `<a href="https://ok.example" onclick="alert(1)">ok</a>`,
  `<base href="https://evil.example/">`,
  `<meta http-equiv="refresh" content="0;url=https://evil.example">`,
]

describe('renderMarkdown sanitization', () => {
  for (const p of PAYLOADS) {
    it(`renders inert: ${p.slice(0, 40)}`, () => {
      const html = renderMarkdown(`before ${p} after`)
      expect(html).not.toMatch(/<script/i)
      expect(html).not.toMatch(/on\w+=/i)
      expect(html).not.toMatch(/javascript:/i)
      expect(html).not.toMatch(/<(iframe|svg|object|embed|form|base|meta|math|style)/i)
      expect(html).not.toMatch(/style=/i)
      expect(html).not.toMatch(/data:text\/html/i)
      expect(html).toContain('before')
      expect(html).toContain('after')
    })
  }

  it('keeps GitHub-flavoured Markdown', () => {
    const html = renderMarkdown('# Title\n\n- [x] done\n- [ ] todo\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n`code` and ~~strike~~ and https://ore.example/x')
    expect(html).toContain('<h1')
    expect(html).toContain('<table')
    expect(html).toContain('<code>code</code>')
    expect(html).toContain('<del>strike</del>')
    expect(html).toContain('type="checkbox"')
    expect(html).toMatch(/<input[^>]*disabled/)
  })

  it('opens external links safely and leaves internal ones alone', () => {
    const html = renderMarkdown('[ext](https://example.com) [int](/app/acme/app) [rel](docs/guide.md)')
    expect(html).toMatch(/<a href="https:\/\/example.com"[^>]*target="_blank"[^>]*rel="noopener noreferrer nofollow"/)
    expect(html).toMatch(/<a href="\/app\/acme\/app">/)
    expect(html).toMatch(/<a href="docs\/guide.md">/)
  })

  it('strips unsafe image sources but keeps safe ones', () => {
    const html = renderMarkdown('![a](https://img.example/x.png) ![b](javascript:alert(1)) ![c](x.png)')
    expect(html).toContain('src="https://img.example/x.png"')
    expect(html).not.toContain('javascript:')
    expect(html).toContain('loading="lazy"')
  })

  it('returns empty for empty input', () => {
    expect(renderMarkdown('')).toBe('')
    expect(renderMarkdown(null)).toBe('')
  })

  it('makes a plain excerpt', () => {
    expect(excerpt('# Hello **world** `code` [link](http://x) and ![img](y)', 30)).toBe('Hello world code link and')
    expect(excerpt('a'.repeat(200), 20)).toHaveLength(20)
  })
})
