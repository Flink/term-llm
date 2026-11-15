import { afterEach, expect, it, vi } from 'vitest';
import { AppStore } from './app-store';
import { initialProjection } from '../domain/response';
import { testConfig, testSession } from './store-test-fixtures';

let store: AppStore;
afterEach(() => store?.dispose());

it('reconciles child activity independently of response ownership and clears absent status', async () => {
  store = new AppStore(testConfig);
  store.sessions.value = [testSession()];
  const resume = vi.spyOn(store.runEngine, 'resumeResponse').mockResolvedValue(undefined);
  const status = vi.spyOn(store.endpoints, 'sessionStatus');
  status.mockResolvedValue({ sessions: [{ id: 's1', active_children: true }] });

  await store.statusReconciler.refresh(true);
  expect(store.sessions.value[0]).toMatchObject({ activeChildren: true });
  expect(store.sessions.value[0].activeRun).toBeFalsy();
  expect(store.sessions.value[0].activeResponseId).toBeFalsy();
  expect(store.runs.value).toEqual({});
  expect(resume).not.toHaveBeenCalled();

  // A metadata-only sidebar snapshot must not erase status evidence.
  store.sessionStore.applySidebar({ sessions: [{ id: 's1', title: 'Updated' }] });
  expect(store.sessions.value[0].activeChildren).toBe(true);
  status.mockResolvedValue({ sessions: [{ id: 's1' }] });
  await store.statusReconciler.refresh(true);
  expect(store.sessions.value[0].activeChildren).toBe(false);

  status.mockResolvedValue({ sessions: [{ id: 's1', active_children: true }] });
  await store.statusReconciler.refresh(true);
  expect(store.sessions.value[0].activeChildren).toBe(true);
  status.mockResolvedValue({ sessions: [] });
  await store.statusReconciler.refresh(true);
  expect(store.sessions.value[0].activeChildren).toBe(false);
});

it('preserves child activity through an idle parent transcript refresh and unchanged status', async () => {
  store = new AppStore(testConfig);
  store.sessions.value = [testSession({ activeChildren: true, activeRun: false })];
  store.activeSessionId.value = 's1';
  store.draftActive.value = false;
  vi.spyOn(store.endpoints, 'selectedSession').mockResolvedValue({
    selected_session: { id: 's1', transcript_rev: 2 },
    selected_transcript: { bodies: { rev: 2, messages: [] } },
  });
  vi.spyOn(store.endpoints, 'sessionState').mockResolvedValue({});
  const status = vi.spyOn(store.endpoints, 'sessionStatus');
  status.mockResolvedValue({
    sessions: [{ id: 's1', active_children: true }],
    __etag: 'children-running',
  });
  await store.statusReconciler.refresh(true);

  expect(store.runs.value).toEqual({});
  await store.runEngine.refreshSessionMessages('s1');
  expect(store.sessions.value[0]).toMatchObject({ activeChildren: true, messageBodiesRev: 2 });
  expect(store.sessions.value[0].activeRun).toBeFalsy();
  expect(store.runs.value).toEqual({});

  status.mockResolvedValue({ __notModified: true, __etag: 'children-running' });
  await store.statusReconciler.refresh();
  expect(store.sessions.value[0].activeChildren).toBe(true);

  // Metadata/default merges also cannot clear liveness that only status owns.
  const merged = store.sessionStore.mergeSession(store.sessions.value[0], testSession());
  expect(merged.activeChildren).toBe(true);
  expect(
    store.sessionStore.mergeSession(merged, testSession({ activeChildren: false })).activeChildren,
  ).toBe(false);
});

it('still reconciles an idle main response while children remain active', async () => {
  store = new AppStore(testConfig);
  store.sessions.value = [testSession({ activeRun: true, activeResponseId: 'r1' })];
  store.runs.value = {
    s1: initialProjection({
      responseId: 'r1',
      sessionId: 's1',
      epoch: 1,
      status: 'streaming',
      lastSequence: 0,
      startedRev: 0,
      reconnects: 0,
    }),
  };
  vi.spyOn(store.endpoints, 'sessionStatus').mockResolvedValue({
    sessions: [{ id: 's1', active_children: true }],
  });
  const resume = vi.spyOn(store.runEngine, 'resumeResponse').mockResolvedValue(undefined);
  const idle = vi
    .spyOn(store.runEngine, 'reconcileServerIdleResponse')
    .mockResolvedValue(undefined);

  await store.statusReconciler.refresh(true);
  expect(store.sessions.value[0]).toMatchObject({
    activeChildren: true,
    activeRun: false,
    activeResponseId: null,
  });
  expect(resume).toHaveBeenCalledWith('s1', 'r1');
  expect(idle).toHaveBeenCalledWith('s1', 'r1', 0);
  // Status does not itself terminate the response projection.
  expect(store.runs.value.s1.run.status).toBe('streaming');
});
