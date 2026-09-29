<script lang="ts">
import { defineComponent } from 'vue';
import IconX from '~icons/lucide/x';

export default defineComponent({
  name: 'Modal',
  components: { IconX },
  props: {
    open: { type: Boolean, required: true },
    title: { type: String, required: true },
    wide: { type: Boolean, default: false },
  },
  emits: { close: null },
});
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-slate-950/40 p-4 backdrop-blur-sm">
      <div class="card mt-10 w-full p-5" :class="wide ? 'max-w-3xl' : 'max-w-xl'">
        <div class="mb-4 flex items-center justify-between">
          <h3 class="text-base font-semibold">{{ title }}</h3>
          <button class="btn-ghost !px-2" @click="$emit('close')"><IconX class="h-4 w-4" /></button>
        </div>
        <slot />
      </div>
    </div>
  </Teleport>
</template>
