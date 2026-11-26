import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/preact';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { StoreContext } from '../app/context';
import { AppStore } from '../stores/app-store';
import { testConfig, testSession } from '../stores/store-test-fixtures';
import { createEmojiSearch, type EmojiCatalog, type EmojiSearch } from '../domain/emoji-search';
import { Composer } from './Composer';

// Deliberately not in rank or alphabetical order, with keyword-only matches first.
const { catalog, loadEmojiSearch } = vi.hoisted(() => {
  const catalog: [string, string, string, string?][] = [
    ['🌞', 'Sun', 'starshine'],
    ['🌠', 'Shooting star', 'night'],
    ['🏅', 'Star badge', 'award'],
    ['🌟', 'GLowing—Star', 'star', 'star2 glowing'],
    ['✨', 'Sparkles', 'starlight'],
    ['⭐', 'Star', 'favorite'],
    ['👩🏽‍🚀', 'Woman astronaut', 'space'],
    ['🚀', 'Rocket', 'launch'],
    ['👍', 'Thumbs up', 'approve', '+1 thumbsup'],
    ['👎', 'Thumbs down', 'disapprove', '-1 thumbsdown'],
    ['🤩', 'Star-struck', 'excited'],
    ['🔯', 'Dotted six-pointed star', 'religion'],
    ['⭐️', 'White star', 'favorite'],
    ['🌃', 'Night sky', 'stars'],
  ];
  return {
    catalog,
    loadEmojiSearch: vi.fn(async (): Promise<EmojiSearch> => createEmojiSearch(catalog)),
  };
});
vi.mock('../domain/emoji-completion', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../domain/emoji-completion')>()),
  loadEmojiSearch,
}));

const search = (rows: EmojiCatalog = catalog) => createEmojiSearch(rows);

let store: AppStore;
beforeEach(() => loadEmojiSearch.mockClear());
afterEach(() => store?.dispose());

function setup() {
  store = new AppStore(testConfig);
  store.sessions.value = [testSession()];
  store.activeSessionId.value = 's1';
  store.draftActive.value = false;
  store.startupDone.value = true;
  const send = vi.spyOn(store, 'send').mockResolvedValue(undefined);
  render(
    <StoreContext.Provider value={store}>
      <Composer />
      <button type="button">Next control</button>
    </StoreContext.Provider>,
  );
  const input = screen.getByRole<HTMLTextAreaElement>('textbox', { name: 'Message' });
  const user = userEvent.setup();
  return { input, send, user };
}

const menu = () => screen.findByRole('listbox');
const noMenu = () => expect(screen.queryByRole('listbox')).not.toBeInTheDocument();

it('opens emoji suggestions only after two shortcode characters at an eligible boundary', async () => {
  const { input, user } = setup();
  await user.type(input, ':s');
  noMenu();
  await user.keyboard('t');
  expect(await menu()).toBeInTheDocument();

  await user.clear(input);
  await user.type(input, 'https://host:star');
  noMenu();
  await user.keyboard(' :st');
  expect(await menu()).toBeInTheDocument();
});

it('ranks exact names, name prefixes, name-word prefixes and keyword prefixes in catalog order and shows only eight', async () => {
  const { input, user } = setup();
  await user.type(input, ':STAR');
  const options = within(await menu()).getAllByRole('option');
  expect(options.map((option) => option.textContent)).toEqual([
    '⭐:star:',
    '🏅:star_badge:',
    '🌟:star2:',
    '🤩:star_struck:',
    '🌠:shooting_star:',
    '🔯:dotted_six_pointed_star:',
    '⭐️:white_star:',
    '🌞:sun:',
  ]);
  await user.clear(input);
  await user.type(input, ':GLOWING_STAR');
  expect(within(await menu()).getByRole('option', { name: /:star2:/ })).toBeVisible();
});

it('completes chained signed shortcodes and shows an exact alternate alias', async () => {
  const { input, send, user } = setup();
  await user.type(input, ':thumbsup');
  expect(within(await menu()).getByText(':thumbsup:')).toBeVisible();
  await user.keyboard('{Enter}:-1');
  expect(within(await menu()).getByText(':-1:')).toBeVisible();
  await user.keyboard('{Enter}:+1{Enter}');
  expect(input).toHaveValue('👍👎👍');
  expect(input.selectionStart).toBe(input.value.length);
  expect(send).not.toHaveBeenCalled();
});

it('wraps the highlight with arrows and completes with Enter without sending or losing focus', async () => {
  const { input, send, user } = setup();
  await user.type(input, ':star');
  const list = await menu();
  const star = within(list).getByRole('option', { name: /:star:$/ });
  const sun = within(list).getByRole('option', { name: /:sun:/ });
  const badge = within(list).getByRole('option', { name: /:star_badge:/ });
  expect(star).toHaveAttribute('aria-selected', 'true');
  await user.keyboard('{ArrowUp}');
  expect(input).toHaveAttribute('aria-activedescendant', sun.id);
  await user.keyboard('{ArrowDown}{ArrowDown}');
  expect(badge).toHaveAttribute('aria-selected', 'true');
  fireEvent.keyDown(input, { key: 'Enter' });
  expect(input).toHaveValue('🏅');
  expect(store.prompt.value).toBe('🏅');
  expect(input).toHaveFocus();
  expect(send).not.toHaveBeenCalled();
  noMenu();
  fireEvent.keyDown(input, { key: 'Enter', repeat: true });
  expect(send).not.toHaveBeenCalled();
  await user.keyboard('{Enter}');
  expect(send).toHaveBeenCalledOnce();
});

it('completes with Tab in the middle of a multiline draft and restores the Unicode caret without altering surrounding text', async () => {
  const { input, send, user } = setup();
  const head = '🪐 orbit\n';
  const query = ':woman_as';
  const tail = ' 👨‍👩‍👧‍👦 stays\nnext';
  const caret = (head + query).length;
  act(() => input.focus());
  fireEvent.input(input, {
    target: { value: head + query + tail, selectionStart: caret, selectionEnd: caret },
  });
  await menu();
  await user.tab();
  const prefix = `${head}👩🏽‍🚀`;
  expect(input).toHaveValue(prefix + tail);
  expect(store.prompt.value).toBe(prefix + tail);
  expect(input).toHaveFocus();
  await waitFor(() => expect(input.selectionStart).toBe(prefix.length));
  expect(input.selectionEnd).toBe(prefix.length);
  expect(send).not.toHaveBeenCalled();
});

it('replaces the full shortcode and its closing colon when completing from inside it', async () => {
  const { input, send, user } = setup();
  const prefix = 'Hello ';
  const beforeCaret = `${prefix}:rocke`;
  act(() => input.focus());
  fireEvent.input(input, {
    target: {
      value: `${beforeCaret}t: soon`,
      selectionStart: beforeCaret.length,
      selectionEnd: beforeCaret.length,
    },
  });
  await menu();
  await user.tab();
  expect(input).toHaveValue(`${prefix}🚀 soon`);
  expect(input.selectionStart).toBe(`${prefix}🚀`.length);
  expect(send).not.toHaveBeenCalled();
});

it('dismisses the emoji popup on Escape and lets Tab leave the composer afterward', async () => {
  const { input, send, user } = setup();
  await user.type(input, ':star');
  await menu();
  await user.keyboard('{Escape}');
  noMenu();
  expect(input).toHaveValue(':star');
  expect(input).toHaveFocus();
  await user.tab();
  expect(input).not.toHaveFocus();
  expect(send).not.toHaveBeenCalled();
});

it('follows the caret on selection changes, closes on blur, and retains textarea focus when clicked', async () => {
  const { input, user } = setup();
  const text = ':star later';
  await user.type(input, text);
  noMenu();
  input.setSelectionRange(5, 5);
  fireEvent.select(input);
  await menu();
  input.setSelectionRange(0, 5);
  fireEvent.select(input);
  noMenu();
  input.setSelectionRange(5, 5);
  fireEvent.select(input);
  const star = within(await menu()).getByRole('option', { name: /:star:$/ });
  await user.click(star);
  expect(input).toHaveValue('⭐ later');
  expect(input).toHaveFocus();
  await user.type(input, ' :star');
  await menu();
  await user.click(screen.getByRole('button', { name: 'Next control' }));
  noMenu();
});

it('switches between slash and emoji options in the same composer completion list', async () => {
  const { input, user } = setup();
  await user.type(input, ':star');
  const list = await menu();
  expect(within(list).getByRole('option', { name: /:star:$/ })).toBeVisible();

  fireEvent.input(input, { target: { value: '/comp', selectionStart: 5, selectionEnd: 5 } });
  expect(screen.getByRole('listbox')).toBe(list);
  expect(within(list).getByRole('option', { name: /\/compact/ })).toBeVisible();
  expect(within(list).queryByRole('option', { name: /:star:$/ })).toBeNull();

  fireEvent.input(input, { target: { value: ':star', selectionStart: 5, selectionEnd: 5 } });
  expect(screen.getByRole('listbox')).toBe(list);
  const star = within(list).getByRole('option', { name: /:star:$/ });
  expect(input).toHaveAttribute('aria-controls', list.id);
  expect(input).toHaveAttribute('aria-activedescendant', star.id);
  expect(within(list).queryByRole('option', { name: /\/compact/ })).toBeNull();
});

it('resets the highlight when the caret switches from slash to emoji in one draft', async () => {
  const { input, user } = setup();
  await user.type(input, '/co :star');
  const list = await menu();
  input.setSelectionRange(3, 3);
  fireEvent.select(input);
  await user.keyboard('{ArrowDown}');
  input.setSelectionRange(9, 9);
  fireEvent.select(input);
  expect(within(list).getByRole('option', { name: /:star:$/ })).toHaveAttribute(
    'aria-selected',
    'true',
  );
});

it.each(['Escape', 'no matches'])(
  'prevents a cold Enter from sending, then restores normal Enter after %s',
  async (outcome) => {
    let finish!: (loaded: EmojiSearch) => void;
    loadEmojiSearch.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
    const { input, send, user } = setup();
    await user.type(input, ':star');
    noMenu();
    await user.keyboard('{Enter}');
    expect(send).not.toHaveBeenCalled();
    expect(input).toHaveValue(':star');

    if (outcome === 'Escape') await user.keyboard('{Escape}');
    await act(async () =>
      finish(
        search(outcome === 'Escape' ? catalog : catalog.filter(([, name]) => name === 'Rocket')),
      ),
    );
    noMenu();
    await user.keyboard('{Enter}');
    expect(send).toHaveBeenCalledOnce();
  },
);

it('does not reopen stale suggestions when the lazy catalog resolves after Escape', async () => {
  let finish!: (loaded: EmojiSearch) => void;
  loadEmojiSearch.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
  const { input, user } = setup();
  await user.type(input, ':star');
  await user.keyboard('{Escape}');
  expect(finish).toEqual(expect.any(Function));
  await act(async () => finish(search()));
  noMenu();
  expect(input).toHaveValue(':star');
  await user.keyboard('_s');
  expect(await menu()).toBeInTheDocument();
});

it('starts loading the catalog at the colon, before suggestions open', async () => {
  const { input, user } = setup();
  await user.type(input, 'Ready');
  expect(loadEmojiSearch).not.toHaveBeenCalled();
  await user.keyboard(' :');
  expect(loadEmojiSearch).toHaveBeenCalledOnce();
  noMenu();
  await user.keyboard('ro');
  expect(within(await menu()).getByRole('option', { name: /:rocket:/ })).toBeVisible();
});

it('shows suggestions while a keyboard composes the shortcode and accepts a tap', async () => {
  const { input, send, user } = setup();
  act(() => input.focus());
  fireEvent.compositionStart(input);
  fireEvent.input(input, { target: { value: ':rock', selectionStart: 5, selectionEnd: 5 } });
  const rocket = within(await menu()).getByRole('option', { name: /:rocket:/ });
  // Keys belong to the keyboard until composition ends.
  fireEvent.keyDown(input, { key: 'Enter', isComposing: true, keyCode: 229 });
  expect(input).toHaveValue(':rock');
  await user.click(rocket);
  expect(input).toHaveValue('🚀');
  expect(input).toHaveFocus();
  expect(send).not.toHaveBeenCalled();
});

it('releases Enter when the catalog fails to load and retries for the next shortcode', async () => {
  loadEmojiSearch.mockRejectedValueOnce(new Error('chunk unavailable'));
  const { input, send, user } = setup();
  await user.type(input, ':star');
  noMenu();
  await user.keyboard('{Enter}');
  expect(send).toHaveBeenCalledOnce();

  await user.clear(input);
  await user.type(input, ':star');
  expect(loadEmojiSearch).toHaveBeenCalledTimes(2);
  expect(within(await menu()).getByRole('option', { name: /:star:$/ })).toBeVisible();
});
