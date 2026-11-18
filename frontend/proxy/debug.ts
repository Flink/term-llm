import type { ProxyClient, ProxyDebugCapture } from '../src/api/proxy-client';
import { capturePreview, runWhileLive } from './debug-preview';
import { byteValue } from './bytes';
import {
  actions,
  button,
  card,
  dateTime,
  el,
  field,
  select,
  titledValue,
  type UIContext,
} from './dom';

// Captures are deliberately transient: no browser storage, downloads or polling.
export function debugView(api: ProxyClient, context: UIContext) {
  let disposed = false;
  let revision = 0;
  let enabled = false;
  let loaded = false;
  let captures: ProxyDebugCapture[] = [];
  let chunk = 65536;
  const status = el('p', 'Loading…');
  const directory = el('p');
  const quota = el('small');
  const details = el('p');
  const viewer = capturePreview(api, context);
  const capture = select([['', 'Select a capture']]);
  const toggle = button('Enable capture', () => {
    void runWhileLive(
      context,
      live,
      async () => {
        const next = !enabled;
        if (next && !window.confirm('Raw prompts/replies may contain secrets. Enable capture?'))
          return;
        const state = await api.setDebug(next);
        if (live()) showStatus(state);
      },
      toolbar,
    );
  });
  toggle.disabled = true;
  const refresh = button('Refresh captures', () => {
    void load();
  });
  const toolbar = actions(toggle, refresh);
  const view = card(
    'Debug',
    status,
    el('small', 'Capture toggle resets on restart. Oldest captures rotate out at the limit.'),
    directory,
    el('p', quota),
    toolbar,
    field('Capture', capture),
    details,
    viewer.element,
  );
  const live = () => !disposed && view.isConnected;
  function showStatus(state: Awaited<ReturnType<ProxyClient['debug']>>) {
    enabled = state.enabled;
    chunk = state.preview_limit_bytes || 65536;
    status.textContent = enabled ? 'Capture enabled' : 'Capture disabled';
    quota.replaceChildren(
      ...(state.record_count === undefined
        ? []
        : [
            `${state.record_count}/${state.record_limit} captures · `,
            titledValue(byteValue(state.remaining_bytes, 'Remaining')),
            ' remaining',
          ]),
    );
    directory.textContent = `Archive: ${state.directory || 'Not created'}`;
    toggle.textContent = enabled ? 'Disable capture' : 'Enable capture';
    loaded = true;
  }
  async function load() {
    const current = ++revision;
    const valid = () => live() && current === revision;
    viewer.clear();
    details.textContent = '';
    capture.disabled = true;
    await runWhileLive(
      context,
      valid,
      async () => {
        const state = await api.debug();
        if (!valid()) return;
        showStatus(state);
        const result = await api.debugCaptures();
        if (!valid()) return;
        captures = result.captures ?? [];
        capture.replaceChildren(el('option', captures.length ? 'Select a capture' : 'No captures'));
        capture.options[0]!.value = '';
        for (const item of captures) {
          const option = el(
            'option',
            `${dateTime(item.started)} · ${item.method} ${item.local_path} · ${item.id}`,
          );
          option.value = item.id;
          capture.append(option);
        }
        capture.disabled = false;
      },
      toolbar,
    );
    if (live()) toggle.disabled = !loaded;
  }
  capture.addEventListener('change', () => {
    viewer.clear();
    details.textContent = '';
    const item = captures.find((item) => item.id === capture.value);
    if (capture.disabled || !item) return;
    details.textContent = [item.finished ? item.outcome : 'Active', item.status || '']
      .filter(Boolean)
      .join(' · ');
    viewer.show(item, chunk);
  });
  queueMicrotask(() => {
    if (live()) void load();
  });
  return {
    element: view,
    dispose() {
      disposed = true;
      ++revision;
      details.textContent = '';
      viewer.clear();
      captures = [];
    },
  };
}
