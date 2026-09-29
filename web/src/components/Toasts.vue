<script lang="ts">
import { defineComponent } from 'vue';
import { toastState } from '../toast';
import IconCheck from '~icons/lucide/check';
import IconAlert from '~icons/lucide/circle-alert';
import IconInfo from '~icons/lucide/info';

export default defineComponent({
  name: 'Toasts',
  components: { IconCheck, IconAlert, IconInfo },
  data() {
    return { state: toastState };
  },
});
</script>

<template>
  <Teleport to="body">
    <div class="pointer-events-none fixed right-4 top-4 z-[70] flex w-80 flex-col gap-2">
      <TransitionGroup name="toast">
        <div
          v-for="t in state.items"
          :key="t.id"
          class="pointer-events-auto flex items-start gap-2 rounded-xl border p-3 shadow-lg backdrop-blur"
          :class="{
            'border-emerald-200 bg-emerald-50/95 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/90 dark:text-emerald-200': t.type === 'success',
            'border-rose-200 bg-rose-50/95 text-rose-800 dark:border-rose-900 dark:bg-rose-950/90 dark:text-rose-200': t.type === 'error',
            'border-slate-200 bg-white/95 text-slate-700 dark:border-slate-700 dark:bg-slate-900/95 dark:text-slate-200': t.type === 'info',
          }"
        >
          <IconCheck v-if="t.type === 'success'" class="mt-0.5 h-4 w-4 shrink-0" />
          <IconAlert v-else-if="t.type === 'error'" class="mt-0.5 h-4 w-4 shrink-0" />
          <IconInfo v-else class="mt-0.5 h-4 w-4 shrink-0" />
          <div class="min-w-0 whitespace-pre-wrap break-words text-xs leading-relaxed">{{ t.msg }}</div>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: all 0.25s ease;
}
.toast-enter-from {
  opacity: 0;
  transform: translateX(24px);
}
.toast-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
