<script setup lang="ts">
defineProps<{
  rows: Array<{
    repo: string
    pr_number: number
    title: string
    author: string
    url: string
    age_hours: number
    reviewers: Array<{ login: string; status: string }>
  }>
}>()

const STATUS: Record<string, { icon: string; cls: string; label: string }> = {
  approved: { icon: '✓', cls: 'ok', label: 'approved' },
  changes: { icon: '±', cls: 'err', label: 'changes' },
  commented: { icon: '💬', cls: 'comment', label: 'commented' },
  pending: { icon: '○', cls: 'pending', label: 'pending' },
}
const meta = (s: string) => STATUS[s] ?? STATUS.pending
</script>

<template>
  <ul class="q">
    <li v-for="p in rows" :key="p.repo + p.pr_number" class="q__row">
      <div class="q__main">
        <a class="q__ref" :href="p.url" target="_blank" rel="noopener">{{ p.repo }}#{{ p.pr_number }}</a>
        <span class="q__title">{{ p.title }}</span>
      </div>
      <div class="q__sub">
        <span class="q__by">{{ p.author }}</span>
        <span class="q__age" :class="{ stale: p.age_hours > 48 }">{{ Math.round(p.age_hours) }}h</span>
        <span class="q__rev">
          <span v-for="rv in p.reviewers" :key="rv.login" class="chip" :class="'chip--' + meta(rv.status).cls" :title="meta(rv.status).label">
            <span class="chip__icon">{{ meta(rv.status).icon }}</span>{{ rv.login }}
          </span>
        </span>
      </div>
    </li>
    <li v-if="rows.length === 0" class="q__empty">✅ Nothing waiting — the queue is clear.</li>
  </ul>
</template>

<style scoped>
.q {
  list-style: none;
}
.q__row {
  padding: var(--space-xs) var(--space-m);
  border-bottom: 1px solid var(--border-subtle);
  transition: background var(--motion-fast) var(--motion-ease);
}
.q__row:last-child {
  border-bottom: 0;
}
.q__row:hover {
  background: var(--bg-row-hover);
}

.q__main {
  display: flex;
  gap: var(--space-2xs);
  align-items: baseline;
  flex-wrap: wrap;
}
.q__ref {
  font-family: var(--font-mono);
  font-size: var(--step--1);
  font-weight: 500;
  white-space: nowrap;
}
.q__title {
  color: var(--fg);
}

.q__sub {
  display: flex;
  align-items: center;
  gap: var(--space-2xs);
  margin-top: var(--space-3xs);
  flex-wrap: wrap;
  font-size: var(--step--2);
}
.q__by,
.q__age {
  font-family: var(--font-mono);
  color: var(--fg-subtle);
}
.q__by::before {
  content: 'by ';
}
.q__age.stale {
  color: var(--warn);
}
.q__rev {
  display: flex;
  gap: var(--space-3xs);
  flex-wrap: wrap;
  margin-left: var(--space-2xs);
}

/* Reviewer status chips — qompass problem-chip pattern */
.chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-family: var(--font-mono);
  font-feature-settings: 'tnum' 1;
  font-size: 11px;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  color: var(--fg-subtle);
  background: var(--bg-sub);
  border: 1px solid var(--border-subtle);
}
.chip__icon {
  font-size: 11px;
  line-height: 1;
}
.chip--ok {
  color: var(--ok);
  background: var(--ok-bg);
  border-color: color-mix(in srgb, var(--ok) 30%, transparent);
}
.chip--err {
  color: var(--err);
  background: var(--err-bg);
  border-color: color-mix(in srgb, var(--err) 30%, transparent);
}
.chip--comment {
  color: var(--tone-comment);
  background: color-mix(in srgb, var(--tone-comment) 14%, transparent);
  border-color: color-mix(in srgb, var(--tone-comment) 30%, transparent);
}
.chip--pending {
  color: var(--fg-subtle);
}

.q__empty {
  padding: var(--space-m);
  text-align: center;
  color: var(--fg-subtle);
}
</style>
