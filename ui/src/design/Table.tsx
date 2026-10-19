import type { ReactNode } from 'react'
import { cx } from './primitives'

export type Column<T> = { key: string; header: ReactNode; render: (row: T) => ReactNode; align?: 'left' | 'right'; width?: string; hideSm?: boolean }

/** A table with a sticky header. Keep it for truly tabular data; use .list rows for navigable lists. */
export function Table<T>({ columns, rows, rowKey, dense, empty, caption, onRowClick }: { columns: Column<T>[]; rows: T[]; rowKey: (row: T) => string; dense?: boolean; empty?: ReactNode; caption?: string; onRowClick?: (row: T) => void }) {
  return (
    <div className="table-wrap">
      <table className={cx('table', dense && 'dense')}>
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} scope="col" className={cx(c.align === 'right' && 'num', c.hideSm && 'hide-sm')} style={{ width: c.width }}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && empty ? (
            <tr>
              <td colSpan={columns.length}>{empty}</td>
            </tr>
          ) : null}
          {rows.map((r) => (
            <tr key={rowKey(r)} onClick={onRowClick ? () => onRowClick(r) : undefined} style={onRowClick ? { cursor: 'pointer' } : undefined}>
              {columns.map((c) => (
                <td key={c.key} className={cx(c.align === 'right' && 'num', c.hideSm && 'hide-sm')}>
                  {c.render(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
