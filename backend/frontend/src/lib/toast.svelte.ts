// Global toasts, rendered by <Toaster/> in the root layout (bottom-right).
// Success/info hide after ~4 s; errors stay until dismissed. An optional
// action (e.g. Undo) runs once and dismisses the toast. At most MAX_TOASTS
// show at once — a new one drops the oldest.
import { errText } from '$lib/api';

export type ToastKind = 'success' | 'error' | 'info';
export interface ToastAction {
  label: string;
  run: () => void | Promise<void>;
}
export interface ToastItem {
  id: number;
  kind: ToastKind;
  message: string;
  action?: ToastAction;
}

const MAX_TOASTS = 3;

class Toasts {
  items = $state<ToastItem[]>([]);
  #next = 1;
  #timers = new Map<number, ReturnType<typeof setTimeout>>();

  push(kind: ToastKind, message: string, opts: { action?: ToastAction; timeout?: number } = {}): number {
    const id = this.#next++;
    const next = [...this.items, { id, kind, message, action: opts.action }];
    for (const old of next.slice(0, -MAX_TOASTS)) {
      clearTimeout(this.#timers.get(old.id));
      this.#timers.delete(old.id);
    }
    this.items = next.slice(-MAX_TOASTS);
    const ms = opts.timeout ?? (kind === 'error' ? 0 : opts.action ? 8000 : 4000);
    if (ms > 0) this.#timers.set(id, setTimeout(() => this.dismiss(id), ms));
    return id;
  }

  dismiss(id: number): void {
    clearTimeout(this.#timers.get(id));
    this.#timers.delete(id);
    this.items = this.items.filter((t) => t.id !== id);
  }
}

export const toasts = new Toasts();

export const toast = {
  success: (message: string, opts?: { action?: ToastAction; timeout?: number }) =>
    toasts.push('success', message, opts),
  info: (message: string, opts?: { action?: ToastAction; timeout?: number }) => toasts.push('info', message, opts),
  /** Accepts a message or anything thrown (ApiError → its message). */
  error: (e: unknown) => toasts.push('error', typeof e === 'string' ? e : errText(e))
};
