import { reactive, watchEffect } from 'vue';

const KEY = 'cr-theme';

export type ThemeMode = 'system' | 'light' | 'dark';

const media = window.matchMedia('(prefers-color-scheme: dark)');

function loadMode(): ThemeMode {
  const v = localStorage.getItem(KEY);
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'system';
}

export const theme = reactive({
  mode: loadMode() as ThemeMode,
  // dark 为解析后的实际状态(LineChart 等组件直接监听它)
  dark: false,
});

function resolve(): void {
  theme.dark = theme.mode === 'dark' || (theme.mode === 'system' && media.matches);
}

media.addEventListener('change', resolve);

watchEffect(() => {
  resolve();
  document.documentElement.classList.toggle('dark', theme.dark);
  localStorage.setItem(KEY, theme.mode);
});

export function setThemeMode(mode: ThemeMode): void {
  theme.mode = mode;
}

// 快捷切换: 在浅色与深色之间显式互换(脱离跟随系统)。
export function toggleTheme(): void {
  theme.mode = theme.dark ? 'light' : 'dark';
}
