<script setup lang="ts">
import { computed } from 'vue'
import QueuePanel from './QueuePanel.vue'

const props = defineProps<{ rows: any[]; me: string }>()

// Todo: only PRs still waiting on my action. Once I've reviewed one
// (todo_done) it drops out of my todo and lives in All open below.
const isTodo = (r: any) => r.relation === 'todo_action'
const isMine = (r: any) => r.relation === 'author'

const todo = computed(() => props.rows.filter(isTodo))
const myOpen = computed(() => props.rows.filter(isMine))
const allOpen = computed(() => props.rows.filter((r) => !isTodo(r) && !isMine(r)))
</script>

<template>
  <!-- Personalized: split into Todo, My open PRs, and All open. -->
  <div v-if="me" class="sections">
    <section>
      <div class="sec-head">
        <h2>Your todo</h2>
        <span class="count">{{ todo.length }}</span>
      </div>
      <div class="grid">
        <QueuePanel v-for="p in todo" :key="p.repo + p.pr_number" :pr="p" />
        <p v-if="todo.length === 0" class="empty">✅ Nothing waiting on you right now.</p>
      </div>
    </section>

    <section>
      <div class="sec-head">
        <h2>My open PRs</h2>
        <span class="count">{{ myOpen.length }}</span>
      </div>
      <div class="grid">
        <QueuePanel v-for="p in myOpen" :key="p.repo + p.pr_number" :pr="p" />
        <p v-if="myOpen.length === 0" class="empty">No open PRs authored by you.</p>
      </div>
    </section>

    <section>
      <div class="sec-head">
        <h2>All open</h2>
        <span class="count">{{ allOpen.length }}</span>
      </div>
      <div class="grid">
        <QueuePanel v-for="p in allOpen" :key="p.repo + p.pr_number" :pr="p" />
        <p v-if="allOpen.length === 0" class="empty">Nothing else open.</p>
      </div>
    </section>
  </div>

  <!-- No account selected: one flat list. -->
  <div v-else class="sections">
    <section>
      <div class="sec-head">
        <h2>Ready for review</h2>
        <span class="count">{{ rows.length }}</span>
      </div>
      <div class="grid">
        <QueuePanel v-for="p in rows" :key="p.repo + p.pr_number" :pr="p" />
        <p v-if="rows.length === 0" class="empty">✅ Nothing waiting — the queue is clear.</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.sections {
  display: flex;
  flex-direction: column;
  gap: var(--space-l);
}
.sec-head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2xs);
  margin-bottom: var(--space-s);
  padding-bottom: var(--space-2xs);
  border-bottom: 1px solid var(--border-subtle);
}
.sec-head h2 {
  font-size: var(--step-0);
  font-weight: 600;
  color: var(--fg-strong);
}
.count {
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
  background: var(--bg-sub);
  border-radius: var(--radius-pill);
  padding: 1px 8px;
}
/* Fluid columns: cards keep a comfortable min width and the track count flows
   with available space instead of snapping at one hard breakpoint. align-items
   keeps each card its natural height (no stretch-whitespace in shorter cards). */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 21rem), 1fr));
  gap: var(--space-s);
  align-items: start;
}
.empty {
  grid-column: 1 / -1;
  padding: var(--space-l);
  text-align: center;
  color: var(--fg-subtle);
}
</style>
