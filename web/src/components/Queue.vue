<script setup lang="ts">
defineProps<{ rows: Array<{
  repo: string; pr_number: number; title: string; author: string; url: string;
  age_hours: number; reviewers: Array<{ login: string; status: string }>;
}> }>()
const chip = (s: string) =>
  ({ approved: '✅', commented: '💬', changes: '🔴', pending: '⏳' } as Record<string, string>)[s] ?? '⏳'
</script>

<template>
  <ul class="queue">
    <li v-for="p in rows" :key="p.repo + p.pr_number">
      <a :href="p.url">{{ p.repo }}#{{ p.pr_number }}</a> — {{ p.title }}
      <span class="meta">by {{ p.author }}, {{ Math.round(p.age_hours) }}h old</span>
      <span class="reviewers">
        <span v-for="rv in p.reviewers" :key="rv.login">{{ chip(rv.status) }} {{ rv.login }}</span>
      </span>
    </li>
  </ul>
</template>

<style scoped>
.queue { list-style: none; padding: 0; }
.queue li { padding: 8px 0; border-bottom: 1px solid #eee; }
.meta { color: #888; font-size: 0.85em; margin-left: 6px; }
.reviewers { display: block; font-size: 0.9em; margin-top: 2px; }
.reviewers span { margin-right: 10px; }
</style>
