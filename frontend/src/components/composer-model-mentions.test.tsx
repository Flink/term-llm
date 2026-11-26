import { act, fireEvent, render, screen } from '@testing-library/preact';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { StoreContext } from '../app/context';
import type { MentionSearchResponse } from '../domain/completions';
import { AppStore } from '../stores/app-store';
import { deferred, testConfig, testSession } from '../stores/store-test-fixtures';
import { Composer } from './Composer';

const scopedResults = (
  start: number,
  end: number,
  query: string,
  model_items: MentionSearchResponse['model_items'] = [],
  models_pending = false,
): MentionSearchResponse => ({
  active: true,
  items: [],
  model_only: true,
  model_token: { start_utf16: start, end_utf16: end, query, quoted: false },
  model_items,
  models_pending,
});

const opus = {
  label: 'claude-bin:opus',
  description: 'Opus 5.5',
  badge: 'latest',
  insert_text: '@model:claude-bin:opus#',
  continues: true,
};
const medium = {
  label: 'claude-bin:opus-medium',
  description: 'medium effort',
  badge: 'default',
  insert_text: 'claude-bin:opus-medium',
  continues: false,
};
const defaultEffort = {
  label: 'claude-bin:opus',
  description: 'No effort suffix',
  badge: '',
  insert_text: 'claude-bin:opus',
  continues: false,
};

const setup = () => {
  const store = new AppStore(testConfig);
  store.sessions.value = [testSession()];
  store.activeSessionId.value = 's1';
  store.draftActive.value = false;
  store.startupDone.value = true;
  store.send = vi.fn(async () => undefined);
  const models = vi.fn(async () => ({ data: [] }));
  const runtime = vi.fn(async () => ({}));
  store.endpoints.models = models;
  store.endpoints.runtime = runtime;
  const search = vi.fn(
    async (
      _body: unknown,
      _sessionId?: string,
      _signal?: AbortSignal,
    ): Promise<MentionSearchResponse> => ({
      active: false,
      items: [],
    }),
  );
  store.endpoints.mentionSearch = search;
  render(
    <StoreContext.Provider value={store}>
      <Composer />
    </StoreContext.Provider>,
  );
  const textbox = screen.getByRole<HTMLTextAreaElement>('textbox', { name: 'Message' });
  const type = (value: string, caret = value.length) =>
    fireEvent.input(textbox, { target: { value, selectionStart: caret, selectionEnd: caret } });
  const press = (key: string) => fireEvent.keyDown(textbox, { key });
  return { store, search, models, runtime, textbox, type, press };
};
const advance = (ms = 50) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

it('inserts a model and its effort via Tab and Enter using the full UTF-16 model range', async () => {
  const { store, search, models, runtime, textbox, type, press } = setup();
  const source = '🎉 @m:opusOLD please';
  search.mockResolvedValueOnce(scopedResults(3, 13, 'opus', [opus]));
  search.mockResolvedValueOnce(scopedResults(3, 26, 'claude-bin:opus#', [defaultEffort, medium]));
  type(source, 10);
  await advance();
  expect(search).toHaveBeenCalledWith(
    expect.objectContaining({ text: source, cursor_utf16: 10 }),
    's1',
    expect.any(AbortSignal),
  );
  expect(screen.getByRole('option', { name: /claude-bin:opus.*Opus 5\.5.*latest/ })).toBeVisible();
  press('Tab');
  expect(textbox).toHaveValue('🎉 @model:claude-bin:opus# please');
  expect(screen.getByRole('option', { name: 'Loading models…' })).toHaveAttribute(
    'aria-disabled',
    'true',
  );
  press('Enter');
  expect(store.send).not.toHaveBeenCalled();

  await advance();
  expect(search).toHaveBeenLastCalledWith(
    expect.objectContaining({ text: '🎉 @model:claude-bin:opus# please', cursor_utf16: 26 }),
    's1',
    expect.any(AbortSignal),
  );
  press('ArrowDown');
  expect(
    screen.getByRole('option', { name: /opus-medium.*medium effort.*default/ }),
  ).toHaveAttribute('aria-selected', 'true');
  press('Enter');
  expect(textbox).toHaveValue('🎉 claude-bin:opus-medium please');
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  expect(store.send).not.toHaveBeenCalled();
  expect(store.attachments.value).toEqual([]);
  expect(runtime).not.toHaveBeenCalled();
  expect(models).not.toHaveBeenCalled();
  expect(store.models.value).toEqual([]);
});

it('refreshes a still-current pending query and preserves usable partial rows', async () => {
  const { search, type } = setup();
  search.mockResolvedValueOnce(scopedResults(0, 3, '', [opus], true));
  search.mockResolvedValueOnce(scopedResults(0, 3, '', [opus, medium]));
  type('@m:');
  await advance();
  expect(screen.getByRole('option', { name: /opus.*Opus/ })).not.toHaveAttribute('aria-disabled');
  expect(screen.getByText('Loading more models…')).toBeVisible();
  await advance(250);
  expect(screen.getByRole('option', { name: /opus-medium/ })).toBeVisible();
  expect(screen.queryByText('Loading more models…')).not.toBeInTheDocument();
  await advance(20_000);
  expect(search).toHaveBeenCalledTimes(2);
});

it.each(['Enter', 'Tab'])(
  'keeps the explicitly highlighted row through progressive insertions (%s)',
  async (key) => {
    const { search, type, press, textbox, store } = setup();
    search.mockResolvedValueOnce(scopedResults(0, 3, '', [defaultEffort, medium], true));
    search.mockResolvedValueOnce(scopedResults(0, 3, '', [opus, defaultEffort, medium]));
    type('@m:');
    await advance();
    press('ArrowDown');
    const mediumRow = () => screen.getByRole('option', { name: /opus-medium/ });
    expect(mediumRow()).toHaveAttribute('aria-selected', 'true');
    await advance(250);
    expect(mediumRow()).toHaveAttribute('aria-selected', 'true');
    press(key);
    expect(textbox).toHaveValue('claude-bin:opus-medium ');
    expect(store.send).not.toHaveBeenCalled();
  },
);

it('does not retarget an explicitly highlighted model that disappears during a refresh', async () => {
  const { search, type, press, textbox, store } = setup();
  search.mockResolvedValueOnce(scopedResults(0, 3, '', [defaultEffort, medium], true));
  search.mockResolvedValueOnce(scopedResults(0, 3, '', [defaultEffort]));
  type('@m:');
  await advance();
  press('ArrowDown');
  await advance(250);
  expect(textbox).not.toHaveAttribute('aria-activedescendant');
  press('Enter');
  expect(textbox).toHaveValue('@m:');
  expect(store.send).not.toHaveBeenCalled();
  press('ArrowDown');
  press('Enter');
  expect(textbox).toHaveValue('claude-bin:opus ');
});

it('does not keep a scoped placeholder when mention search is disabled', async () => {
  const { search, store, type, press } = setup();
  type('@m:opus');
  expect(screen.getByRole('option', { name: 'Loading models…' })).toHaveAttribute(
    'aria-disabled',
    'true',
  );
  await advance();
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  expect(press('Tab')).toBe(true);
  press('Enter');
  expect(store.send).toHaveBeenCalledOnce();
  expect(search).toHaveBeenCalledOnce();
});

it.each([true, false])(
  'hides stale file rows while a bare effort query is classified (known provider: %s)',
  async (knownProvider) => {
    const { search, store, type, press, textbox } = setup();
    if (knownProvider) store.providers.value = [{ id: 'claude-bin', name: 'Claude' }];
    const text = '@claude-bin:';
    const token = { start_utf16: 0, end_utf16: text.length, query: text.slice(1), quoted: false };
    search.mockResolvedValueOnce({
      active: true,
      token,
      model_token: knownProvider ? undefined : token,
      items: [
        { path: 'claude-bin:notes', kind: 'file', insert_text: '@claude-bin:notes', segments: [] },
      ],
    });
    type(text);
    await advance();
    expect(screen.getByRole('option', { name: /claude-bin:notes/ })).toBeVisible();
    type(`${text}#`);
    expect(screen.queryByRole('option', { name: /claude-bin:notes/ })).not.toBeInTheDocument();
    press('Enter');
    expect(textbox).toHaveValue(`${text}#`);
    expect(store.send).not.toHaveBeenCalled();
    await advance();
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  },
);

it('bounds refreshes when the server remains pending with no rows', async () => {
  const { search, store, textbox, type, press } = setup();
  search.mockResolvedValue(scopedResults(0, 9, 'absent', [], true));
  type('@m:absent');
  await advance();
  expect(screen.getByRole('option', { name: 'Loading models…' })).toHaveAttribute(
    'aria-disabled',
    'true',
  );
  await advance(60_000);
  const requestCount = search.mock.calls.length;
  expect(requestCount).toBeGreaterThan(1);
  expect(requestCount).toBeLessThan(15);
  expect(screen.getByRole('option', { name: 'No matching models' })).toHaveAttribute(
    'aria-disabled',
    'true',
  );
  await advance(60_000);
  expect(search).toHaveBeenCalledTimes(requestCount);
  press('Enter');
  expect(store.send).not.toHaveBeenCalled();
  expect(textbox).toHaveValue('@m:absent');
});

it('hides stale rows immediately and ignores late results from an aborted refresh', async () => {
  const { store, search, textbox, type, press } = setup();
  const late = deferred<MentionSearchResponse>();
  search.mockResolvedValueOnce(scopedResults(0, 7, 'opus', [opus], true));
  search.mockImplementationOnce(() => late.promise);
  search.mockResolvedValueOnce(scopedResults(0, 10, 'missing'));
  type('@m:opus');
  await advance(300);
  expect(screen.getByRole('option', { name: /opus.*Opus/ })).toBeVisible();
  const refreshSignal = search.mock.calls[1][2];

  type('@m:missing');
  expect(refreshSignal?.aborted).toBe(true);
  expect(screen.queryByRole('option', { name: /opus.*Opus/ })).not.toBeInTheDocument();
  press('Enter');
  expect(textbox).toHaveValue('@m:missing');
  expect(store.send).not.toHaveBeenCalled();
  await advance();
  await act(async () => late.resolve(scopedResults(0, 7, 'opus', [opus], true)));
  expect(screen.getByRole('option', { name: 'No matching models' })).toBeVisible();
  await advance(20_000);
  expect(search).toHaveBeenCalledTimes(3);
});

it('invalidates a model row when only the caret moves', async () => {
  const { search, textbox, type, press } = setup();
  search.mockResolvedValueOnce(scopedResults(0, 9, 'opus#', [defaultEffort]));
  search.mockResolvedValueOnce(scopedResults(0, 9, 'opus', [opus]));
  type('@m:opus##', 8);
  await advance();
  const stale = screen.getByRole('option', { name: /No effort suffix/ });

  // The DOM caret can move before keyup updates the composer's search state.
  textbox.setSelectionRange(7, 7);
  fireEvent.click(stale);
  expect(textbox).toHaveValue('@m:opus##');
  fireEvent.keyUp(textbox, { key: 'ArrowLeft' });
  expect(stale).not.toBeInTheDocument();
  await advance();
  press('Enter');
  expect(textbox).toHaveValue('@model:claude-bin:opus#');
});

it('drops a live model menu on a session change with the same prompt and caret', async () => {
  const { store, search, type } = setup();
  store.sessions.value = [testSession(), { ...testSession(), id: 's2' }];
  search.mockResolvedValueOnce(scopedResults(0, 7, 'opus', [opus], true));
  type('@m:opus');
  await advance();
  const oldSignal = search.mock.calls[0][2];
  expect(screen.getByRole('option', { name: /opus.*Opus/ })).toBeVisible();

  await act(() => {
    store.activeSessionId.value = 's2';
  });
  expect(oldSignal?.aborted).toBe(true);
  expect(screen.queryByRole('option', { name: /opus.*Opus/ })).not.toBeInTheDocument();
  await advance(20_000);
  expect(search).toHaveBeenCalledTimes(2);
  expect(search.mock.calls[1][1]).toBe('s2');
});

it('honors model_only on a bare effort step and can select the unsuffixed reference', async () => {
  const { store, search, type, press, textbox } = setup();
  const value = '@claude-bin:opus#';
  const payload = scopedResults(0, value.length, 'claude-bin:opus#', [defaultEffort, medium]);
  search.mockResolvedValueOnce({
    ...payload,
    token: payload.model_token,
    items: [{ path: 'wrong.txt', kind: 'file', insert_text: '@wrong.txt', segments: [] }],
  });
  type(value);
  await advance();
  expect(screen.queryByRole('option', { name: /wrong\.txt/ })).not.toBeInTheDocument();
  press('Tab');
  expect(textbox).toHaveValue('claude-bin:opus ');
  expect(store.send).not.toHaveBeenCalled();
});

it('dismisses a pending model menu with Escape without reopening or changing the prompt', async () => {
  const { store, search, textbox, type, press } = setup();
  search.mockResolvedValue(scopedResults(0, 3, '', [], true));
  type('@m:');
  await advance();
  expect(screen.getByRole('option', { name: 'Loading models…' })).toBeVisible();
  press('Escape');
  await advance(20_000);
  expect(search).toHaveBeenCalledOnce();
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  expect(textbox).toHaveValue('@m:');
  press('Enter');
  expect(store.send).toHaveBeenCalledOnce();
});

it('keeps bare model rows additive with ordinary agent and file rows', async () => {
  const { search, textbox, type, press } = setup();
  const token = { start_utf16: 0, end_utf16: 3, query: 'ja', quoted: false };
  search.mockResolvedValueOnce({
    active: true,
    token,
    items: [{ path: 'jar.go', kind: 'file', insert_text: '@jar.go', segments: [] }],
    model_token: token,
    model_items: [
      {
        ...opus,
        label: 'jar-provider:latest',
        insert_text: 'jar-provider:latest',
        continues: false,
      },
    ],
  });
  type('@ja');
  await advance();
  expect(
    screen.getAllByRole('option').map((option) => option.querySelector('strong')?.textContent),
  ).toEqual(['@jarvis', 'jar.go', 'jar-provider:latest']);
  press('Enter');
  expect(textbox).toHaveValue('@jarvis ');
});

it.each(['@agent:jarvis', '@"model:notes', '@./m:notes'])(
  'does not reserve %s as a model namespace',
  async (value) => {
    const { search, type, press, store } = setup();
    type(value);
    await advance();
    expect(search).toHaveBeenCalledOnce();
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    press('Enter');
    expect(store.send).toHaveBeenCalledOnce();
  },
);

it('preserves file priority for a bare prefix and prevents sending a qualified query during debounce', async () => {
  const { search, store, type, press, textbox } = setup();
  store.providers.value = [{ id: 'claude-bin', name: 'Claude' }];
  const token = { start_utf16: 0, end_utf16: 3, query: 'cl', quoted: false };
  search.mockResolvedValueOnce({
    active: true,
    token,
    model_token: token,
    model_items: [opus],
    items: [{ path: 'client.ts', kind: 'file', insert_text: '@client.ts', segments: [] }],
  });
  type('@cl');
  await advance();
  expect(screen.getByRole('option', { name: /client.ts/ })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  type('@claude-bin:op');
  expect(screen.queryByRole('option', { name: /client.ts/ })).not.toBeInTheDocument();
  press('Enter');
  expect(textbox).toHaveValue('@claude-bin:op');
  expect(store.send).not.toHaveBeenCalled();
});
