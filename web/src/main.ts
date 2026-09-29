import { createApp } from 'vue';
import App from './App.vue';
import { router } from './router';
import { setApiBase } from './api';
import './styles.css';

declare global {
  interface Window {
    go?: { main?: { App?: { GetAPIBase: () => Promise<string> } } };
  }
}

// Wails 桌面模式下先经绑定拿到管理 API 基址, 再挂载应用。
// 若是从"最小化卸载"恢复, 还原最小化前的页面路由。
async function boot(): Promise<void> {
  try {
    const wailsApp = window.go?.main?.App;
    if (wailsApp?.GetAPIBase) {
      setApiBase(await wailsApp.GetAPIBase());
    }
  } catch {
    // 浏览器模式: 保持同源
  }
  createApp(App).use(router).mount('#app');
  const restore = sessionStorage.getItem('__cr_restore');
  if (restore) {
    sessionStorage.removeItem('__cr_restore');
    if (restore.startsWith('#')) {
      void router.replace(restore.slice(1) || '/');
    }
  }
}

boot();
