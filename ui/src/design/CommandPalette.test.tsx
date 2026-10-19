import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CommandPalette, paletteKey, rankCommands, type Command } from './CommandPalette'

const cmds: Command[] = [
  { id: 'issues', label: 'Issues', group: 'Go to', run: vi.fn() },
  { id: 'new-issue', label: 'New issue', group: 'Create', run: vi.fn() },
  { id: 'ledger', label: 'Ledger', group: 'Go to', keywords: 'events audit', run: vi.fn() },
  { id: 'theme-dark', label: 'Theme: dark', group: 'Preferences', run: vi.fn() },
]

describe('rankCommands', () => {
  it('returns everything for an empty query', () => {
    expect(rankCommands(cmds, '').map((c) => c.id)).toEqual(['issues', 'new-issue', 'ledger', 'theme-dark'])
  })
  it('prefers prefix, then substring, then keywords, then fuzzy', () => {
    expect(rankCommands(cmds, 'iss').map((c) => c.id)).toEqual(['issues', 'new-issue'])
    expect(rankCommands(cmds, 'audit').map((c) => c.id)).toEqual(['ledger'])
    expect(rankCommands(cmds, 'tdk').map((c) => c.id)).toEqual(['theme-dark'])
    expect(rankCommands(cmds, 'zzz')).toEqual([])
  })
})

describe('paletteKey', () => {
  it('wraps around and reports actions', () => {
    expect(paletteKey('ArrowDown', 2, 3)).toEqual({ index: 0 })
    expect(paletteKey('ArrowUp', 0, 3)).toEqual({ index: 2 })
    expect(paletteKey('Enter', 1, 3)).toEqual({ index: 1, action: 'run' })
    expect(paletteKey('Escape', 1, 3)).toEqual({ index: 1, action: 'close' })
    expect(paletteKey('Home', 2, 3)).toEqual({ index: 0 })
    expect(paletteKey('End', 0, 3)).toEqual({ index: 2 })
    expect(paletteKey('ArrowDown', 0, 0)).toEqual({ index: 0 })
  })
})

describe('<CommandPalette>', () => {
  it('filters as you type and runs the selected command with the keyboard', async () => {
    const onClose = vi.fn()
    render(<CommandPalette open onClose={onClose} commands={cmds} />)
    const input = await screen.findByRole('combobox')
    fireEvent.change(input, { target: { value: 'led' } })
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(1))
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(cmds[2]!.run).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })
  it('merges remote search results without duplicates', async () => {
    const remote = vi.fn(async () => [{ id: 'issues', label: 'Issues', group: 'Go to', run: vi.fn() }, { id: 'hit:1', label: '#1 Retry', group: 'Issues in acme/app', run: vi.fn() }])
    render(<CommandPalette open onClose={() => {}} commands={cmds} search={remote} />)
    const input = await screen.findByRole('combobox')
    fireEvent.change(input, { target: { value: 'iss' } })
    await waitFor(() => expect(remote).toHaveBeenCalled())
    await waitFor(() => expect(screen.getAllByRole('option').map((o) => o.textContent)).toContain('#1 Retry'))
    // The remote copy of "Issues" is dropped because the local command already matched.
    expect(screen.getAllByRole('option').filter((o) => o.textContent === 'Issues')).toHaveLength(1)
  })
  it('closes on Escape', async () => {
    const onClose = vi.fn()
    render(<CommandPalette open onClose={onClose} commands={cmds} />)
    fireEvent.keyDown(await screen.findByRole('combobox'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })
})
