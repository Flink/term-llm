import {
  ProxyAPIError,
  type ProxyClient,
  type ProxyDebugCaptureFiles,
  type ProxyDebugFile,
} from '../src/api/proxy-client';
import { byteValue } from './bytes';
import { actions, button, el, field, select, titledValue, type UIContext } from './dom';

// Don't render an old result or surface its error (including 401) in another view.
export function runWhileLive(
  context: UIContext,
  live: () => boolean,
  action: () => Promise<void>,
  source?: HTMLElement,
) {
  return context.run(async () => {
    if (!live()) return;
    try {
      await action();
    } catch (error) {
      if (live()) throw error;
    }
  }, source);
}

const files: ProxyDebugFile[] = [
  'manifest.json',
  'request.body',
  'response.body',
  'websocket-client.frames',
  'websocket-server.frames',
];

const decodedHistoryLimit = 100;

// The same bounded, text-only viewer is used in Debug and Requests. Nothing is
// cached, accumulated between pages, put into the browser URL, or downloaded.
export function capturePreview(api: ProxyClient, context: UIContext) {
  let item: ProxyDebugCaptureFiles | undefined;
  let chunk = 65536;
  let revision = 0;
  let offset = 0;
  let nextOffset: number | undefined;
  let total = 0;
  let decoded = false;
  const history: number[] = [];
  const file = select(
    files.map((name) => [name, name]),
    'manifest.json',
  );
  const info = el('p');
  info.setAttribute('aria-live', 'polite');
  const preview = el('pre');
  preview.tabIndex = 0;
  preview.setAttribute('aria-label', 'Capture preview');
  preview.className = 'debug-preview';
  const raw = button('Raw bytes', () => {
    decoded = !decoded;
    restart();
  });
  const first = button('Start', restart);
  const previous = button('Previous', () => {
    const start = decoded ? history.pop() : Math.max(0, offset - chunk);
    if (start !== undefined) inspect(start);
  });
  const next = button('Next', () => {
    if (nextOffset === undefined) return;
    if (decoded) {
      if (history.length === decodedHistoryLimit) history.shift();
      history.push(offset);
    }
    inspect(nextOffset);
  });
  const last = button('End', () => inspect(Math.max(0, total - chunk)));
  const paging = [first, previous, next, last];
  const element = el('div', field('File', file), info, actions(raw, ...paging), preview);
  function restart() {
    history.length = 0;
    raw.setAttribute('aria-pressed', String(!decoded));
    // Decoding cannot start from an arbitrary wire offset.
    last.hidden = decoded;
    inspect();
  }
  function selectFile() {
    const name = file.value as ProxyDebugFile;
    decoded =
      !!item?.files.includes(name) &&
      (name === 'websocket-client.frames' || name === 'websocket-server.frames');
    raw.hidden = !decoded;
    restart();
  }
  function inspect(start = 0) {
    const current = ++revision;
    preview.textContent = '';
    info.textContent = '';
    nextOffset = undefined;
    for (const control of paging) control.disabled = true;
    const id = item?.id;
    const name = file.value as ProxyDebugFile;
    if (!id || !item?.files.includes(name) || !files.includes(name)) return;
    const view = decoded ? 'decoded' : undefined;
    const live = () => element.isConnected && current === revision;
    info.textContent = 'Loading preview…';
    void runWhileLive(context, live, async () => {
      const request = view
        ? api.debugFile(id, name, start, chunk, view)
        : api.debugFile(id, name, start, chunk);
      const result = await request.catch((error: unknown) => {
        if (!(error instanceof ProxyAPIError) || error.status !== 404) throw error;
      });
      if (!live()) return;
      if (
        !result ||
        result.id !== id ||
        result.file !== name ||
        (decoded
          ? result.encoding !== 'websocket'
          : !result.bytes_returned || result.encoding === 'websocket')
      ) {
        info.textContent = 'Preview unavailable.';
        return;
      }
      info.replaceChildren(
        `${decoded ? 'Decoded WebSocket' : result.encoding === 'base64' ? 'Binary · base64' : 'UTF-8'} · `,
        titledValue(byteValue(result.total_bytes, decoded ? 'Total wire' : 'Total')),
        ` · Offset ${result.offset}${result.truncated ? ` · ${decoded ? 'Showing part of capture' : 'Truncated preview'}` : ''}${decoded ? ' · Compressed WebSocket payloads are not decoded.' : ''}`,
      );
      preview.textContent = result.content;
      offset = result.offset;
      total = result.total_bytes;
      if (decoded) {
        const cursor = result.next_offset;
        if (cursor !== undefined && Number.isSafeInteger(cursor) && cursor > offset) {
          nextOffset = cursor;
        }
      } else if (offset + result.bytes_returned < total) {
        nextOffset = offset + result.bytes_returned;
      }
      first.disabled = offset === 0;
      previous.disabled = decoded ? history.length === 0 : offset === 0;
      next.disabled = nextOffset === undefined;
      last.disabled = decoded || next.disabled;
    }).then((ok) => {
      if (!ok && live()) info.textContent = 'Preview unavailable.';
    });
  }
  file.addEventListener('change', selectFile);
  selectFile();
  return {
    element,
    show(capture: ProxyDebugCaptureFiles, limit = 65536) {
      item = capture;
      chunk = limit;
      for (const option of file.options)
        option.disabled = !capture.files.includes(option.value as ProxyDebugFile);
      if (!file.value || file.selectedOptions[0]?.disabled)
        file.value = [...file.options].find((option) => !option.disabled)?.value ?? '';
      selectFile();
    },
    clear() {
      item = undefined;
      selectFile();
    },
  };
}

export function requestCapture(api: ProxyClient, context: UIContext, id: string) {
  const status = el('p', 'Checking for captured payload…');
  status.setAttribute('aria-live', 'polite');
  const element = el('div', el('h3', 'Raw payload capture'), status);
  const live = () => element.isConnected;
  const absent = 'No raw payload capture for this request.';
  queueMicrotask(() => {
    if (!live()) return;
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(id ?? '')) {
      status.textContent = absent;
      return;
    }
    void runWhileLive(context, live, async () => {
      const capture = await api.debugCapture(id).catch((error: unknown) => {
        if (!(error instanceof ProxyAPIError) || error.status !== 404) throw error;
      });
      if (!live()) return;
      if (!capture || capture.id !== id) {
        status.textContent = absent;
        return;
      }
      const payloadFiles = files.filter(
        (name) => name !== 'manifest.json' && capture.files?.includes(name),
      );
      if (!payloadFiles.length) {
        status.textContent = capture.finished
          ? 'This capture has no stored payload data.'
          : 'No payload data yet. Reopen the request to check again.';
        return;
      }
      status.textContent = `${capture.finished ? '' : 'Capture unfinished; data may be incomplete. '}Raw payloads may contain secrets.`;
      const reveal = button('View raw payload', () => {
        const viewer = capturePreview(api, context);
        reveal.replaceWith(viewer.element);
        viewer.show({ ...capture, files: payloadFiles });
        viewer.element.querySelector('select')?.focus();
      });
      element.append(reveal);
    }).then((ok) => {
      if (!ok && live()) status.textContent = 'Capture unavailable.';
    });
  });
  return element;
}
