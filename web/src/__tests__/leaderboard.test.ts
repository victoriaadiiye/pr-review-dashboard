import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import Leaderboard from '../components/Leaderboard.vue'

describe('Leaderboard', () => {
  it('renders ranked rows and flags guests', () => {
    const rows = [
      { login: 'alice', display_name: 'Alice', team: 'member', is_guest: false, points: 13, reviews: 2, avg_points_per_review: 6.5, rank: 1 },
      { login: 'dave', display_name: 'Dave', team: 'guest', is_guest: true, points: 5, reviews: 1, avg_points_per_review: 5, rank: 2 },
    ]
    const wrapper = mount(Leaderboard, { props: { rows } })
    const text = wrapper.text()
    expect(text).toContain('Alice')
    expect(text).toContain('13')
    expect(wrapper.find('.guest').exists()).toBe(true)
  })
})
