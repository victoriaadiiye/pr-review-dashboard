import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import History from '../components/History.vue'

describe('History', () => {
  it('renders a reviewer/PR row with points and a PR link', () => {
    const rows = [
      {
        reviewer: 'alice', display_name: 'Alice', repo: 'acme/widgets', pr_number: 42,
        title: 'Add caching', url: 'http://gh/pr/42', author: 'bob',
        points: 13, reviews: 2, states: ['APPROVED', 'COMMENTED'],
        last_submitted: '2026-06-15T10:00:00Z',
      },
    ]
    const wrapper = mount(History, { props: { rows } })
    const text = wrapper.text()
    expect(text).toContain('Alice')
    expect(text).toContain('Add caching')
    expect(text).toContain('13')
    expect(text).toContain('acme/widgets#42')
    const link = wrapper.find('a')
    expect(link.attributes('href')).toBe('http://gh/pr/42')
  })

  it('shows an empty state when there are no rows', () => {
    const wrapper = mount(History, { props: { rows: [] } })
    expect(wrapper.text()).toContain('No reviews in this window')
  })
})
