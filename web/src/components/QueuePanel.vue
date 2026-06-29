<script setup lang="ts">
defineProps<{
  pr: {
    repo: string; pr_number: number; title: string; author: string; url: string
    age_hours: number; last_activity_hours: number
    additions: number; deletions: number; changed_files: number
    commits_since_review: number
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

const TIER_LABEL: Record<string, string> = {
  urgent: 'Urgent', waiting: 'Waiting', new: 'New', reviewed: 'Reviewed',
}
const tierLabel = (t: string) => TIER_LABEL[t] ?? t
</script>

<template>
  <a class="panel" :class="'tier-' + pr.tier" :href="pr.url" target="_blank" rel="noopener">
    <div class="panel__head">
      <span class="ref">{{ pr.repo }}#{{ pr.pr_number }}</span>
      <span class="badge" :class="'badge--' + pr.tier">{{ tierLabel(pr.tier) }}</span>
      <span class="open">↗</span>
    </div>
    <div class="panel__title">{{ pr.title }}</div>
    <div class="panel__meta">
      <span class="by">{{ pr.author }}</span>
      <span class="loc"><span class="add">+{{ pr.additions }}</span> <span class="del">−{{ pr.deletions }}</span> · {{ pr.changed_files }} {{ pr.changed_files === 1 ? 'file' : 'files' }}</span>
      <span class="age">{{ hrs(pr.age_hours) }} old</span>
      <span class="act">active {{ hrs(pr.last_activity_hours) }} ago</span>
    </div>
    <div v-if="pr.commits_since_review > 0" class="commits">
      ⟳ {{ pr.commits_since_review }} new commit{{ pr.commits_since_review === 1 ? '' : 's' }} since last review
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
  border-left-width: 6px;
  border-left-style: solid;
  border-left-color: var(--tier, var(--border));
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
  text-decoration: none;
}
/* Per-tier accent: a thick left bar + a clearly tinted card background so the
   urgency reads at a glance, not just a hairline. --tier drives both. */
.tier-urgent {
  --tier: var(--err);
  background: color-mix(in srgb, var(--err) 12%, var(--bg-card));
}
.tier-waiting {
  --tier: var(--warn);
  background: color-mix(in srgb, var(--warn) 10%, var(--bg-card));
}
.tier-new {
  --tier: var(--accent);
  background: color-mix(in srgb, var(--accent) 9%, var(--bg-card));
}
.tier-reviewed {
  --tier: var(--ok);
  background: color-mix(in srgb, var(--ok) 8%, var(--bg-card));
}

.panel__head {
  display: flex;
  align-items: center;
  gap: var(--space-2xs);
}
.ref {
  font-family: var(--font-mono);
  font-weight: 600;
  color: var(--accent);
  white-space: nowrap;
}
.panel__title {
  color: var(--fg-strong);
  font-weight: 600;
  margin-top: var(--space-3xs);
  line-height: 1.3;
}
.open {
  margin-left: auto;
  color: var(--fg-subtle);
}

/* Tier badge — solid, tier-coloured pill so the status is unmistakable. */
.badge {
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  padding: 2px 8px;
  border-radius: var(--radius-pill);
  color: var(--tier);
  background: color-mix(in srgb, var(--tier) 18%, transparent);
  border: 1px solid color-mix(in srgb, var(--tier) 45%, transparent);
}

/* New-commits-since-review notice — warns the reviewer their last look is stale. */
.commits {
  margin-top: var(--space-xs);
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-family: var(--font-mono);
  font-size: var(--step--2);
  font-weight: 600;
  color: var(--warn);
  background: var(--warn-bg);
  border: 1px solid color-mix(in srgb, var(--warn) 35%, transparent);
  border-radius: var(--radius-pill);
  padding: 2px 10px;
}

.panel__meta {
  display: flex;
  align-items: center;
  gap: var(--space-3xs) var(--space-2xs);
  flex-wrap: wrap;
  margin-top: var(--space-xs);
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
}
/* Middot separators between meta items for a tidy single rhythm. */
.panel__meta > span:not(:last-child)::after {
  content: '·';
  margin-left: var(--space-2xs);
  color: var(--border-strong);
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
