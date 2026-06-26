<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import Leaderboard from './components/Leaderboard.vue'
import Queue from './components/Queue.vue'
import History from './components/History.vue'

const windows = [
  { key: 'week', label: 'Week' },
  { key: 'month', label: 'Month' },
  { key: 'all', label: 'All time' },
] as const

const activeWindow = ref<'week' | 'month' | 'all'>('week')
const board = ref<any[]>([])
const queue = ref<any[]>([])
const error = ref<string>('')
const view = ref<'leaderboard' | 'queue' | 'history'>('leaderboard')
const history = ref<any[]>([])
const reviewers = ref<string[]>([])
const historyReviewer = ref<string>('')
const historyWindow = ref<'week' | 'month' | 'all'>('all')

const windowLabel = computed(() => windows.find((w) => w.key === activeWindow.value)?.label ?? '')

async function loadBoard() {
  try {
    const res = await fetch(`/api/leaderboard?window=${activeWindow.value}`)
    if (!res.ok) throw new Error(`leaderboard: HTTP ${res.status}`)
    board.value = await res.json()
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load leaderboard'
  }
}
async function loadQueue() {
  try {
    const res = await fetch('/api/queue')
    if (!res.ok) throw new Error(`queue: HTTP ${res.status}`)
    queue.value = await res.json()
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load queue'
  }
}
async function loadHistory() {
  try {
    const res = await fetch(`/api/history?window=${historyWindow.value}&reviewer=${encodeURIComponent(historyReviewer.value)}`)
    if (!res.ok) throw new Error(`history: HTTP ${res.status}`)
    history.value = await res.json()
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load history'
  }
}
async function loadReviewers() {
  try {
    const res = await fetch('/api/reviewers')
    if (!res.ok) throw new Error(`reviewers: HTTP ${res.status}`)
    reviewers.value = await res.json()
  } catch {
    reviewers.value = []
  }
}
onMounted(() => {
  loadBoard()
  loadQueue()
  loadHistory()
  loadReviewers()
})
watch(activeWindow, loadBoard)
watch([historyWindow, historyReviewer], loadHistory)
</script>

<template>
  <main class="page">
    <header class="masthead">
      <div class="title">
        <span class="mark">🏆</span>
        <div>
          <h1>PR Review Leaderboard</h1>
          <p class="tagline">Review work, scored on merge — quality over volume.</p>
        </div>
      </div>
      <div class="seg" role="tablist" aria-label="View">
        <button role="tab" :aria-selected="view === 'leaderboard'"
          :class="{ seg__opt: true, 'seg__opt--on': view === 'leaderboard' }"
          @click="view = 'leaderboard'">Leaderboard</button>
        <button role="tab" :aria-selected="view === 'queue'"
          :class="{ seg__opt: true, 'seg__opt--on': view === 'queue' }"
          @click="view = 'queue'">Review queue</button>
        <button role="tab" :aria-selected="view === 'history'"
          :class="{ seg__opt: true, 'seg__opt--on': view === 'history' }"
          @click="view = 'history'">History</button>
      </div>
    </header>

    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <template v-if="view === 'leaderboard'">
      <div class="leaderboard-controls">
        <div class="seg" role="tablist" aria-label="Leaderboard window">
          <button
            v-for="w in windows"
            :key="w.key"
            role="tab"
            :aria-selected="activeWindow === w.key"
            :class="{ 'seg__opt': true, 'seg__opt--on': activeWindow === w.key }"
            @click="activeWindow = w.key"
          >
            {{ w.label }}
          </button>
        </div>
      </div>

      <section class="card">
        <div class="card__head">
          <h2>Top reviewers</h2>
          <span class="card__meta">{{ windowLabel }}</span>
        </div>
        <Leaderboard :rows="board" />
      </section>
    </template>

    <section v-else-if="view === 'queue'" class="queue-view">
      <div class="card__head bare">
        <h2>Ready for review</h2>
        <span class="card__meta">{{ queue.length }} open</span>
      </div>
      <Queue :rows="queue" />
    </section>

    <section v-else class="history-view">
      <div class="history-controls">
        <select v-model="historyReviewer" class="reviewer-select" aria-label="Filter by reviewer">
          <option value="">All reviewers</option>
          <option v-for="rv in reviewers" :key="rv" :value="rv">{{ rv }}</option>
        </select>
        <div class="seg" role="tablist" aria-label="History window">
          <button v-for="w in windows" :key="w.key" role="tab"
            :aria-selected="historyWindow === w.key"
            :class="{ seg__opt: true, 'seg__opt--on': historyWindow === w.key }"
            @click="historyWindow = w.key">{{ w.label }}</button>
        </div>
      </div>
      <section class="card">
        <div class="card__head">
          <h2>Review history</h2>
          <span class="card__meta">{{ history.length }} rows</span>
        </div>
        <History :rows="history" />
      </section>
    </section>

    <footer class="foot">Updated continuously · scored at merge</footer>
  </main>
</template>

<style scoped>
.page {
  max-width: 60rem;
  margin: 0 auto;
  padding: var(--space-l) var(--gutter, var(--space-m)) var(--space-xl);
  display: flex;
  flex-direction: column;
  gap: var(--space-m);
}

.masthead {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-m);
  flex-wrap: wrap;
}

.title {
  display: flex;
  align-items: center;
  gap: var(--space-s);
}
.mark {
  font-size: var(--step-2);
  line-height: 1;
}
h1 {
  font-size: var(--step-2);
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--fg-strong);
}
.tagline {
  color: var(--fg-subtle);
  font-size: var(--step--1);
  margin-top: 2px;
}

/* Segmented control — static port of qompass .ff-seg */
.seg {
  display: inline-flex;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius-pill);
  padding: 3px;
  align-self: center;
}
.seg__opt {
  padding: 5px 16px;
  font: inherit;
  font-size: var(--step--1);
  font-weight: 500;
  color: var(--fg-muted);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  cursor: pointer;
  white-space: nowrap;
  transition:
    color var(--motion-fast) var(--motion-ease),
    background var(--motion-fast) var(--motion-ease);
}
.seg__opt:hover {
  color: var(--fg);
}
.seg__opt--on {
  color: var(--accent);
  font-weight: 600;
  background: var(--accent-bg);
  border-color: color-mix(in srgb, var(--accent) 30%, transparent);
}

.leaderboard-controls {
  display: flex;
  justify-content: flex-end;
}

.history-controls {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: var(--space-s);
  flex-wrap: wrap;
}
.reviewer-select {
  font: inherit;
  font-size: var(--step--1);
  color: var(--fg);
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius-pill);
  padding: 5px 12px;
}

.card {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow);
  overflow: hidden;
}
.card__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: var(--space-s) var(--space-m);
  border-bottom: 1px solid var(--border-subtle);
}
.card__head.bare {
  border-bottom: none;
  padding-left: 0;
  padding-right: 0;
}
.card__head h2 {
  font-size: var(--step-0);
  font-weight: 600;
  color: var(--fg-strong);
}
.card__meta {
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.error {
  color: var(--err);
  background: var(--err-bg);
  border: 1px solid color-mix(in srgb, var(--err) 30%, transparent);
  padding: var(--space-2xs) var(--space-s);
  border-radius: var(--radius-md);
  font-size: var(--step--1);
}

.foot {
  text-align: center;
  color: var(--fg-subtle);
  font-size: var(--step--2);
}
</style>
