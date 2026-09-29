// 应用内通知 Toast(替代原生 alert)与确认弹窗(替代原生 confirm)。
import { reactive } from 'vue';

export interface ToastItem {
  id: number;
  type: 'success' | 'error' | 'info';
  msg: string;
}

export const toastState = reactive({ items: [] as ToastItem[] });
let toastSeq = 1;

export function toast(type: ToastItem['type'], msg: string, ms = 3500): void {
  const id = toastSeq++;
  toastState.items.push({ id, type, msg });
  setTimeout(() => {
    const i = toastState.items.findIndex((t) => t.id === id);
    if (i >= 0) toastState.items.splice(i, 1);
  }, ms);
}

export const confirmState = reactive({
  open: false,
  title: '确认操作',
  message: '',
  confirmText: '确认',
  danger: true,
  _resolve: null as null | ((v: boolean) => void),
});

export function confirmDialog(message: string, title = '确认操作', confirmText = '确认'): Promise<boolean> {
  return new Promise((resolve) => {
    confirmState.open = true;
    confirmState.title = title;
    confirmState.message = message;
    confirmState.confirmText = confirmText;
    confirmState._resolve = resolve;
  });
}

export function settleConfirm(v: boolean): void {
  confirmState.open = false;
  confirmState._resolve?.(v);
  confirmState._resolve = null;
}
