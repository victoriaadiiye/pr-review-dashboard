<script setup lang="ts">
import QueuePanel from './QueuePanel.vue'
defineProps<{ rows: any[] }>()
</script>

<template>
  <div class="grid">
    <QueuePanel v-for="p in rows" :key="p.repo + p.pr_number" :pr="p" />
    <p v-if="rows.length === 0" class="empty">✅ Nothing waiting — the queue is clear.</p>
  </div>
</template>

<style scoped>
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
