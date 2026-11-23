import { button, el } from './dom';

/** Standalone DOM version of the web UI's compact notifications. */
export function toastRegion() {
  const element = el('div');
  element.className = 'toast-region';
  element.setAttribute('aria-label', 'Notifications');
  element.setAttribute('aria-live', 'polite');
  let timer: ReturnType<typeof setTimeout> | undefined;

  function clear() {
    clearTimeout(timer);
    element.replaceChildren();
  }

  function toast(message: string, kind: 'success' | 'error') {
    const icon = el('span', kind === 'success' ? '✓' : '!');
    icon.className = 'toast-icon';
    icon.setAttribute('aria-hidden', 'true');
    const text = el('span', message);
    text.className = 'toast-message';
    const close = button('×', clear);
    close.className = 'toast-close';
    close.setAttribute('aria-label', 'Dismiss notification');
    const notice = el('div', icon, text, close);
    notice.className = `toast toast-${kind}`;
    notice.role = kind === 'error' ? 'alert' : 'status';
    clearTimeout(timer);
    element.replaceChildren(notice);
    timer = setTimeout(clear, 4000);
  }

  return { element, toast, clear };
}
