import { useEffect, useState } from 'react'

export type Theme = 'auto' | 'light' | 'dark'
const KEY = 'ore.theme'

export function getTheme(): Theme {
  try {
    const t = localStorage.getItem(KEY)
    return t === 'light' || t === 'dark' ? t : 'auto'
  } catch {
    return 'auto'
  }
}

export function applyTheme(t: Theme) {
  const el = document.documentElement
  if (t === 'auto') el.removeAttribute('data-theme')
  else el.setAttribute('data-theme', t)
  try {
    if (t === 'auto') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, t)
  } catch {
    /* private mode */
  }
  window.dispatchEvent(new CustomEvent('ore:theme', { detail: t }))
}

export function useTheme(): [Theme, (t: Theme) => void] {
  const [theme, set] = useState<Theme>(() => getTheme())
  useEffect(() => {
    const on = (e: Event) => set((e as CustomEvent<Theme>).detail)
    window.addEventListener('ore:theme', on)
    return () => window.removeEventListener('ore:theme', on)
  }, [])
  return [theme, applyTheme]
}

/** The effective scheme right now (for CodeMirror and screenshots). */
export function effectiveScheme(): 'light' | 'dark' {
  const t = getTheme()
  if (t !== 'auto') return t
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}
