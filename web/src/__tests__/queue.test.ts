import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import QueuePanel from '../components/QueuePanel.vue'

const base = {
  repo: 'acme/widgets', pr_number: 7, title: 'Add fleet sync', author: 'alice',
  url: 'https://gh/7', age_hours: 100, last_activity_hours: 2,
  additions: 210, deletions: 18, changed_files: 4, commits_since_review: 0, awaiting: true,
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
  it('labels each PR with its tier badge', () => {
    expect(mount(QueuePanel, { props: { pr: { ...base, tier: 'new' } } }).text()).toContain('New')
    expect(mount(QueuePanel, { props: { pr: base } }).text()).toContain('Urgent')
    expect(mount(QueuePanel, { props: { pr: { ...base, tier: 'reviewed' } } }).text()).toContain('Reviewed')
  })
  it('notes new commits since last review only when there are any', () => {
    const withCommits = mount(QueuePanel, { props: { pr: { ...base, commits_since_review: 3 } } })
    expect(withCommits.text()).toMatch(/3 new commits since last review/i)
    const single = mount(QueuePanel, { props: { pr: { ...base, commits_since_review: 1 } } })
    expect(single.text()).toMatch(/1 new commit since last review/i)
    expect(mount(QueuePanel, { props: { pr: base } }).text()).not.toMatch(/since last review/i)
  })
})
