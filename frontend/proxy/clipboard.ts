import { button, type UIContext } from './dom';

type ToastFeedback = Pick<UIContext, 'toast'>;

/** Clipboard feedback stays out of the page's status banner and leaves the button alone. */
export function clipboardButton(
  label: string,
  context: ToastFeedback,
  options: {
    read: () => string | undefined;
    copied: string;
    unavailable: string;
    empty?: string;
    fallback?: () => void;
  },
): HTMLButtonElement {
  async function copy() {
    const value = options.read();
    if (!value) {
      if (options.empty) context.toast(options.empty, 'error');
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      if (!control.isConnected) return;
      options.fallback?.();
      context.toast(`Clipboard unavailable. ${options.unavailable}`, 'error');
      return;
    }
    if (control.isConnected) context.toast(options.copied, 'success');
  }
  const control = button(label, () => void copy());
  return control;
}

export function selectCopyField(field: HTMLInputElement | HTMLTextAreaElement): void {
  field.focus({ preventScroll: true });
  field.select();
}
