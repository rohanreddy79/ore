import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ActorChip, StatusPill, actorParts, statusTone } from './primitives'

describe('primitives', () => {
  it('maps statuses to tones', () => {
    expect(statusTone('succeeded')).toBe('ok')
    expect(statusTone('failed')).toBe('danger')
    expect(statusTone('running')).toBe('warn')
    expect(statusTone('whatever')).toBe('neutral')
  })
  it('splits actor ids with duplicated kind prefixes', () => {
    expect(actorParts({ kind: 'human', id: 'human/alice' })).toEqual({ kind: 'human', id: 'alice' })
    expect(actorParts('agent/fixer')).toEqual({ kind: 'agent', id: 'fixer' })
    expect(actorParts(undefined)).toEqual({ kind: 'human', id: 'unknown' })
  })
  it('renders agent chips distinctly', () => {
    render(<ActorChip actor={{ kind: 'agent', id: 'claude-code' }} />)
    expect(screen.getByText('claude-code')).toBeInTheDocument()
    expect(screen.getByText('agent')).toBeInTheDocument()
  })
  it('renders live statuses with a pulsing dot', () => {
    const { container } = render(<StatusPill status="running" />)
    expect(container.querySelector('.dot.pulse')).not.toBeNull()
  })
})
