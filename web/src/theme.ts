import { reactive, watchEffect } from 'vue';

const KEY = 'cr-theme';

export const theme = reactive({
  dark: localStorage.getItem(KEY) === 'dark',
});

watchEffect(() => {
  document.documentElement.classList.toggle('dark', theme.dark);
  localStorage.setItem(KEY, theme.dark ? 'dark' : 'light');
});

export function toggleTheme(): void {
  theme.dark = !theme.dark;
}
