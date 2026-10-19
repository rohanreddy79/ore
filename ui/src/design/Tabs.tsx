import type { ReactNode } from 'react'
import { Link, type LinkProps } from '@tanstack/react-router'
import { cx } from './primitives'

export type TabDef = { to: LinkProps['to']; params?: Record<string, string>; label: ReactNode; icon?: ReactNode; count?: number; exact?: boolean; search?: Record<string, unknown> }

/** Navigation tabs: links, so middle-click and the back button work. */
export function Tabs({ tabs, vertical, label }: { tabs: TabDef[]; vertical?: boolean; label: string }) {
  return (
    <nav className={cx('tabs', vertical && 'vertical')} aria-label={label}>
      {tabs.map((t, i) => (
        <Link
          key={i}
          to={t.to}
          params={t.params as never}
          search={t.search as never}
          className="tab"
          activeOptions={{ exact: t.exact ?? false, includeSearch: false }}
          activeProps={{ 'aria-current': 'page' }}
        >
          {t.icon}
          {t.label}
          {typeof t.count === 'number' ? <span className="count">{t.count}</span> : null}
        </Link>
      ))}
    </nav>
  )
}
