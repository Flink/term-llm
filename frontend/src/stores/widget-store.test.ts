import { expect, it, vi } from 'vitest';
import type { WidgetStatusResponse } from '../api/endpoints';
import { AppStore } from './app-store';
import { deferred, testConfig } from './store-test-fixtures';

const running = { widgets: [{ mount: 'usage', title: 'Usage', state: 'running' }] };
const stopped = { widgets: [{ mount: 'usage', title: 'Usage', state: 'stopped' }] };

it('ignores widget refreshes and sideloads that race a mutation', async () => {
  const store = new AppStore(testConfig);
  try {
    const widgets = store.widgetStore;
    const preMutation = widgets.capture();
    widgets.applyRead(running, preMutation);
    const read = deferred<WidgetStatusResponse>();
    store.endpoints.widgetStatus = vi.fn(() => read.promise);
    store.endpoints.stopWidget = vi.fn(async () => ({ ok: true as const, ...stopped }));

    const status = widgets.load();
    const stop = widgets.stop('usage');
    const duringMutation = widgets.capture();
    await stop;
    read.resolve(running);
    await status;
    widgets.applyRead(running, preMutation);
    widgets.applyRead(running, duringMutation);
    widgets.applyRead(undefined, widgets.capture());
    expect(widgets.widgets.value[0].state).toBe('stopped');
    expect(store.endpoints.widgetStatus).toHaveBeenCalledOnce();

    widgets.applyRead({ widgets: [] }, widgets.capture());
    expect(widgets.widgets.value).toEqual([]);
  } finally {
    store.dispose();
  }
});

it('refreshes legacy acknowledgements instead of assuming the requested state', async () => {
  const store = new AppStore(testConfig);
  try {
    store.endpoints.setWidgetDisabled = vi.fn(async () => ({ ok: true as const }));
    store.endpoints.widgetStatus = vi.fn(async () => running);
    await store.widgetStore.setDisabled('usage', true);
    expect(store.endpoints.widgetStatus).toHaveBeenCalledOnce();
    expect(store.widgetStore.enabled.value[0].state).toBe('running');
  } finally {
    store.dispose();
  }
});

it.each([true, false])(
  'preserves the write error when status refresh succeeds=%s',
  async (recover) => {
    const store = new AppStore(testConfig);
    try {
      const widgets = store.widgetStore;
      widgets.applyRead(running, widgets.capture());
      const error = new Error('Stop request failed');
      store.endpoints.stopWidget = vi.fn().mockRejectedValue(error);
      store.endpoints.widgetStatus = vi.fn(async () => {
        if (recover) return stopped;
        throw new Error('Status unavailable');
      });
      await expect(widgets.stop('usage')).rejects.toBe(error);
      expect(store.endpoints.widgetStatus).toHaveBeenCalledOnce();
      expect(widgets.widgets.value[0].state).toBe(recover ? 'stopped' : 'running');
      expect(widgets.pending.value).toBeNull();
    } finally {
      store.dispose();
    }
  },
);

it('keeps newer widget snapshots when valid responses resolve out of order', () => {
  const store = new AppStore(testConfig);
  try {
    const widgets = store.widgetStore;
    const older = widgets.capture();
    const newer = widgets.capture();
    widgets.applyRead(stopped, newer);
    widgets.applyRead(running, older);
    expect(widgets.widgets.value[0].state).toBe('stopped');

    const valid = widgets.capture();
    widgets.applyRead(undefined, widgets.capture());
    widgets.applyRead(running, valid);
    expect(widgets.widgets.value[0].state).toBe('running');
  } finally {
    store.dispose();
  }
});
