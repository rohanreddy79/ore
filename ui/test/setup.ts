import '@testing-library/jest-dom/vitest'

// jsdom lacks a few browser APIs the app touches.
if (!('ResizeObserver' in globalThis)) {
  class RO {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  ;(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO
}
if (!('IntersectionObserver' in globalThis)) {
  class IO {
    constructor(_cb: unknown) {}
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return []
    }
    root = null
    rootMargin = ''
    thresholds = []
  }
  ;(globalThis as unknown as { IntersectionObserver: typeof IO }).IntersectionObserver = IO
}
if (typeof window.matchMedia !== 'function') {
  window.matchMedia = (q: string) =>
    ({ matches: false, media: q, onchange: null, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false }) as MediaQueryList
}
if (typeof Element.prototype.scrollTo !== 'function') {
  Element.prototype.scrollTo = function () {}
}
if (!document.createRange) {
  document.createRange = () => new Range()
}

// jsdom has no layout: give elements with an explicit inline size a rect so
// virtualizers (which require a non-zero viewport) can compute a window.
const origRect = HTMLElement.prototype.getBoundingClientRect
HTMLElement.prototype.getBoundingClientRect = function () {
  const r = origRect.call(this)
  const h = parseFloat(this.style?.height || '')
  const w = parseFloat(this.style?.width || '')
  if (Number.isFinite(h) || Number.isFinite(w)) {
    return { ...r, x: 0, y: 0, top: 0, left: 0, width: Number.isFinite(w) ? w : 1200, height: Number.isFinite(h) ? h : r.height, right: Number.isFinite(w) ? w : 1200, bottom: Number.isFinite(h) ? h : r.height, toJSON() {} } as DOMRect
  }
  return r
}
if (!('scrollIntoView' in Element.prototype) || typeof Element.prototype.scrollIntoView !== 'function') {
  Element.prototype.scrollIntoView = function () {}
}
if (typeof window.requestAnimationFrame !== 'function') {
  window.requestAnimationFrame = (cb: FrameRequestCallback) => setTimeout(() => cb(performance.now()), 0) as unknown as number
  window.cancelAnimationFrame = (id: number) => clearTimeout(id)
}

// The virtualizer reads clientWidth/clientHeight, which jsdom reports as 0.
for (const [prop, fallback] of [['clientHeight', 0], ['clientWidth', 1200]] as const) {
  Object.defineProperty(HTMLElement.prototype, prop, {
    configurable: true,
    get(this: HTMLElement) {
      const v = parseFloat((prop === 'clientHeight' ? this.style?.height : this.style?.width) || '')
      return Number.isFinite(v) ? v : fallback
    },
  })
}
