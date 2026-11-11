<script lang="ts">
import { defineComponent } from 'vue';

export default defineComponent({
  name: 'StatCard',
  props: {
    label: { type: String, required: true },
    value: { type: String, required: true },
    sub: { type: String, default: '' },
    tone: { type: String, default: 'sky' }, // sky|emerald|violet|amber|slate|rose
  },
  computed: {
    toneClass(): string {
      const map: Record<string, string> = {
        sky: 'text-sky-600 dark:text-sky-400',
        emerald: 'text-emerald-600 dark:text-emerald-400',
        violet: 'text-violet-600 dark:text-violet-400',
        amber: 'text-amber-600 dark:text-amber-400',
        slate: 'text-slate-600 dark:text-slate-400',
        rose: 'text-rose-600 dark:text-rose-400',
      };
      return map[this.tone] || map.sky;
    },
  },
});
</script>

<template>
  <div class="card p-4">
    <div class="text-xs font-medium text-slate-500 dark:text-slate-400">{{ label }}</div>
    <div class="mt-1.5 text-2xl font-bold tracking-tight" :class="toneClass">
      {{ value }}
    </div>
    <div v-if="sub" class="mt-0.5 text-[11px] text-slate-400">{{ sub }}</div>
  </div>
</template>
