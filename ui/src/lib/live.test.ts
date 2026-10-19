import { describe, expect, it, vi } from 'vitest'
import { invalidateFor } from './live'
import type { LedgerEvent } from './types'

const ev = (type: string, subject: string): LedgerEvent => ({ specversion: '1.0', id: 'e', source: 's', type, subject, time: '', datacontenttype: 'application/json', oreactor: { kind: 'human', id: 'a' }, oreorg: 'o', data: {} })

describe('invalidateFor', () => {
  it('maps events to query families', () => {
    const qc = { invalidateQueries: vi.fn() }
    invalidateFor(qc, ev('io.ore.issue.opened', 'acme/app/issues/1'))
    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['repo', 'acme', 'app', 'issues'] })
    qc.invalidateQueries.mockClear()
    invalidateFor(qc, ev('io.ore.pipeline.run.finished', 'acme/app/runs/prun_1'))
    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['repo', 'acme', 'app', 'runs'] })
    qc.invalidateQueries.mockClear()
    invalidateFor(qc, ev('io.ore.repository.autonomy.changed', 'acme/app'))
    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['repo', 'acme', 'app'] })
    qc.invalidateQueries.mockClear()
    invalidateFor(qc, ev('io.ore.identity.token.created', 'users/alice'))
    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['identity'] })
  })
})
