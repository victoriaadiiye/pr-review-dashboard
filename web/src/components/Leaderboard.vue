<script setup lang="ts">
defineProps<{
  rows: Array<{
    login: string
    display_name: string
    team: string
    is_guest: boolean
    points: number
    reviews: number
    avg_points_per_review: number
    rank: number
  }>
}>()

const medal = (rank: number) => ({ 1: '🥇', 2: '🥈', 3: '🥉' } as Record<number, string>)[rank] ?? ''
</script>

<template>
  <table class="lb">
    <thead>
      <tr>
        <th class="num">#</th>
        <th>Reviewer</th>
        <th class="num">Points</th>
        <th class="num">Reviews</th>
        <th class="num">Avg</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="r in rows" :key="r.login" :class="{ guest: r.is_guest, zero: r.points === 0 }">
        <td class="num rank">
          <span v-if="medal(r.rank)" class="medal">{{ medal(r.rank) }}</span>
          <span v-else>{{ r.rank }}</span>
        </td>
        <td class="who">
          <span class="name">{{ r.display_name }}</span>
          <span v-if="r.is_guest" class="chip chip--guest">guest</span>
        </td>
        <td class="num points">{{ r.points }}</td>
        <td class="num dim">{{ r.reviews }}</td>
        <td class="num dim">{{ r.avg_points_per_review.toFixed(1) }}</td>
      </tr>
      <tr v-if="rows.length === 0">
        <td colspan="5" class="empty">No reviews scored in this window yet.</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.lb {
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
tbody tr:last-child td {
  border-bottom: 0;
}
tbody tr {
  transition: background var(--motion-fast) var(--motion-ease);
}
tbody tr:hover {
  background: var(--bg-row-hover);
}

.num {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  text-align: right;
  width: 1%;
  white-space: nowrap;
}
th.num {
  text-align: right;
}

.rank {
  color: var(--fg-muted);
  text-align: center;
}
.medal {
  font-family: var(--font-sans);
  font-size: var(--step-0);
}

.who {
  width: 100%;
}
.name {
  color: var(--fg);
  font-weight: 500;
}

.points {
  color: var(--fg-strong);
  font-weight: 600;
  font-size: var(--step-0);
}
.dim {
  color: var(--fg-subtle);
}

/* Top three get the accent on points */
tbody tr:nth-child(1) .points {
  color: var(--gold);
}

.zero .name,
.zero .rank {
  color: var(--fg-subtle);
}

.chip {
  display: inline-block;
  margin-left: var(--space-2xs);
  font-family: var(--font-mono);
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 1px 6px;
  border-radius: var(--radius-pill);
}
.chip--guest {
  color: var(--fg-subtle);
  background: var(--bg-sub);
  border: 1px solid var(--border-subtle);
}

.empty {
  text-align: center;
  color: var(--fg-subtle);
  padding: var(--space-m);
}
</style>
