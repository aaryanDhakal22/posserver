// Promise-based confirmation dialog, rendered by <ConfirmHost/> in the root
// layout. Replaces window.confirm():
//
//   if (!(await confirmDialog({ title: 'Delete “Kitchen”?', danger: true }))) return;

export interface ConfirmOptions {
  title: string;
  body?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  /** The person must type this exact text before the confirm button enables. */
  typeToConfirm?: string;
}

interface Pending extends ConfirmOptions {
  resolve: (ok: boolean) => void;
}

class ConfirmState {
  current = $state<Pending | null>(null);

  settle(ok: boolean): void {
    const c = this.current;
    this.current = null;
    c?.resolve(ok);
  }
}

export const confirmState = new ConfirmState();

export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  // A second request cancels the first.
  confirmState.settle(false);
  return new Promise((resolve) => {
    confirmState.current = { ...opts, resolve };
  });
}
