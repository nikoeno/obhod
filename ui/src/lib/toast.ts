import { reactive } from 'vue';
import { motion } from '../../design/tokens';

export interface ToastItem { id: number; text: string; out: boolean }

/** Toasts are for confirmations only; errors go in the page (design README, Toast). */
export const toasts = reactive<ToastItem[]>([]);

let next = 1;

export function toast(text: string): void {
  const item: ToastItem = { id: next++, text, out: false };
  toasts.push(item);
  setTimeout(() => {
    item.out = true;
  }, motion.toastVisibleMs);
  setTimeout(() => {
    const i = toasts.findIndex((t) => t.id === item.id);
    if (i >= 0) toasts.splice(i, 1);
  }, motion.toastRemoveMs);
}
