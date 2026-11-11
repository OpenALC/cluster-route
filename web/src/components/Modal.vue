<script lang="ts">
import { defineComponent, nextTick } from 'vue';
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
  watch: {
    open(v: boolean) {
      if (v) {
        window.addEventListener('keydown', this.onKey);
        nextTick(() => this.focusFirst());
      } else {
        window.removeEventListener('keydown', this.onKey);
      }
    },
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.onKey);
  },
  methods: {
    focusables(): HTMLElement[] {
      const panel = this.$refs.panel as HTMLElement | undefined;
      if (!panel) return [];
      return Array.from(panel.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'))
        .filter((el) => !el.hasAttribute('disabled'));
    },
    focusFirst(): void {
      this.focusables()[0]?.focus();
    },
    onKey(e: KeyboardEvent): void {
      if (e.key === 'Escape') {
        this.$emit('close');
        return;
      }
      if (e.key !== 'Tab') return;
      const list = this.focusables();
      if (!list.length) return;
      const first = list[0];
      const last = list[list.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || !list.includes(active as HTMLElement))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    },
  },
});
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-slate-950/40 p-4 backdrop-blur-sm">
      <div ref="panel" class="card mt-10 w-full p-5" :class="wide ? 'max-w-3xl' : 'max-w-xl'" role="dialog" aria-modal="true">
        <div class="mb-4 flex items-center justify-between">
          <h3 class="text-base font-semibold">{{ title }}</h3>
          <button class="btn-ghost !px-2" @click="$emit('close')"><IconX class="h-4 w-4" /></button>
        </div>
        <slot />
      </div>
    </div>
  </Teleport>
</template>
