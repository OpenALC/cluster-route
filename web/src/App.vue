<script lang="ts">
import { defineComponent } from 'vue';
import { useRoute } from 'vue-router';
import { theme, toggleTheme } from './theme';
import IconSun from '~icons/lucide/sun';
import IconMoon from '~icons/lucide/moon';
import IconActivity from '~icons/lucide/activity';
import IconServer from '~icons/lucide/server';
import IconRoute from '~icons/lucide/route';
import IconArchive from '~icons/lucide/archive';
import IconTag from '~icons/lucide/tags';
import IconLogs from '~icons/lucide/scroll-text';
import IconSettings from '~icons/lucide/settings';
import IconBook from '~icons/lucide/book-open';
import Toasts from './components/Toasts.vue';
import ConfirmDialog from './components/ConfirmDialog.vue';

interface NavItem {
  to: string;
  label: string;
  icon: unknown;
}

export default defineComponent({
  name: 'App',
  components: { IconSun, IconMoon, IconActivity, IconServer, IconRoute, IconArchive, IconTag, IconLogs, IconSettings, IconBook, Toasts, ConfirmDialog },
  setup() {
    const route = useRoute();
    const items: NavItem[] = [
      { to: '/', label: '总览', icon: IconActivity },
      { to: '/providers', label: '供应商', icon: IconServer },
      { to: '/channels', label: '路由', icon: IconRoute },
      { to: '/sessions', label: '会话', icon: IconArchive },
      { to: '/pricing', label: '定价', icon: IconTag },
      { to: '/logs', label: '日志', icon: IconLogs },
      { to: '/settings', label: '设置', icon: IconSettings },
      { to: '/guide', label: '指引', icon: IconBook },
    ];
    const isActive = (to: string): boolean =>
      to === '/' ? route.path === '/' : route.path.startsWith(to);
    return { theme, toggleTheme, items, isActive };
  },
});
</script>

<template>
  <div class="flex min-h-screen">
    <!-- 侧边栏 -->
    <aside class="fixed inset-y-0 left-0 z-20 flex w-56 flex-col border-r border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
      <div class="flex items-center gap-3 px-5 py-5">
        <img src="/logo.svg" alt="Cluster Route" class="h-10 w-10 rounded-[11px] shadow-sm" />
        <div class="leading-none select-none">
          <div class="text-[13px] font-extrabold tracking-[0.14em] text-slate-800 dark:text-slate-100">CLUSTER</div>
          <div class="mt-1.5 text-[10px] font-semibold tracking-[0.44em] text-sky-600 dark:text-sky-400">ROUTE</div>
        </div>
      </div>
      <nav class="mt-2 flex-1 space-y-1 px-3">
        <RouterLink
          v-for="item in items"
          :key="item.to"
          :to="item.to"
          class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-sm font-medium transition"
          :class="isActive(item.to)
            ? 'bg-sky-50 text-sky-700 dark:bg-sky-950/60 dark:text-sky-300'
            : 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/70'"
        >
          <component :is="item.icon" class="h-[18px] w-[18px]" />
          {{ item.label }}
        </RouterLink>
      </nav>
      <div class="px-5 py-4 text-[11px] text-slate-400">v0.2.0</div>
    </aside>

    <!-- 主区域 -->
    <div class="ml-56 flex min-h-screen flex-1 flex-col">
      <header class="sticky top-0 z-10 flex items-center justify-end border-b border-slate-200 bg-white/80 px-6 py-3 backdrop-blur dark:border-slate-800 dark:bg-slate-900/80">
        <button class="btn-ghost !px-2" title="切换主题" @click="toggleTheme">
          <IconSun v-if="theme.dark" class="h-[18px] w-[18px]" />
          <IconMoon v-else class="h-[18px] w-[18px]" />
        </button>
      </header>
      <main class="mx-auto w-full max-w-6xl flex-1 px-6 py-6">
        <RouterView />
      </main>
    </div>

    <Toasts />
    <ConfirmDialog />
  </div>
</template>
