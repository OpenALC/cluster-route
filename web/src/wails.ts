// Wails 桌面运行时检测与窗口控制。
// 桌面模式下 wails 在页面加载前自动注入 window.runtime(无需 import wailsjs),
// 浏览器/无头模式没有该全局对象, 以此区分两种运行形态。

export interface WailsRuntime {
  WindowMinimise(): void;
  WindowToggleMaximise(): void;
  WindowIsMaximised(): Promise<boolean>;
  Environment(): Promise<{ platform: string; arch: string; buildType: string }>;
  Quit(): void;
}

declare global {
  interface Window {
    runtime?: WailsRuntime;
  }
}

export const isDesktop = typeof window !== 'undefined' && !!window.runtime;

export function rt(): WailsRuntime | null {
  return window.runtime ?? null;
}
