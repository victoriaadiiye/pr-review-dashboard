import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Queue from '../components/Queue.vue'

const base = {
  repo: 'acme/widgets', author: 'x', url: 'u', age_hours: 5, last_activity_hours: 1,
  additions: 1, deletions: 0, changed_files: 1, commits_since_review: 0,
  awaiting: true, tier: 'new', reviewers: [],
}

const rows = [
  { ...base, pr_number: 1, relation: 'todo_action' },
  { ...base, pr_number: 2, relation: 'todo_done' },
  { ...base, pr_number: 3, relation: 'other' },
  { ...base, pr_number: 4, relation: 'author' },
]

describe('Queue split', () => {
  it('splits into Your todo and All open when an account is selected', () => {
    const w = mount(Queue, { props: { rows, me: 'vic' } })
    const text = w.text()
    expect(text).toContain('Your todo')
    expect(text).toContain('All open')
    // 2 todo (action + done), 2 all-open (other + author)
    const heads = w.findAll('.sec-head .count').map((c) => c.text())
    expect(heads).toEqual(['2', '2'])
  })

  it('orders action items before already-reviewed in todo', () => {
    const w = mount(Queue, { props: { rows, me: 'vic' } })
    const firstSection = w.findAll('section')[0]
    const refs = firstSection.findAll('.ref').map((r) => r.text())
    // PR 1 (action) must come before PR 2 (done)
    expect(refs[0]).toContain('#1')
    expect(refs[1]).toContain('#2')
  })

  it('shows a single flat list when no account is selected', () => {
    const w = mount(Queue, { props: { rows, me: '' } })
    expect(w.text()).toContain('Ready for review')
    expect(w.text()).not.toContain('Your todo')
  })
})
