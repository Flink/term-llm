import { button, el } from './dom';

export function timelineScopeView(
  steps: { label: string; run: () => void }[],
  current: { label: string; clearLabel: string; clear: () => void },
): HTMLElement {
  const caption = el('span', 'Viewing');
  caption.className = 'timeline-scope-label';
  const trail = el('ol');
  trail.className = 'timeline-scope-trail';
  function appendStep(control: HTMLElement) {
    const item = el('li');
    if (trail.childElementCount) {
      const separator = el('span');
      separator.className = 'timeline-chevron';
      separator.setAttribute('aria-hidden', 'true');
      item.append(separator);
    }
    item.append(control);
    trail.append(item);
  }
  for (const step of steps) {
    const ancestor = button(step.label, step.run);
    ancestor.className = 'timeline-scope-link';
    appendStep(ancestor);
  }
  const label = el('span', current.label);
  label.setAttribute('aria-current', 'location');
  const cross = el('span', '×');
  cross.setAttribute('aria-hidden', 'true');
  const clear = button('', current.clear);
  clear.append(cross);
  clear.className = 'timeline-scope-clear';
  clear.setAttribute('aria-label', current.clearLabel);
  clear.title = current.clearLabel;
  const pill = el('span', label, clear);
  pill.className = 'timeline-scope-current';
  appendStep(pill);

  const bar = el('nav', caption, trail);
  bar.className = 'timeline-scope';
  bar.setAttribute('aria-label', 'Usage scope');
  return bar;
}
