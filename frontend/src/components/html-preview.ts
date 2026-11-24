import { iconElement } from './Icon';
import './html-preview.css';

// A sandboxed frame can't be measured by the chat. Accept only a numeric size
// hint from a preview we actually mounted, never HTML, selectors, or commands.
const previews = new WeakMap<MessageEventSource, HTMLDialogElement>();
window.addEventListener('message', (event: MessageEvent) => {
  if (event.origin !== 'null' || !event.source || event.data?.type !== 'term-llm:preview-height')
    return;
  const preview = previews.get(event.source);
  const height = event.data.height;
  if (!preview?.isConnected || preview.matches(':modal') || !Number.isFinite(height) || height <= 0)
    return;
  // CSS independently accounts for the header and caps the complete preview.
  preview.style.setProperty('--preview-height', `${Math.ceil(Math.min(height, 4096))}px`);
});

/** Keep one connected iframe: moving it between inline and modal containers would reload the demo. */
export function openHTMLPreview(launcher: HTMLButtonElement, url: string): void {
  if (!launcher.isConnected) return;
  const preview = document.createElement('dialog');
  preview.className = 'html-preview';
  preview.open = true; // Non-modal and in the transcript flow until expanded.
  preview.setAttribute('aria-label', launcher.textContent || 'HTML preview');

  const header = document.createElement('div');
  header.className = 'html-preview-header';
  const title = document.createElement('span');
  title.textContent = launcher.textContent?.replace(/^Preview: /, '') || 'HTML preview';
  title.title = title.textContent;
  const toggle = document.createElement('button');
  toggle.type = 'button';
  toggle.className = 'html-preview-toggle';
  const setIcon = (expanded: boolean) => {
    toggle.title = expanded ? 'Exit full screen' : 'Full screen';
    toggle.setAttribute('aria-label', toggle.title);
    toggle.replaceChildren(iconElement(expanded ? 'restore' : 'expand'));
  };
  setIcon(false);

  const frame = document.createElement('iframe');
  frame.title = launcher.textContent || 'HTML demo';
  frame.setAttribute('sandbox', 'allow-scripts');
  frame.setAttribute('referrerpolicy', 'no-referrer');
  frame.src = url;
  header.append(iconElement('file-code'), title, toggle);
  preview.append(header, frame);
  // A standalone Markdown reference normally comes wrapped in a paragraph;
  // replace its wrapper so the dialog isn't announced as part of that paragraph.
  const parent = launcher.parentElement;
  const standalone =
    parent?.tagName === 'P' &&
    parent.childElementCount === 1 &&
    parent.textContent.trim() === launcher.textContent.trim();
  (standalone ? parent : launcher).replaceWith(preview);
  if (frame.contentWindow) previews.set(frame.contentWindow, preview);
  toggle.focus({ preventScroll: true });

  toggle.onclick = () => {
    if (preview.matches(':modal')) {
      preview.close();
    } else {
      // A non-modal open dialog cannot be promoted directly to modal.
      preview.open = false;
      setIcon(true);
      preview.showModal();
    }
  };
  // Escape dismisses the top-layer preview, not panels behind it. Leave native
  // dialog cancellation intact; keyboard events inside the iframe stay there.
  preview.onkeydown = (event) => {
    if (event.key === 'Escape' && preview.matches(':modal')) event.stopPropagation();
  };
  preview.onclose = () => {
    preview.open = true;
    setIcon(false);
    toggle.focus({ preventScroll: true });
  };
}
