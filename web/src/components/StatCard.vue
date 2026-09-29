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
});
</script>

<template>
  <div class="card p-4">
    <div class="text-xs font-medium text-slate-500 dark:text-slate-400">{{ label }}</div>
    <div class="mt-1.5 text-2xl font-bold tracking-tight" :class="`text-${tone}-600 dark:text-${tone}-400`">
      {{ value }}
    </div>
    <div v-if="sub" class="mt-0.5 text-[11px] text-slate-400">{{ sub }}</div>
  </div>
</template>
