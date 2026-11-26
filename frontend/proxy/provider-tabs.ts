import { button, el } from './dom';

export interface ProviderTab {
  id: string;
  label: string;
  /** Short count shown after the label, e.g. approved clients. */
  count?: number;
  /** Marks a tab whose content needs attention (missing key, sign-in). */
  tone?: 'warn' | 'down';
  panel: HTMLElement;
}

let tabSequence = 0;

/**
 * WAI-ARIA tabs with a roving tab stop. Every panel stays mounted so
 * half-finished forms survive switching; `onSelect` may veto a switch (for
 * example while a show-once token is on screen).
 */
export function providerTabs(
  name: string,
  tabs: ProviderTab[],
  initial: string,
  onSelect: (id: string) => boolean,
): HTMLElement {
  const prefix = `provider-tab-${++tabSequence}`;
  const list = el('div');
  list.className = 'provider-tabs';
  list.setAttribute('role', 'tablist');
  list.setAttribute('aria-label', name);
  const selected = tabs.some((tab) => tab.id === initial) ? initial : tabs[0].id;
  const controls = tabs.map((tab) => {
    const control = button(tab.label);
    control.id = `${prefix}-${tab.id}`;
    control.dataset.tab = tab.id;
    control.setAttribute('role', 'tab');
    control.setAttribute('aria-controls', `${control.id}-panel`);
    if (tab.count !== undefined) {
      const count = el('span', String(tab.count));
      count.className = 'provider-tab-count';
      control.append(' ', count);
    }
    if (tab.tone) {
      // Tabs have presentational children: name the state in text, not an img.
      const mark = el('span');
      mark.className = `provider-tab-mark provider-tab-mark-${tab.tone}`;
      mark.setAttribute('aria-hidden', 'true');
      const note = el('span', ', needs attention');
      note.className = 'visually-hidden';
      control.append(mark, note);
    }
    tab.panel.id = `${control.id}-panel`;
    tab.panel.setAttribute('role', 'tabpanel');
    tab.panel.setAttribute('aria-labelledby', control.id);
    // Panels may hold only text (read-only mode), so keep them reachable by Tab.
    tab.panel.tabIndex = 0;
    tab.panel.classList.add('provider-tab-panel');
    return control;
  });
  function show(id: string) {
    tabs.forEach((tab, index) => {
      const active = tab.id === id;
      controls[index].setAttribute('aria-selected', String(active));
      controls[index].tabIndex = active ? 0 : -1;
      tab.panel.hidden = !active;
    });
  }
  function choose(index: number, focus: boolean) {
    const id = tabs[index].id;
    if (controls[index].getAttribute('aria-selected') !== 'true' && !onSelect(id)) return;
    show(id);
    if (focus) controls[index].focus();
  }
  controls.forEach((control, index) => {
    control.addEventListener('click', () => choose(index, false));
    control.addEventListener('keydown', (event) => {
      // Leave modified keys (e.g. Alt+Left for Back) to the browser.
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      const last = controls.length - 1;
      const next =
        event.key === 'ArrowRight'
          ? (index + 1) % controls.length
          : event.key === 'ArrowLeft'
            ? (index + last) % controls.length
            : event.key === 'Home'
              ? 0
              : event.key === 'End'
                ? last
                : undefined;
      if (next === undefined) return;
      event.preventDefault();
      choose(next, true);
    });
  });
  list.append(...controls);
  show(selected);
  const view = el('div', list, ...tabs.map((tab) => tab.panel));
  view.className = 'provider-tabset';
  return view;
}
