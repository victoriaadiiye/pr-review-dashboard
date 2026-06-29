<script setup lang="ts">
import { computed } from 'vue'
import QueuePanel from './QueuePanel.vue'

const props = defineProps<{ rows: any[]; me: string }>()

const isTodo = (r: any) => r.relation === 'todo_action' || r.relation === 'todo_done'

// Todo: action items first, already-reviewed ones sink to the bottom (stable
// within each group, preserving the server's urgency order).
const todo = computed(() =>
  props.rows
    .filter(isTodo)
    .slice()
    .sort((a, b) => (a.relation === 'todo_action' ? 0 : 1) - (b.relation === 'todo_action' ? 0 : 1)),
)
const allOpen = computed(() => props.rows.filter((r) => !isTodo(r)))
</script>

<template>
  <!-- Personalized: split into Todo and All open. -->
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
.grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: var(--space-s);
}
@media (min-width: 48rem) {
  .grid {
    grid-template-columns: 1fr 1fr;
  }
}
.empty {
  grid-column: 1 / -1;
  padding: var(--space-l);
  text-align: center;
  color: var(--fg-subtle);
}
</style>
