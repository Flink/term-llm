import type {
  ProxyClient,
  ProxyDefaults,
  ProxyDefaultSlot,
  ProxyState,
} from '../src/api/proxy-client';
import { actions, card, el, field, select, submit, type UIContext } from './dom';

const slots: [ProxyDefaultSlot, string][] = [
  ['search', 'Search'],
  ['fetch', 'Fetch'],
  ['image', 'Image'],
  ['audio', 'Audio'],
  ['music', 'Music'],
  ['transcription', 'Transcription'],
];

export function clientDefaults(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  const fields = slots.map(([slot, label]) => {
    // `routeServes` emits these entries only for canonical providers, using
    // the exact selector declared by that route's profile (not its name).
    const prefix = `${slot}: `;
    const options: [string, string][] = [['', 'No proxy default']];
    for (const route of state.routes) {
      const match = route.serves.find((entry) => entry.startsWith(prefix));
      if (match) options.push([route.name, `${route.provider} · ${match.slice(prefix.length)}`]);
    }
    const control = select(options, state.defaults?.[slot] ?? '');
    control.disabled = state.mode === 'file';
    return { slot, control, label };
  });
  const grid = el('div', ...fields.map(({ label, control }) => field(label, control)));
  grid.className = 'route-form-grid';
  const form = el('form', grid);
  const panel = card(
    'Client defaults',
    el(
      'p',
      state.mode === 'file'
        ? 'Edit defaults in the operator configuration and reload.'
        : 'A proxy default applies only when the client can use its route and has not set its own provider.',
    ),
    form,
  );
  panel.dataset.focus = 'client-defaults';
  panel.tabIndex = -1;
  if (state.mode !== 'file') {
    const save = submit(
      form,
      context,
      async () => {
        const defaults: ProxyDefaults = {};
        for (const { slot, control } of fields) if (control.value) defaults[slot] = control.value;
        await api.updateDefaults(state.revision, defaults);
        await context.refresh('client-defaults');
        context.announce('Saved client defaults.');
      },
      'Save defaults',
    );
    form.append(actions(save));
  }
  return panel;
}
