<script setup lang="ts">
defineProps<{
  rows: Array<{
    reviewer: string
    display_name: string
    repo: string
    pr_number: number
    title: string
    url: string
    author: string
    points: number
    reviews: number
    states: string[]
    last_submitted: string
  }>
}>()

const stateChip = (s: string) =>
  ({ APPROVED: 'approved', CHANGES_REQUESTED: 'changes', COMMENTED: 'commented' } as Record<string, string>)[s] ?? 'commented'

const rel = (iso: string) => {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  const hours = (Date.now() - then) / 3.6e6
  if (hours < 1) return 'just now'
  if (hours < 24) return `${Math.round(hours)}h ago`
  return `${Math.round(hours / 24)}d ago`
}
</script>

<template>
  <table class="hist">
    <thead>
      <tr>
        <th>Reviewer</th>
        <th>Pull request</th>
        <th>State</th>
        <th class="num">Reviews</th>
        <th class="num">Points</th>
        <th class="num">When</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="r in rows" :key="`${r.reviewer}/${r.repo}/${r.pr_number}`">
        <td class="who"><span class="name">{{ r.display_name }}</span></td>
        <td class="pr">
          <a :href="r.url" target="_blank" rel="noopener">{{ r.title || `${r.repo}#${r.pr_number}` }}</a>
          <span class="ref">{{ r.repo }}#{{ r.pr_number }}</span>
        </td>
        <td>
          <span v-for="s in r.states" :key="s" class="chip" :class="`chip--${stateChip(s)}`">{{ stateChip(s) }}</span>
        </td>
        <td class="num dim">{{ r.reviews }}</td>
        <td class="num points">{{ r.points }}</td>
        <td class="num dim">{{ rel(r.last_submitted) }}</td>
      </tr>
      <tr v-if="rows.length === 0">
        <td colspan="6" class="empty">No reviews in this window.</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.hist {
  width: 100%;
  border-collapse: collapse;
}
th {
  text-align: left;
  font-size: var(--step--2);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--fg-subtle);
  padding: var(--space-2xs) var(--space-m);
  border-bottom: 1px solid var(--border-subtle);
}
td {
  padding: var(--space-2xs) var(--space-m);
  border-bottom: 1px solid var(--border-subtle);
  vertical-align: middle;
}
tbody tr:last-child td { border-bottom: 0; }
tbody tr { transition: background var(--motion-fast) var(--motion-ease); }
tbody tr:hover { background: var(--bg-row-hover); }

.num {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  text-align: right;
  width: 1%;
  white-space: nowrap;
}
th.num { text-align: right; }

.who .name { color: var(--fg); font-weight: 500; }

.pr { width: 100%; }
.pr a { color: var(--fg); font-weight: 500; text-decoration: none; }
.pr a:hover { color: var(--accent); text-decoration: underline; }
.ref {
  display: block;
  font-family: var(--font-mono);
  font-size: var(--step--2);
  color: var(--fg-subtle);
}

.points { color: var(--fg-strong); font-weight: 600; font-size: var(--step-0); }
.dim { color: var(--fg-subtle); }

.chip {
  display: inline-block;
  margin-right: var(--space-2xs);
  font-family: var(--font-mono);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  border: 1px solid var(--border-subtle);
  color: var(--fg-subtle);
  background: var(--bg-sub);
}
.chip--approved { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 30%, transparent); }

.empty {
  text-align: center;
  color: var(--fg-subtle);
  padding: var(--space-m);
}
</style>
