import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  fireEvent,
  getByLabelText,
  getByRole,
  queryByRole,
  waitFor,
} from '@testing-library/preact';
import {
  ProxyAPIError,
  ProxyClient,
  type ProxyDebugCapture,
  type ProxyDebugPreview,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { debugView } from './debug';

const capture: ProxyDebugCapture = {
  id: 'newest',
  started: 1700000000000,
  method: 'POST',
  local_path: '/v1/responses',
  finished: false,
  outcome: 'active',
  route: 'example',
  websocket: false,
  files: [
    'manifest.json',
    'request.body',
    'response.body',
    'websocket-client.frames',
    'websocket-server.frames',
  ],
};
const preview: ProxyDebugPreview = {
  id: 'newest',
  file: 'manifest.json',
  bytes_returned: 65536,
  content: '<script>secret</script> café',
  encoding: 'utf8',
  offset: 0,
  total_bytes: 200000,
  truncated: true,
};
const disposals: (() => void)[] = [];
function setup() {
  const api = new ProxyClient(vi.fn());
  vi.spyOn(api, 'debug').mockResolvedValue({ enabled: false, directory: '/private/captures' });
  vi.spyOn(api, 'setDebug').mockImplementation(async (enabled) => ({
    enabled,
    directory: '/private/captures',
  }));
  vi.spyOn(api, 'debugCaptures').mockResolvedValue({
    captures: [capture, { ...capture, id: 'older', started: 1600000000000 }],
  });
  vi.spyOn(api, 'debugFile').mockImplementation(async (id, file) => ({ ...preview, id, file }));
  const errors = vi.fn();
  const context: UIContext = {
    run: vi.fn(async (action) => {
      try {
        await action();
        return true;
      } catch (error) {
        errors(error);
        return false;
      }
    }),
    refresh: vi.fn(),
    toast: vi.fn(),
    announce: vi.fn(),
    navigate: vi.fn(),
    protectToken: vi.fn(),
  };
  const view = debugView(api, context);
  document.body.append(view.element);
  disposals.push(view.dispose);
  return { api, root: view.element, errors, dispose: view.dispose, context };
}
async function ready(root: HTMLElement) {
  await waitFor(() => expect(getByLabelText(root, 'Capture')).not.toBeDisabled());
}
function choose(root: HTMLElement, id = 'newest') {
  fireEvent.change(getByLabelText(root, 'Capture'), { target: { value: id } });
}
afterEach(() => {
  disposals.splice(0).forEach((dispose) => dispose());
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe('debug capture viewer', () => {
  it('defaults to disabled, shows archive and lists newest first without reading bodies or polling', async () => {
    const { root, api } = setup();
    await ready(root);
    expect(root).toHaveTextContent('Capture disabled');
    expect(root).toHaveTextContent('Archive: /private/captures');
    expect(
      [...getByRole<HTMLSelectElement>(root, 'combobox', { name: 'Capture' }).options].map(
        (option) => option.value,
      ),
    ).toEqual(['', 'newest', 'older']);
    expect(api.setDebug).not.toHaveBeenCalled();
    expect(api.debugFile).not.toHaveBeenCalled();
    expect(api.debugCaptures).toHaveBeenCalledOnce();
    fireEvent.click(getByRole(root, 'button', { name: 'Refresh captures' }));
    await waitFor(() => expect(api.debugCaptures).toHaveBeenCalledTimes(2));
  });

  it.each([
    { remaining_bytes: 32767, compact: '~32.77K', record_count: 1, record_limit: 1000 },
    { remaining_bytes: 65536, compact: '~65.54K', record_count: 1000, record_limit: 1000 },
  ])('shows full archive quotas rather than implying new captures can start: %j', async (quota) => {
    const { root, api } = setup();
    vi.mocked(api.debug).mockResolvedValue({ enabled: true, directory: '/captures', ...quota });
    await ready(root);
    expect(root).toHaveTextContent('Capture enabled');
    expect(root).toHaveTextContent('Oldest captures rotate out at the limit.');
    expect(root).not.toHaveTextContent('Archive full');
    expect(root).toHaveTextContent(`${quota.record_count}/${quota.record_limit} captures`);
    expect(root).toHaveTextContent(`${quota.compact} remaining`);
    expect(root.querySelector('small span')).toHaveAttribute(
      'title',
      `Remaining: ${quota.remaining_bytes.toLocaleString()} bytes`,
    );
  });

  it('enables only after the concise sensitive-data confirmation, and disables without confirmation', async () => {
    const { root, api, context } = setup();
    await ready(root);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    fireEvent.click(getByRole(root, 'button', { name: 'Enable capture' }));
    expect(confirm).toHaveBeenCalledWith(
      'Raw prompts/replies may contain secrets. Enable capture?',
    );
    expect(api.setDebug).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    fireEvent.click(getByRole(root, 'button', { name: 'Enable capture' }));
    await waitFor(() => expect(root).toHaveTextContent('Capture enabled'));
    expect(api.setDebug).toHaveBeenLastCalledWith(true);
    fireEvent.click(getByRole(root, 'button', { name: 'Disable capture' }));
    await waitFor(() => expect(root).toHaveTextContent('Capture disabled'));
    expect(api.setDebug).toHaveBeenLastCalledWith(false);
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(context.run).toHaveBeenCalled();
  });

  it('renders only text and distinguishes UTF-8, decoded WebSocket, and raw binary previews', async () => {
    const { root, api } = setup();
    await ready(root);
    choose(root);
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe(preview.content));
    expect(api.debugFile).toHaveBeenCalledWith('newest', 'manifest.json', 0, 65536);
    expect(root.querySelector('script')).toBeNull();
    expect(root).toHaveTextContent('UTF-8 · ~200K · Offset 0 · Truncated preview');
    expect(
      root.querySelector('.debug-preview')?.parentElement?.querySelector('p span'),
    ).toHaveAttribute('title', 'Total: 200,000 bytes');
    expect(queryByRole(root, 'button', { name: 'Raw bytes' })).toBeNull();
    const file = 'websocket-server.frames';
    vi.mocked(api.debugFile)
      .mockResolvedValueOnce({ ...preview, file, encoding: 'websocket' })
      .mockResolvedValueOnce({ ...preview, file, encoding: 'base64', content: 'AP8=' });
    fireEvent.change(getByLabelText(root, 'File'), { target: { value: file } });
    await waitFor(() => expect(root).toHaveTextContent('Decoded WebSocket'));
    expect(root).toHaveTextContent('~200K');
    expect(
      root.querySelector('.debug-preview')?.parentElement?.querySelector('p span'),
    ).toHaveAttribute('title', 'Total wire: 200,000 bytes');
    expect(api.debugFile).toHaveBeenLastCalledWith('newest', file, 0, 65536, 'decoded');
    expect(root.querySelector('pre')?.textContent).toBe(preview.content);
    expect(root.querySelector('script')).toBeNull();
    expect(root).toHaveTextContent('Compressed WebSocket payloads are not decoded');
    expect(root).toHaveTextContent('Showing part of capture');
    expect(queryByRole(root, 'button', { name: 'End' })).toBeNull();
    const raw = getByRole(root, 'button', { name: 'Raw bytes' });
    expect(raw).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(raw);
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('AP8='));
    expect(root).toHaveTextContent('Binary · base64');
    expect(raw).toHaveAttribute('aria-pressed', 'true');
    expect(api.debugFile).toHaveBeenLastCalledWith('newest', file, 0, 65536);
    expect(getByRole(root, 'button', { name: 'End' })).toBeEnabled();
    expect(root.querySelector('a')).toBeNull();
  });

  it('pages decoded frames by server cursor and previous offsets, independent of wire span and truncation', async () => {
    const { root, api } = setup();
    await ready(root);
    const file = 'websocket-client.frames';
    vi.mocked(api.debugFile).mockImplementation(async (id, file, offset = 0) => ({
      ...preview,
      id,
      file,
      encoding: 'websocket',
      offset,
      content: `Frame at ${offset}`,
      total_bytes: 1000000,
      bytes_returned: offset === 251234 ? 3 : 780000,
      ...(offset === 0 ? { next_offset: 71321 } : offset === 71321 ? { next_offset: 251234 } : {}),
      truncated: offset !== 0,
    }));
    choose(root);
    fireEvent.change(getByLabelText(root, 'File'), { target: { value: file } });
    const next = getByRole(root, 'button', { name: 'Next' });
    const previous = getByRole(root, 'button', { name: 'Previous' });
    const start = getByRole(root, 'button', { name: 'Start' });
    async function frame(offset: number) {
      await waitFor(() =>
        expect(root.querySelector('pre')?.textContent).toBe(`Frame at ${offset}`),
      );
      expect(api.debugFile).toHaveBeenLastCalledWith('newest', file, offset, 65536, 'decoded');
    }
    await frame(0);
    expect(previous).toBeDisabled();
    expect(root).not.toHaveTextContent('Showing part of capture');
    fireEvent.click(next);
    await frame(71321);
    fireEvent.click(next);
    await frame(251234);
    expect(root).toHaveTextContent('Showing part of capture');
    expect(next).toBeDisabled();
    fireEvent.click(previous);
    await frame(71321);
    fireEvent.click(previous);
    await frame(0);
    expect(previous).toBeDisabled();
    fireEvent.click(next);
    await frame(71321);
    fireEvent.click(start);
    await frame(0);
    expect(previous).toBeDisabled();
  });

  it('ignores a decoded response that arrives after switching the same file to raw', async () => {
    const { root, api } = setup();
    await ready(root);
    choose(root);
    await waitFor(() => expect(root).toHaveTextContent('UTF-8'));
    const file = 'websocket-server.frames';
    let complete!: (result: ProxyDebugPreview) => void;
    vi.mocked(api.debugFile)
      .mockReturnValueOnce(
        new Promise((resolve) => {
          complete = resolve;
        }),
      )
      .mockResolvedValueOnce({ ...preview, file, encoding: 'base64', content: 'AP8=' });
    fireEvent.change(getByLabelText(root, 'File'), { target: { value: file } });
    fireEvent.click(getByRole(root, 'button', { name: 'Raw bytes' }));
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('AP8='));
    complete({ ...preview, file, encoding: 'websocket', content: 'stale decoded page' });
    await Promise.resolve();
    await Promise.resolve();
    expect(root.querySelector('pre')?.textContent).toBe('AP8=');
    expect(root).toHaveTextContent('Binary · base64');
    expect(getByRole(root, 'button', { name: 'End' })).toBeEnabled();
  });

  it('pages manually to the tail without accumulating bodies and resets offsets on file changes', async () => {
    const { root, api } = setup();
    await ready(root);
    vi.mocked(api.debugFile).mockImplementation(async (_id, _file, offset = 0) => ({
      ...preview,
      offset,
      content: `Chunk ${offset}`,
    }));
    choose(root);
    await waitFor(() => expect(getByRole(root, 'button', { name: 'Next' })).not.toBeDisabled());
    fireEvent.click(getByRole(root, 'button', { name: 'Next' }));
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('Chunk 65536'));
    fireEvent.click(getByRole(root, 'button', { name: 'End' }));
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('Chunk 134464'));
    expect(getByRole(root, 'button', { name: 'Next' })).toBeDisabled();
    fireEvent.click(getByRole(root, 'button', { name: 'Previous' }));
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('Chunk 68928'));
    fireEvent.click(getByRole(root, 'button', { name: 'Start' }));
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('Chunk 0'));
    expect(getByRole(root, 'button', { name: 'Previous' })).toBeDisabled();
    fireEvent.change(getByLabelText(root, 'File'), { target: { value: 'response.body' } });
    await waitFor(() =>
      expect(api.debugFile).toHaveBeenLastCalledWith('newest', 'response.body', 0, 65536),
    );
  });

  it('advances by bytes_returned, not string length, and treats truncated tail previews as the last page', async () => {
    const { root, api } = setup();
    await ready(root);
    vi.mocked(api.debugFile)
      .mockResolvedValueOnce({ ...preview, content: 'café', total_bytes: 12, bytes_returned: 5 })
      .mockResolvedValueOnce({
        ...preview,
        content: 'last',
        total_bytes: 12,
        offset: 5,
        bytes_returned: 7,
        truncated: true,
      });
    choose(root);
    await waitFor(() => expect(getByRole(root, 'button', { name: 'Next' })).not.toBeDisabled());
    fireEvent.click(getByRole(root, 'button', { name: 'Next' }));
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe('last'));
    expect(api.debugFile).toHaveBeenLastCalledWith('newest', 'manifest.json', 5, 65536);
    expect(root).toHaveTextContent('Truncated preview');
    expect(getByRole(root, 'button', { name: 'Next' })).toBeDisabled();
  });

  it('discards old previews after changing files, refresh and disposal; never caches bodies', async () => {
    const { root, api, dispose } = setup();
    await ready(root);
    let complete!: (result: ProxyDebugPreview) => void;
    vi.mocked(api.debugFile).mockReturnValueOnce(
      new Promise((resolve) => {
        complete = resolve;
      }),
    );
    choose(root);
    fireEvent.change(getByLabelText(root, 'File'), { target: { value: 'response.body' } });
    await waitFor(() => expect(root.querySelector('pre')?.textContent).toBe(preview.content));
    complete({ ...preview, content: 'stale' });
    await Promise.resolve();
    expect(root.querySelector('pre')?.textContent).toBe(preview.content);
    fireEvent.click(getByRole(root, 'button', { name: 'Refresh captures' }));
    expect(root.querySelector('pre')?.textContent).toBe('');
    await ready(root);
    choose(root);
    await waitFor(() => expect(api.debugFile).toHaveBeenCalledTimes(3));
    dispose();
    expect(root.querySelector('pre')?.textContent).toBe('');
  });

  it('does not surface stale authentication failures after navigation', async () => {
    const { root, api, dispose, errors } = setup();
    await ready(root);
    let reject!: (error: Error) => void;
    vi.mocked(api.debugFile).mockReturnValueOnce(
      new Promise((_resolve, failure) => {
        reject = failure;
      }),
    );
    choose(root);
    dispose();
    root.remove();
    reject(new ProxyAPIError(401, 'Please sign in.'));
    await Promise.resolve();
    await Promise.resolve();
    expect(errors).not.toHaveBeenCalled();
  });
});

describe('debug admin transport', () => {
  it('uses session credentials, CSRF on writes, no-store reads, and explicit acknowledgement only when enabling', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockImplementation(async (url) =>
        String(url).endsWith('/state')
          ? Response.json({ csrf: 'session' })
          : Response.json({ enabled: false, captures: [] }),
      );
    const api = new ProxyClient(fetcher);
    await api.state();
    await api.debug();
    await api.setDebug(true);
    await api.setDebug(false);
    await api.debugCaptures();
    await api.debugFile('a/b ?#', 'request.body');
    await api.debugCapture('a/b ?#');
    await api.debugFile('capture', 'websocket-client.frames', 71321, 32768, 'decoded');
    for (const [, options] of fetcher.mock.calls)
      expect(options).toMatchObject({
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'error',
      });
    expect(fetcher.mock.calls[2]).toEqual([
      '/_proxy/api/debug',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ 'X-CSRF-Token': 'session' }),
        body: '{"enabled":true,"acknowledge_sensitive":true}',
      }),
    ]);
    expect(fetcher.mock.calls[3][1]?.body).toBe('{"enabled":false}');
    expect(fetcher.mock.calls[4][0]).toBe('/_proxy/api/debug/captures?limit=50');
    expect(fetcher.mock.calls[5][0]).toBe(
      '/_proxy/api/debug/captures/a%2Fb%20%3F%23/request.body?offset=0&limit=65536',
    );
    expect(fetcher.mock.calls[6]).toEqual([
      '/_proxy/api/debug/captures/a%2Fb%20%3F%23',
      expect.objectContaining({ method: 'GET', headers: { Accept: 'application/json' } }),
    ]);
    expect(fetcher.mock.calls[7][0]).toBe(
      '/_proxy/api/debug/captures/capture/websocket-client.frames?offset=71321&limit=32768&view=decoded',
    );
  });

  it('does not clear the current CSRF token on late capture 401s', async () => {
    const fetcher = vi.fn<typeof fetch>();
    const api = new ProxyClient(fetcher);
    let respond!: (response: Response) => void;
    fetcher.mockReturnValueOnce(
      new Promise((resolve) => {
        respond = resolve;
      }),
    );
    const lookup = api.debugCapture('old-request');
    fetcher.mockResolvedValueOnce(Response.json({ csrf: 'current-session' }));
    await api.state();
    respond(new Response('private', { status: 401 }));
    await expect(lookup).rejects.toMatchObject({ status: 401 });
    fetcher.mockResolvedValueOnce(Response.json({ enabled: false }));
    await api.setDebug(false);
    expect(fetcher.mock.lastCall?.[1]?.headers).toMatchObject({
      'X-CSRF-Token': 'current-session',
    });
  });

  it.each([401, 403])('keeps authentication failure %s credential-safe', async (status) => {
    const api = new ProxyClient(
      vi.fn<typeof fetch>().mockResolvedValue(new Response('raw secret prompt', { status })),
    );
    await expect(api.debugFile('capture', 'response.body')).rejects.toMatchObject({ status });
    await expect(api.setDebug(true)).rejects.not.toThrow('raw secret prompt');
  });
});
