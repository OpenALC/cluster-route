<script lang="ts">
import { defineComponent } from 'vue';
import { confirmState, settleConfirm } from '../toast';

export default defineComponent({
  name: 'ConfirmDialog',
  data() {
    return { state: confirmState };
  },
  methods: {
    settle(v: boolean): void {
      settleConfirm(v);
    },
  },
});
</script>

<template>
  <Teleport to="body">
    <div v-if="state.open" class="fixed inset-0 z-[60] flex items-center justify-center bg-slate-950/40 p-4 backdrop-blur-sm">
      <div class="card w-full max-w-sm p-5">
        <h3 class="text-base font-semibold">{{ state.title }}</h3>
        <p class="mt-2 whitespace-pre-wrap break-words text-sm leading-relaxed text-slate-500 dark:text-slate-400">{{ state.message }}</p>
        <div class="mt-5 flex justify-end gap-2">
          <button class="btn-ghost" @click="settle(false)">取消</button>
          <button
            class="btn"
            :class="state.danger ? 'bg-rose-600 text-white hover:bg-rose-500 active:bg-rose-700' : 'bg-sky-600 text-white hover:bg-sky-500 active:bg-sky-700'"
            @click="settle(true)"
          >{{ state.confirmText }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
