<script lang="ts">
import { defineComponent } from 'vue';
import { theme, toggleTheme } from '../theme';
import { rt } from '../wails';
import IconSun from '~icons/lucide/sun';
import IconMoon from '~icons/lucide/moon';
import IconMinus from '~icons/lucide/minus';
import IconMaximize from '~icons/lucide/square';
import IconRestore from '~icons/lucide/minimize-2';
import IconClose from '~icons/lucide/x';

export default defineComponent({
  name: 'TitleBar',
  components: { IconSun, IconMoon, IconMinus, IconMaximize, IconRestore, IconClose },
  data() {
    return { theme, maximised: false };
  },
  mounted() {
    rt()?.WindowIsMaximised().then((m) => { this.maximised = m; }).catch(() => undefined);
  },
  methods: {
    toggleTheme,
    minimise(): void { rt()?.WindowMinimise(); },
    toggleMaximise(): void {
      rt()?.WindowToggleMaximise();
      this.maximised = !this.maximised;
    },
    close(): void { rt()?.Quit(); },
  },
});
</script>

<template>
  <div class="titlebar sticky top-0 z-30 flex h-9 select-none items-center border-b border-slate-200 bg-white/90 backdrop-blur dark:border-slate-800 dark:bg-slate-900/90">
    <div class="flex items-center gap-2 px-3">
      <img src="/logo.svg" alt="Cluster Route" class="h-[18px] w-[18px] rounded-[5px]" />
      <span class="text-xs font-semibold tracking-wide text-slate-600 dark:text-slate-300">Cluster Route</span>
    </div>
    <!-- 拖动区域: 双击切换最大化 -->
    <div class="h-full flex-1" @dblclick="toggleMaximise" />
    <div class="flex h-full items-center">
      <button class="tb-btn" title="切换主题" @click="toggleTheme">
        <IconSun v-if="theme.dark" class="h-4 w-4" />
        <IconMoon v-else class="h-4 w-4" />
      </button>
      <button class="tb-btn" title="最小化" @click="minimise"><IconMinus class="h-4 w-4" /></button>
      <button class="tb-btn" :title="maximised ? '还原' : '最大化'" @click="toggleMaximise">
        <IconRestore v-if="maximised" class="h-4 w-4" />
        <IconMaximize v-else class="h-4 w-4" />
      </button>
      <button class="tb-btn close" title="关闭" @click="close"><IconClose class="h-4 w-4" /></button>
    </div>
  </div>
</template>

<style scoped>
/* wails 拖拽协议: 整个标题栏可拖动, 按钮区域除外 */
.titlebar {
  --wails-draggable: drag;
}
.titlebar button {
  --wails-draggable: no-drag;
}
.tb-btn {
  @apply flex h-9 w-11 items-center justify-center text-slate-500 transition hover:bg-slate-200/70 dark:text-slate-400 dark:hover:bg-slate-800;
}
.tb-btn.close:hover {
  @apply bg-rose-600 text-white dark:bg-rose-600;
}
</style>
