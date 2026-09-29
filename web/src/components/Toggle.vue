<script lang="ts">
import { defineComponent } from 'vue';

export default defineComponent({
  name: 'Toggle',
  props: {
    modelValue: { type: Boolean, required: true },
    label: { type: String, default: '' },
  },
  emits: { 'update:modelValue': null },
});
</script>

<template>
  <label class="inline-flex cursor-pointer select-none items-center gap-2.5">
    <button
      type="button"
      class="relative h-[22px] w-10 rounded-full transition"
      :class="modelValue ? 'bg-sky-600' : 'bg-slate-300 dark:bg-slate-700'"
      @click="$emit('update:modelValue', !modelValue)"
    >
      <span
        class="absolute top-0.5 h-[18px] w-[18px] rounded-full bg-white shadow transition-all"
        :class="modelValue ? 'left-[20px]' : 'left-0.5'"
      />
    </button>
    <span v-if="label" class="text-sm">{{ label }}</span>
  </label>
</template>
