import { publishedText } from '../api/file-text';
import {
  isDocumentMedia,
  publishedMediaDownloadURL,
  trustedPublishedMediaURL,
} from '../domain/published-media';
import { decorateRichContent, renderMarkdown, type MarkdownMediaTarget } from '../domain/markdown';
import { applyDocumentURLPolicy } from '../domain/markdown-document';
import { iconElement, type IconName } from './Icon';
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

/** Only observe finished, passive previews. An offscreen launcher is unloaded. */
export function observeDocumentPreviews(root: HTMLElement): (() => void) | undefined {
  if (!root.isConnected || typeof IntersectionObserver !== 'function') return;
  const buttons = root.querySelectorAll<HTMLButtonElement>('button[data-auto-preview]:enabled');
  if (!buttons.length) return;
  const observer = new IntersectionObserver(
    (entries) => {
      for (const { target, isIntersecting, intersectionRatio } of entries) {
        if (!target.isConnected) {
          observer.unobserve(target);
          continue;
        }
        if (!isIntersecting || !intersectionRatio) continue;
        observer.unobserve(target);
        const button = target as HTMLButtonElement;
        if (button.disabled) continue;
        button.removeAttribute('data-auto-preview');
        button.dataset.autoOpening = '';
        button.click();
        delete button.dataset.autoOpening;
      }
    },
    { threshold: 0.01 },
  );
  for (const button of buttons) observer.observe(button);
  return () => observer.disconnect();
}

function labelPreviewAction(element: HTMLElement, label: string, icon: IconName): void {
  element.title = label;
  element.setAttribute('aria-label', label);
  element.replaceChildren(iconElement(icon));
}

function previewButton(label: string, icon: IconName): HTMLButtonElement {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'html-preview-toggle';
  labelPreviewAction(button, label, icon);
  return button;
}

/** Keep one connected iframe: moving it between inline and modal containers would reload the demo. */
export async function openDocumentPreview(
  launcher: HTMLButtonElement,
  { url, type: kind, name }: MarkdownMediaTarget,
  automatic: boolean,
): Promise<void> {
  if (!launcher.isConnected) return;
  if (!isDocumentMedia(kind) || !trustedPublishedMediaURL(url, kind))
    throw new Error('Invalid document preview.');
  const source =
    kind === 'html'
      ? undefined
      : await publishedText(url, kind === 'markdown' ? 'text/markdown' : 'text/plain');
  if (!launcher.isConnected) return;
  const preview = document.createElement('dialog');
  preview.className = source === undefined ? 'html-preview' : 'html-preview document-preview';
  preview.open = true; // Non-modal and in the transcript flow until expanded.
  preview.setAttribute('aria-label', launcher.textContent || 'Document preview');

  const header = document.createElement('div');
  header.className = 'html-preview-header';
  const title = document.createElement('span');
  title.textContent = launcher.textContent?.replace(/^Preview: /, '') || 'Document preview';
  title.title = title.textContent;
  const toggle = previewButton('Full screen', 'expand');
  const setIcon = (expanded: boolean) =>
    labelPreviewAction(
      toggle,
      expanded ? 'Exit full screen' : 'Full screen',
      expanded ? 'restore' : 'expand',
    );

  const download = document.createElement('a');
  download.className = 'html-preview-toggle';
  labelPreviewAction(
    download,
    `Download ${name || title.textContent}${kind === 'html' ? ' (ZIP)' : ''}`,
    'download',
  );
  download.href = publishedMediaDownloadURL(url, kind === 'html' ? 'zip' : name || '');
  download.download = '';
  header.append(
    iconElement(kind === 'markdown' ? 'file-text' : 'file-code'),
    title,
    download,
    toggle,
  );
  preview.append(header);
  let frame: HTMLIFrameElement | undefined;
  if (source === undefined) {
    frame = document.createElement('iframe');
    frame.title = launcher.textContent || 'HTML demo';
    frame.setAttribute('sandbox', 'allow-scripts');
    frame.setAttribute('referrerpolicy', 'no-referrer');
    frame.src = url;
    preview.append(frame);
  } else {
    const body = document.createElement('div');
    body.tabIndex = 0;
    body.setAttribute('aria-label', title.textContent);
    body.className = 'document-preview-body markdown-body';
    const raw = document.createElement('pre');
    raw.className = 'document-preview-source';
    const code = document.createElement('code');
    const language =
      kind === 'markdown' ? 'markdown' : /\.([a-z0-9]+)$/i.exec(name || '')?.[1].toLowerCase();
    if (language && language !== 'txt') code.className = `language-${language}`;
    code.textContent = source;
    raw.append(code);
    if (kind === 'markdown') {
      const rendered = document.createElement('div');
      // Use the existing no-active-HTML Markdown mode. Images become links rather
      // than automatically loading subresources from a published document.
      rendered.innerHTML = renderMarkdown(source, undefined, 'user');
      applyDocumentURLPolicy(rendered);
      const mode = previewButton('Raw source', 'code');
      mode.setAttribute('aria-pressed', 'false');
      raw.hidden = true;
      mode.onclick = () => {
        raw.hidden = !raw.hidden;
        rendered.hidden = !raw.hidden;
        mode.setAttribute('aria-pressed', String(!raw.hidden));
      };
      header.insertBefore(mode, toggle);
      body.append(rendered);
    }
    body.append(raw);
    preview.append(body);
    // Paint readable text first. The existing decorator skips oversized blocks
    // and languages we haven't registered, without guessing from the content.
    void decorateRichContent(
      body,
      kind === 'markdown' ? source : '',
      () => preview.isConnected,
    ).catch(() => {});
  }
  // A standalone Markdown reference normally comes wrapped in a paragraph;
  // replace its wrapper so the dialog isn't announced as part of that paragraph.
  const parent = launcher.parentElement;
  const standalone =
    parent?.tagName === 'P' &&
    parent.childElementCount === 1 &&
    parent.textContent.trim() === launcher.textContent.trim();
  (standalone ? parent : launcher).replaceWith(preview);
  if (frame?.contentWindow) previews.set(frame.contentWindow, preview);
  if (!automatic) toggle.focus({ preventScroll: true });

  toggle.onclick = () => {
    if (preview.matches(':modal')) {
      preview.close();
    } else {
      // A non-modal open dialog cannot be promoted directly to modal.
      preview.open = false;
      setIcon(true);
      preview.showModal();
      toggle.focus({ preventScroll: true });
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
