<script setup lang="ts">
defineProps<{ rows: Array<{
  login: string; display_name: string; team: string; is_guest: boolean;
  points: number; reviews: number; avg_points_per_review: number; rank: number;
}> }>()
</script>

<template>
  <table class="leaderboard">
    <thead><tr><th>#</th><th>Reviewer</th><th>Points</th><th>Reviews</th><th>Avg</th></tr></thead>
    <tbody>
      <tr v-for="r in rows" :key="r.login" :class="{ guest: r.is_guest }">
        <td>{{ r.rank }}</td>
        <td>{{ r.display_name }}<span v-if="r.is_guest" class="guest-badge"> (guest)</span></td>
        <td>{{ r.points }}</td>
        <td>{{ r.reviews }}</td>
        <td>{{ r.avg_points_per_review.toFixed(1) }}</td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.leaderboard { width: 100%; border-collapse: collapse; }
.leaderboard th, .leaderboard td { padding: 6px 10px; text-align: left; border-bottom: 1px solid #eee; }
.guest { opacity: 0.8; }
.guest-badge { color: #888; font-size: 0.85em; }
</style>
