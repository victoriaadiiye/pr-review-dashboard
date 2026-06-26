import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import QueuePanel from '../components/QueuePanel.vue'

const base = {
  repo: 'acme/widgets', pr_number: 7, title: 'Add fleet sync', author: 'alice',
  url: 'https://gh/7', age_hours: 100, last_activity_hours: 2,
  additions: 210, deletions: 18, changed_files: 4, awaiting: true,
  tier: 'urgent', reviewers: [{ login: 'bob', status: 'pending', re_requested: true }],
}

describe('QueuePanel', () => {
  it('renders the PR ref, links to the PR, and tiers by urgency', () => {
    const w = mount(QueuePanel, { props: { pr: base } })
    expect(w.text()).toContain('acme/widgets#7')
    expect(w.get('a').attributes('href')).toBe('https://gh/7')
    expect(w.get('a').classes()).toContain('tier-urgent')
  })
  it('shows a re-requested badge and +/- size', () => {
    const w = mount(QueuePanel, { props: { pr: base } })
    expect(w.text()).toMatch(/re-?requested/i)
    expect(w.text()).toContain('+210')
    expect(w.text()).toContain('−18')
  })
  it('shows a NEW chip only for the new tier', () => {
    expect(mount(QueuePanel, { props: { pr: { ...base, tier: 'new' } } }).text()).toContain('NEW')
    expect(mount(QueuePanel, { props: { pr: base } }).text()).not.toContain('NEW')
  })
})
