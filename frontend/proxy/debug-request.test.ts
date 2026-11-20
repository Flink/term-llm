import { fireEvent, getByRole, queryByRole, waitFor } from '@testing-library/preact';
import { afterEach, expect, it, vi } from 'vitest';
import { ProxyAPIError, ProxyClient, type ProxyDebugCaptureFiles } from '../src/api/proxy-client';
import { requestCapture } from './debug-preview';
import type { UIContext } from './dom';

const id = 'px_exact';
const metadata: ProxyDebugCaptureFiles = { id, finished: true, files: ['request.body'] };
const revealName = { name: 'View raw payload' };

function setup() {
  const api = new ProxyClient(vi.fn());
  const lookup = vi.spyOn(api, 'debugCapture').mockResolvedValue(metadata);
  const preview = vi.spyOn(api, 'debugFile');
  const errors = vi.fn();
  const context: UIContext = {
    async run(action) {
      try {
        await action();
        return true;
      } catch (err) {
        errors(err);
        return false;
      }
    },
    announce: vi.fn(),
    navigate: vi.fn(),
    refresh: vi.fn(),
    protectToken: vi.fn(),
  };
  function open(requestId = id) {
    const section = requestCapture(api, context, requestId);
    document.body.replaceChildren(section);
    return section;
  }
  return { open, lookup, preview, errors };
}

afterEach(() => document.body.replaceChildren());

it.each([
  { result: new ProxyAPIError(404, 'Not found'), text: 'No raw payload capture' },
  { result: undefined, text: 'No raw payload capture' },
  { result: { ...metadata, id: 'px_different' }, text: 'No raw payload capture' },
  { result: { ...metadata, files: ['manifest.json'] as const }, text: 'no stored payload data' },
  { result: { ...metadata, finished: false, files: [] }, text: 'No payload data yet' },
])('does not offer raw payload when exact bytes are absent: $text', async ({ result, text }) => {
  const { open, lookup, preview, errors } = setup();
  if (result instanceof Error) lookup.mockRejectedValueOnce(result);
  else lookup.mockResolvedValueOnce(result as ProxyDebugCaptureFiles);
  const section = open();
  await waitFor(() => expect(section).toHaveTextContent(text));
  expect(lookup).toHaveBeenCalledExactlyOnceWith(id);
  expect(queryByRole(section, 'button', revealName)).toBeNull();
  expect(preview).not.toHaveBeenCalled();
  expect(errors).not.toHaveBeenCalled();
});

it('skips the archive entirely when the ledger ID is missing or invalid', async () => {
  const { open, lookup } = setup();
  for (const invalid of ['', 'px/other']) {
    const section = open(invalid);
    await waitFor(() => expect(section).toHaveTextContent('No raw payload capture'));
  }
  expect(lookup).not.toHaveBeenCalled();
});

it.each(['deleted', 'wrong ID', 'wrong file'])('does not render a %s preview', async (kind) => {
  const { open, preview } = setup();
  if (kind === 'deleted') preview.mockRejectedValueOnce(new ProxyAPIError(404, 'Not found'));
  else
    preview.mockResolvedValueOnce({
      id: kind === 'wrong ID' ? 'px_other' : id,
      file: kind === 'wrong file' ? 'response.body' : 'request.body',
      offset: 0,
      total_bytes: 7,
      bytes_returned: 7,
      encoding: 'utf8',
      truncated: false,
      content: 'private',
    });
  const section = open();
  await waitFor(() => expect(getByRole(section, 'button', revealName)).toBeEnabled());
  expect(preview).not.toHaveBeenCalled();
  fireEvent.click(getByRole(section, 'button', revealName));
  await waitFor(() => expect(section).toHaveTextContent('Preview unavailable.'));
  expect(section.querySelector('pre')?.textContent).toBe('');
});

it('lets Requests switch from the raw body to any available header summary', async () => {
  const { open, lookup, preview } = setup();
  const summaries = [
    ['request.headers', 'Client request headers', { fields: [{ name: 'Content-Type' }] }],
    [
      'upstream-request.headers',
      'Upstream request headers',
      {
        fields: [{ name: 'Authorization', values: 1, non_empty: true }],
        authorization_scheme: 'bearer',
      },
    ],
    ['upstream-response.headers', 'Upstream response headers', { fields: [], status: 401 }],
  ] as const;
  lookup.mockResolvedValueOnce({
    ...metadata,
    files: ['request.body', ...summaries.map(([file]) => file)],
  });
  preview.mockImplementation(async (id, file) => {
    const content =
      file === 'request.body'
        ? '{"model":"example"}'
        : JSON.stringify(summaries.find(([name]) => name === file)?.[2], null, 2);
    return {
      id,
      file,
      offset: 0,
      total_bytes: content.length,
      bytes_returned: content.length,
      encoding: 'utf8',
      truncated: false,
      content,
    };
  });
  const section = open();
  const reveal = await waitFor(() => getByRole(section, 'button', revealName));
  expect(preview).not.toHaveBeenCalled();
  fireEvent.click(reveal);
  const dropdown = getByRole<HTMLSelectElement>(section, 'combobox', { name: 'File' });
  expect(dropdown).toHaveValue('request.body');
  expect(getByRole(dropdown, 'option', { name: 'request.body' })).toBeEnabled();
  await waitFor(() =>
    expect(section.querySelector('pre')?.textContent).toBe('{"model":"example"}'),
  );
  for (const [file, label, summary] of summaries) {
    expect(getByRole(dropdown, 'option', { name: label })).toBeEnabled();
    fireEvent.change(dropdown, { target: { value: file } });
    await waitFor(() =>
      expect(section.querySelector('pre')?.textContent).toBe(JSON.stringify(summary, null, 2)),
    );
    expect(preview).toHaveBeenLastCalledWith(id, file, 0, 65536);
    expect(section).toHaveTextContent('UTF-8');
    expect(queryByRole(section, 'button', { name: 'Raw bytes' })).toBeNull();
  }
});

it('offers header summaries when a capture has no raw payload', async () => {
  const { open, lookup, preview } = setup();
  const file = 'upstream-response.headers';
  const content = '{"fields":[],"status":401}';
  lookup.mockResolvedValueOnce({ ...metadata, files: ['manifest.json', file] });
  preview.mockResolvedValueOnce({
    id,
    file,
    offset: 0,
    total_bytes: content.length,
    bytes_returned: content.length,
    encoding: 'utf8',
    truncated: false,
    content,
  });
  const section = open();
  const reveal = await waitFor(() =>
    getByRole(section, 'button', { name: 'View header summaries' }),
  );
  expect(preview).not.toHaveBeenCalled();
  fireEvent.click(reveal);
  const dropdown = getByRole(section, 'combobox', { name: 'File' });
  expect(dropdown).toHaveValue(file);
  expect(getByRole(dropdown, 'option', { name: 'Upstream request headers' })).toBeDisabled();
  await waitFor(() => expect(section.querySelector('pre')?.textContent).toBe(content));
  expect(preview).toHaveBeenCalledExactlyOnceWith(id, file, 0, 65536);
});

it('reveals a WebSocket capture decoded, with raw bytes available on demand', async () => {
  const { open, lookup, preview } = setup();
  const file = 'websocket-server.frames';
  lookup.mockResolvedValueOnce({ ...metadata, files: [file] });
  const decoded = {
    id,
    file,
    offset: 0,
    total_bytes: 19,
    bytes_returned: 19,
    encoding: 'websocket',
    truncated: false,
    content: '<b>captured text</b>',
  } as const;
  preview
    .mockResolvedValueOnce(decoded)
    .mockResolvedValueOnce({ ...decoded, encoding: 'base64', content: 'AP8=' })
    .mockResolvedValueOnce(decoded);
  const section = open();
  const reveal = await waitFor(() => getByRole(section, 'button', revealName));
  expect(preview).not.toHaveBeenCalled();
  fireEvent.click(reveal);
  await waitFor(() => expect(section.querySelector('pre')?.textContent).toBe(decoded.content));
  expect(section.querySelector('b')).toBeNull();
  expect(preview).toHaveBeenLastCalledWith(id, file, 0, 65536, 'decoded');
  const raw = getByRole(section, 'button', { name: 'Raw bytes' });
  fireEvent.click(raw);
  await waitFor(() => expect(section.querySelector('pre')?.textContent).toBe('AP8='));
  expect(preview).toHaveBeenLastCalledWith(id, file, 0, 65536);
  fireEvent.click(raw);
  await waitFor(() => expect(section.querySelector('pre')?.textContent).toBe(decoded.content));
  expect(preview).toHaveBeenLastCalledWith(id, file, 0, 65536, 'decoded');
});

it('ignores metadata errors after leaving a row, even after reopening the same ID', async () => {
  const { open, lookup, errors } = setup();
  let reject!: (error: ProxyAPIError) => void;
  lookup.mockReturnValueOnce(
    new Promise((_resolve, fail) => {
      reject = fail;
    }),
  );
  const closed = open();
  await waitFor(() => expect(lookup).toHaveBeenCalledOnce());
  const reopened = open();
  await waitFor(() => expect(getByRole(reopened, 'button', revealName)).toBeEnabled());
  reject(new ProxyAPIError(401, 'Auth expired'));
  await new Promise((done) =>
    queueMicrotask(() =>
      queueMicrotask(() => {
        done(undefined);
      }),
    ),
  );
  expect(errors).not.toHaveBeenCalled();
  expect(queryByRole(closed, 'button', revealName)).toBeNull();
});
