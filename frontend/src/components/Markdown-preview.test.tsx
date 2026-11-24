import { act, fireEvent, render, screen } from '@testing-library/preact';
import { expect, it, vi } from 'vitest';
import * as markdown from '../domain/markdown';
import { Markdown } from './Markdown';

const ref = '0123456789abcdef0123456789abcdef';
const demo = `![Counter](term-llm-media://${ref})`;
const resolveMedia = () => ({ type: 'html' as const, url: `/ui/media/${ref}.html` });
const launcher = () => screen.getByRole('button', { name: 'Preview: Counter' });

it('keeps a streaming launcher disabled across tail updates while another row can launch', async () => {
  const rows = (value: string, streaming: boolean) => (
    <>
      <Markdown value={demo.replace('Counter', 'Clock')} resolveMedia={resolveMedia} />
      <Markdown value={value} streaming={streaming} resolveMedia={resolveMedia} />
    </>
  );
  const { rerender } = render(rows(demo, true));
  expect(launcher()).toBeDisabled();
  fireEvent.click(launcher());
  fireEvent.click(screen.getByRole('button', { name: 'Preview: Clock' }));
  const clock = await screen.findByTitle('Preview: Clock');
  const next = demo + '\n\nMore text';
  rerender(rows(next, true));
  fireEvent(document, new Event('visibilitychange')); // Flush adaptive rendering.
  expect(screen.getByText('More text')).toBeVisible();
  expect(launcher()).toBeDisabled();
  expect(screen.queryByTitle('Preview: Counter')).toBeNull();
  rerender(rows(next + '.', false));
  expect(launcher()).toBeEnabled();
  fireEvent.click(launcher());
  expect(await screen.findByTitle('Preview: Counter')).toBeVisible();
  expect(screen.getByTitle('Preview: Clock')).toBe(clock);
});

it('waits for canonical final DOM when async decoration keeps the streaming DOM on screen', async () => {
  const source = `${demo}\n\n[ref]: /url\n\n\`\`\`js\n1\n\`\`\``;
  let finish!: () => void;
  const pending = new Promise<void>((resolve) => {
    finish = resolve;
  });
  vi.spyOn(markdown, 'decorateRichContent').mockReturnValue(pending);
  const { rerender, container } = render(
    <Markdown value={source} streaming resolveMedia={resolveMedia} />,
  );
  const old = launcher();
  expect(old).toBeDisabled();
  rerender(<Markdown value={source} resolveMedia={resolveMedia} />);
  expect(launcher()).toBe(old);
  expect(old).toBeDisabled();
  fireEvent.click(old);
  await act(async () => finish());
  expect(container.querySelector('iframe')).toBeNull();
  expect(old).not.toBeInTheDocument();
  expect(launcher()).toBeEnabled();
  fireEvent.click(launcher());
  expect(await screen.findByTitle('Preview: Counter')).toBeVisible();
});

it('offers a retry after a failed import without changing the eventual preview title', async () => {
  vi.doMock('./html-preview', () => {
    throw new Error('chunk unavailable');
  });
  try {
    render(<Markdown value={demo} resolveMedia={resolveMedia} />);
    fireEvent.click(launcher());
    const retry = await screen.findByRole('button', { name: /click to retry/i });
    expect(retry).toBeEnabled();
    vi.doUnmock('./html-preview');
    fireEvent.click(retry);
    expect(await screen.findByTitle('Preview: Counter')).toBeVisible();
    expect(screen.getByTitle('Counter')).toHaveTextContent(/^Counter$/);
  } finally {
    vi.doUnmock('./html-preview');
  }
});
