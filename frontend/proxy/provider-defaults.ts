import type {
  ProxyClient,
  ProxyDefaults,
  ProxyDefaultSlot,
  ProxyState,
} from '../src/api/proxy-client';
import { actions, button, el, field, select, submit, type UIContext } from './dom';

const slots: [ProxyDefaultSlot, string][] = [
  ['search', 'Search'],
  ['fetch', 'Fetch'],
  ['image', 'Image'],
  ['audio', 'Audio'],
  ['music', 'Music'],
  ['transcription', 'Transcription'],
  ['classify', 'Classification'],
];

let popoverSequence = 0;

/** Route choices for one slot; the provider is omitted when the selector repeats it. */
function slotOptions(state: ProxyState, slot: ProxyDefaultSlot): [string, string][] {
  // `routeServes` emits these entries only for canonical providers, using
  // the exact selector declared by that route's profile (not its name).
  const prefix = `${slot}: `;
  const options: [string, string][] = [['', 'None']];
  for (const route of state.routes) {
    const match = route.serves.find((entry) => entry.startsWith(prefix));
    if (!match) continue;
    const selector = match.slice(prefix.length);
    options.push([
      route.name,
      selector === route.provider ? route.provider : `${route.provider} · ${selector}`,
    ]);
  }
  return options;
}

/**
 * Client defaults render as a one-line summary; the editor opens in a
 * popover anchored to Edit so the providers list below never reflows.
 */
export function clientDefaults(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  const fields = slots.map(([slot, label]) => {
    const options = slotOptions(state, slot);
    const saved = state.defaults?.[slot] ?? '';
    const control = select(options, saved);
    const text = options.find(([value]) => value === saved)?.[1] ?? saved;
    return { slot, label, control, saved, text };
  });

  const summary = el('p');
  summary.className = 'defaults-summary';
  const title = el('strong', 'Client defaults');
  summary.append(title);
  const set = fields.filter((f) => f.saved);
  for (const f of set) {
    const item = el('span', el('span', f.label), ' ', el('b', f.text));
    item.className = 'defaults-item';
    summary.append(item);
  }
  const unset = fields.length - set.length;
  const rest = el('span', set.length ? `${unset} unset` : 'None set');
  rest.className = 'defaults-rest';
  if (unset) summary.append(rest);

  const panel = el('section', summary);
  panel.className = 'defaults-strip';
  panel.dataset.focus = 'client-defaults';
  panel.tabIndex = -1;
  panel.setAttribute('aria-label', 'Client defaults');

  if (state.mode === 'file') {
    const note = el('span', 'Set in operator config');
    note.className = 'defaults-note';
    panel.append(note);
    return panel;
  }

  const grid = el('div', ...fields.map(({ label, control }) => field(label, control)));
  grid.className = 'defaults-grid';
  const form = el('form', grid);
  const popover = el(
    'div',
    el(
      'p',
      'A default applies only when the client can use its route and has not set its own provider.',
    ),
    form,
  );
  popover.className = 'defaults-popover';
  popover.id = `proxy-defaults-${++popoverSequence}`;
  popover.setAttribute('role', 'group');
  popover.setAttribute('aria-label', 'Edit client defaults');
  popover.hidden = true;
  // Focus the panel rather than its first select, so opening it with the
  // mouse does not paint a focus ring; Tab then reaches Search.
  popover.tabIndex = -1;

  const edit = button('Edit', () => (popover.hidden ? open() : close()));
  edit.className = 'defaults-edit';
  edit.setAttribute('aria-expanded', 'false');
  edit.setAttribute('aria-controls', popover.id);

  function onPointerDown(event: PointerEvent) {
    if (!panel.contains(event.target as Node)) close(false);
  }
  function onKeyDown(event: KeyboardEvent) {
    if (event.key === 'Escape') close();
  }
  function open() {
    popover.hidden = false;
    edit.setAttribute('aria-expanded', 'true');
    document.addEventListener('pointerdown', onPointerDown);
    panel.addEventListener('keydown', onKeyDown);
    popover.focus();
  }
  function close(returnFocus = true) {
    if (popover.hidden) return;
    for (const f of fields) f.control.value = f.saved;
    popover.hidden = true;
    edit.setAttribute('aria-expanded', 'false');
    document.removeEventListener('pointerdown', onPointerDown);
    panel.removeEventListener('keydown', onKeyDown);
    if (returnFocus) edit.focus();
  }

  const save = submit(form, context, async () => {
    const defaults: ProxyDefaults = {};
    for (const { slot, control } of fields) if (control.value) defaults[slot] = control.value;
    await api.updateDefaults(state.revision, defaults);
    document.removeEventListener('pointerdown', onPointerDown);
    await context.refresh('client-defaults');
    context.announce('Saved client defaults.');
  });
  form.append(
    actions(
      save,
      button('Cancel', () => close()),
    ),
  );

  panel.append(edit, popover);
  return panel;
}
