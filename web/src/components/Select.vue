<script lang="ts">
import { defineComponent, PropType } from 'vue';
import IconChevron from '~icons/lucide/chevron-down';

export interface SelectOption {
  value: string;
  label: string;
}

// 自定义下拉: 原生 <select> 的弹出层由操作系统渲染, 无法跟随应用主题
// (尤其在 WebKitGTK/WebView2 深色模式下), 故以按钮 + 绝对定位弹层实现,
// 保证明暗两套主题下弹层样式与全局一致。
export default defineComponent({
  name: 'Select',
  components: { IconChevron },
  props: {
    modelValue: { type: String, default: '' },
    options: { type: Array as PropType<SelectOption[]>, required: true },
    width: { type: String, default: 'w-40' },
    compact: { type: Boolean, default: false },
  },
  emits: ['update:modelValue', 'change'],
  data() {
    return { open: false };
  },
  computed: {
    currentLabel(): string {
      const hit = this.options.find((o) => o.value === this.modelValue);
      return hit ? hit.label : (this.modelValue || '—');
    },
  },
  mounted() {
    document.addEventListener('click', this.onDocClick);
    document.addEventListener('keydown', this.onKey);
  },
  beforeUnmount() {
    document.removeEventListener('click', this.onDocClick);
    document.removeEventListener('keydown', this.onKey);
  },
  methods: {
    toggle(): void { this.open = !this.open; },
    pick(v: string): void {
      this.$emit('update:modelValue', v);
      this.$emit('change', v);
      this.open = false;
    },
    onDocClick(e: MouseEvent): void {
      if (!this.$el.contains(e.target as Node)) this.open = false;
    },
    onKey(e: KeyboardEvent): void {
      if (e.key === 'Escape') this.open = false;
    },
  },
});
</script>

<template>
  <div class="relative" :class="width">
    <button
      type="button"
      class="input flex items-center justify-between gap-2 text-left"
      :class="compact && '!py-1.5 !text-xs'"
      @click.stop="toggle"
    >
      <span class="truncate">{{ currentLabel }}</span>
      <IconChevron class="h-3.5 w-3.5 shrink-0 text-slate-400 transition-transform" :class="open && 'rotate-180'" />
    </button>
    <div
      v-if="open"
      class="absolute left-0 z-40 mt-1 max-h-60 w-full min-w-full overflow-auto rounded-xl border border-slate-200 bg-white p-1 shadow-lg dark:border-slate-700 dark:bg-slate-900"
    >
      <button
        v-for="o in options"
        :key="o.value"
        type="button"
        class="flex w-full items-center whitespace-nowrap rounded-lg px-2.5 py-1.5 text-left text-xs transition"
        :class="o.value === modelValue
          ? 'bg-sky-50 font-medium text-sky-700 dark:bg-sky-950/50 dark:text-sky-300'
          : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
        @click="pick(o.value)"
      >
        {{ o.label }}
      </button>
    </div>
  </div>
</template>
