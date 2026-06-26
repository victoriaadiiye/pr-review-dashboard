<script setup lang="ts">
defineProps<{
  pr: {
    repo: string; pr_number: number; title: string; author: string; url: string
    age_hours: number; last_activity_hours: number
    additions: number; deletions: number; changed_files: number
    awaiting: boolean; tier: string
    reviewers: Array<{ login: string; status: string; re_requested: boolean }>
  }
}>()

const STATUS: Record<string, { icon: string; cls: string }> = {
  approved: { icon: '✓', cls: 'ok' },
  changes: { icon: '±', cls: 'err' },
  commented: { icon: '💬', cls: 'comment' },
  pending: { icon: '○', cls: 'pending' },
}
const sIcon = (s: string) => (STATUS[s] ?? STATUS.pending).icon
const sCls = (s: string) => (STATUS[s] ?? STATUS.pending).cls
const hrs = (h: number) => (h >= 48 ? `${Math.round(h / 24)}d` : `${Math.round(h)}h`)
</script>

<template>
  <a class="panel" :class="'tier-' + pr.tier" :href="pr.url" target="_blank" rel="noopener">
    <div class="panel__head">
      <span class="ref">{{ pr.repo }}#{{ pr.pr_number }}</span>
      <span class="title">{{ pr.title }}</span>
      <span v-if="pr.tier === 'new'" class="chip chip--new">NEW</span>
      <span class="open">↗</span>
    </div>
    <div class="panel__meta">
      <span class="by">{{ pr.author }}</span>
      <span class="loc"><span class="add">+{{ pr.additions }}</span> <span class="del">−{{ pr.deletions }}</span> · {{ pr.changed_files }} files</span>
      <span class="age">{{ hrs(pr.age_hours) }} old</span>
      <span class="act">active {{ hrs(pr.last_activity_hours) }} ago</span>
    </div>
    <div class="panel__rev">
      <span v-for="rv in pr.reviewers" :key="rv.login" class="chip" :class="'chip--' + sCls(rv.status)">
        <span class="chip__icon">{{ sIcon(rv.status) }}</span>{{ rv.login }}
        <span v-if="rv.re_requested" class="rr">re-requested</span>
      </span>
      <span v-if="pr.reviewers.length === 0" class="no-rev">no reviewers requested</span>
    </div>
  </a>
</template>

<style scoped>
.panel {
  display: block;
  color: inherit;
  text-decoration: none;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-left-width: 3px;
  border-left-style: solid;
  border-left-color: var(--border);
  border-radius: var(--radius-lg);
  padding: var(--space-s) var(--space-m);
  transition:
    transform var(--motion-fast) var(--motion-ease),
    box-shadow var(--motion-fast) var(--motion-ease),
    border-color var(--motion-fast) var(--motion-ease);
}
.panel:hover {
  transform: translateY(-1px);
  box-shadow: var(--shadow);
  border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
  text-decoration: none;
}
.tier-urgent {
  border-left-color: var(--err);
  background: color-mix(in srgb, var(--err) 5%, var(--bg-card));
}
.tier-waiting {
  border-left-color: var(--warn);
}
.tier-new {
  border-left-color: var(--accent);
}
.tier-reviewed {
  border-left-color: var(--ok);
}

.panel__head {
  display: flex;
  align-items: baseline;
  gap: var(--space-2xs);
  flex-wrap: wrap;
}
.ref {
  font-family: var(--font-mono);
  font-weight: 600;
  color: var(--accent);
  white-space: nowrap;
}
.title {
  color: var(--fg-strong);
  font-weight: 500;
}
.open {
  margin-left: auto;
  color: var(--fg-subtle);
}

.panel__meta {
  display: flex;
  gap: var(--space-s);
  flex-wrap: wrap;
  margin-top: var(--space-2xs);
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
}
.by::before {
  content: 'by ';
}
.add {
  color: var(--ok);
}
.del {
  color: var(--err);
}

.panel__rev {
  display: flex;
  gap: var(--space-3xs);
  flex-wrap: wrap;
  margin-top: var(--space-xs);
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  color: var(--fg-subtle);
  background: var(--bg-sub);
  border: 1px solid var(--border-subtle);
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
.chip--new {
  margin-left: var(--space-2xs);
  color: var(--accent);
  background: var(--accent-bg);
  border: 1px solid color-mix(in srgb, var(--accent) 30%, transparent);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.05em;
}
.rr {
  margin-left: 4px;
  color: var(--warn);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.03em;
}
.no-rev {
  color: var(--fg-subtle);
  font-size: var(--step--2);
}
</style>
