<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import Leaderboard from './components/Leaderboard.vue'
import Queue from './components/Queue.vue'

const activeWindow = ref<'week' | 'month' | 'all'>('week')
const board = ref<any[]>([])
const queue = ref<any[]>([])
const error = ref<string>('')

async function loadBoard() {
  try {
    board.value = await (await fetch(`/api/leaderboard?window=${activeWindow.value}`)).json()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load leaderboard'
  }
}
async function loadQueue() {
  try {
    queue.value = await (await fetch('/api/queue')).json()
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load queue'
  }
}
onMounted(() => { loadBoard(); loadQueue() })
watch(activeWindow, loadBoard)
</script>

<template>
  <main style="max-width: 880px; margin: 0 auto; font-family: system-ui;">
    <h1>🏆 PR Review Leaderboard</h1>
    <p v-if="error" class="error">{{ error }}</p>
    <div class="tabs">
      <button v-for="w in ['week','month','all']" :key="w"
        :class="{ active: activeWindow === w }" @click="activeWindow = w as any">{{ w }}</button>
    </div>
    <Leaderboard :rows="board" />
    <h2>📋 Ready for review</h2>
    <Queue :rows="queue" />
  </main>
</template>

<style scoped>
.tabs button { margin-right: 6px; padding: 4px 12px; cursor: pointer; }
.tabs button.active { font-weight: bold; text-decoration: underline; }
.error { color: #c0392b; background: #fdf0f0; padding: 8px 12px; border-radius: 4px; }
</style>
