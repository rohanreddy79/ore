import { marked } from 'marked'
import DOMPurify from 'dompurify'

marked.setOptions({ gfm: true, breaks: false })

const ALLOWED_TAGS = [
  'a', 'abbr', 'b', 'blockquote', 'br', 'code', 'dd', 'del', 'details', 'div', 'dl', 'dt', 'em', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'i', 'img', 'input', 'ins', 'kbd', 'li', 'ol', 'p', 'pre', 'q', 's', 'samp', 'small', 'span', 'strong', 'sub', 'summary', 'sup', 'table', 'tbody', 'td', 'tfoot', 'th', 'thead', 'tr', 'u', 'ul',
]
const ALLOWED_ATTR = ['href', 'title', 'alt', 'src', 'align', 'class', 'type', 'checked', 'disabled', 'start', 'colspan', 'rowspan', 'open', 'id', 'aria-label', 'tabindex']

let hooked = false
function hook() {
  if (hooked) return
  hooked = true
  DOMPurify.addHook('afterSanitizeAttributes', (node) => {
    if (node.tagName === 'A') {
      const href = node.getAttribute('href') ?? ''
      if (/^(https?:)?\/\//i.test(href)) {
        node.setAttribute('target', '_blank')
        node.setAttribute('rel', 'noopener noreferrer nofollow')
      }
    }
    if (node.tagName === 'INPUT') {
      node.setAttribute('disabled', '')
      if (node.getAttribute('type') !== 'checkbox') node.remove()
      else {
        node.setAttribute('aria-label', node.hasAttribute('checked') ? 'Done' : 'To do')
        node.setAttribute('tabindex', '-1')
      }
    }
    if (node.tagName === 'IMG') {
      const src = node.getAttribute('src') ?? ''
      if (!/^(https?:\/\/|\/|data:image\/(png|gif|jpeg|webp);base64,)/i.test(src)) node.removeAttribute('src')
      node.setAttribute('loading', 'lazy')
    }
  })
}

/** renderMarkdown turns GitHub-flavoured Markdown into sanitized HTML. Scripts,
 * event handlers, javascript: URLs, forms and styles never survive. */
export function renderMarkdown(src: string | undefined | null): string {
  if (!src) return ''
  hook()
  const html = marked.parse(src, { async: false }) as string
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOW_DATA_ATTR: false,
    ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto):|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i,
    FORBID_TAGS: ['style', 'script', 'iframe', 'object', 'embed', 'form', 'svg', 'math', 'link', 'meta', 'base'],
    FORBID_ATTR: ['style', 'onerror', 'onload', 'onclick', 'srcset', 'formaction', 'xlink:href'],
    USE_PROFILES: { html: true },
  })
}

/** Plain-text excerpt for list rows and titles. */
export function excerpt(src: string | undefined | null, n = 140): string {
  if (!src) return ''
  const text = src
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/[#>*_~-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  return text.length > n ? text.slice(0, n - 1) + '…' : text
}
