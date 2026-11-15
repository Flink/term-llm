import { act, render } from '@testing-library/preact';
import { afterEach, describe, expect, it } from 'vitest';
import { StoreContext } from '../app/context';
import { AppStore } from '../stores/app-store';
import { testConfig, testSession } from '../stores/store-test-fixtures';
import { activitySnapshot } from '../test/activity-fixtures';
import { Sidebar } from './Sidebar';

let store: AppStore;
afterEach(() => store?.dispose());

function setup() {
  store = new AppStore(testConfig);
  store.sessions.value = [testSession({ id: 'parent', activeRun: false })];
  store.activeSessionId.value = 'parent';
  store.draftActive.value = false;
}

function mount() {
  return render(
    <StoreContext.Provider value={store}>
      <Sidebar />
    </StoreContext.Provider>,
  );
}

describe('sidebar descendant activity', () => {
  it.each([true, false])('uses server child activity only with a healthy feed=%s', (healthy) => {
    setup();
    store.activeSessionId.value = '';
    store.sessions.value = [{ ...store.sessions.value[0], activeChildren: true }];
    store.services.eventFeedHealthy.value = healthy;
    const { container } = mount();
    const row = container.querySelector('.session-row');
    expect(row?.classList.contains('is-active')).toBe(healthy);
    expect(store.runs.value).toEqual({});
    expect(store.runActive.value).toBe(false);
    if (healthy) expect(row?.querySelector('.session-btn')).toHaveAccessibleName('Test — Running');
  });

  it('clears the existing marker when child activity ends without using cached activity rows', () => {
    setup();
    store.sessions.value = [{ ...store.sessions.value[0], activeChildren: true }];
    store.services.eventFeedHealthy.value = true;
    store.childSessionStore.activitySessionId.value = 'parent';
    store.childSessionStore.activity.value = activitySnapshot();
    const { container } = mount();
    const row = container.querySelector('.session-row');
    expect(row).toHaveClass('is-active');

    act(() => {
      store.sessions.value = [{ ...store.sessions.value[0], activeChildren: false }];
    });
    expect(row).not.toHaveClass('is-active');
    expect(store.runActive.value).toBe(false);
  });
});
