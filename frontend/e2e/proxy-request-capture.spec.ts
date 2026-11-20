import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test } from '@playwright/test';
import type { ProxyRequest, ProxyState } from '../src/api/proxy-client';

const dist =
  process.env.TERM_LLM_PROXY_DIST ??
  resolve(import.meta.dirname, '../../internal/serveui/static/dist');
function asset(name: string) {
  try {
    return readFileSync(resolve(dist, name));
  } catch {
    return gunzipSync(readFileSync(resolve(dist, `${name}.gz`)));
  }
}
const js = asset('proxy.js');
const css = asset('proxy.css');
const snapshot: ProxyState = {
  defaults: null,
  providers: [],
  auth: 'token',
  mode: 'ui',
  revision: 1,
  routes: [],
  callers: [],
  admin_keys: [],
  secrets: [],
  csrf: 'synthetic-csrf',
};
const base = 'https://proxy.test/_proxy/';
const old = 'px_older_than_recent_window';
const absent = 'px_uncaptured';
const records = [absent, old].map((id, i): ProxyRequest => ({
  id,
  seq: 2 - i,
  started: 1700000000100 - i,
  ended: 1700000000200 - i,
  method: 'POST',
  path: i === 0 ? '/uncaptured' : '/captured',
  caller_id: 'caller',
  caller_name: 'Client',
  route: 'fixture',
  revision: 1,
  peer: '',
  status: 200,
  outcome: 'complete',
  sent: 0,
  received: 0,
  header_ms: 1,
  first_byte_ms: 2,
  total_ms: 5,
  upstream_id: '',
}));

function latch() {
  let release!: () => void;
  const waiting = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { waiting, release };
}

test('Requests checks only the selected capture and reveals transient, paged text on demand', async ({
  page,
}) => {
  const debug: string[] = [];
  const delayed = latch();
  const delayedSent = latch();
  const payload = '<img src=x onerror="window.leaked=true"> private café';
  await page.route(`${base}**`, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    switch (url.pathname.slice('/_proxy/'.length)) {
      case '':
        return route.fulfill({
          contentType: 'text/html',
          body: '<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="assets/proxy.css"></head><body><div id="proxy-app"></div><script type="module" src="assets/proxy.js"></script></body></html>',
        });
      case 'assets/proxy.js':
        return route.fulfill({ contentType: 'text/javascript', body: js });
      case 'assets/proxy.css':
        return route.fulfill({ contentType: 'text/css', body: css });
      case 'api/auth/status':
        return route.fulfill({ json: { auth: 'token' } });
      case 'api/state':
        return route.fulfill({ json: snapshot });
      case 'api/requests':
        return route.fulfill({
          json: { requests: records, total: 2, next: 0, page: 1, page_size: 25 },
        });
    }
    if (!url.pathname.includes('/api/debug')) return route.abort();
    debug.push(`${req.method()} ${url.pathname.slice('/_proxy/api/'.length)}${url.search}`);
    expect(req.method()).toBe('GET');
    if (url.pathname.endsWith(`/${absent}`)) return route.fulfill({ status: 404 });
    if (url.pathname.endsWith(`/${old}`))
      return route.fulfill({
        json: {
          id: old,
          finished: true,
          files: ['request.body', 'response.body', 'websocket-client.frames'],
        },
      });
    const offset = Number(url.searchParams.get('offset'));
    const file = url.pathname.split('/').at(-1);
    if (!url.pathname.includes(`/${old}/`) || url.searchParams.get('limit') !== '65536')
      return route.abort();
    if (file === 'websocket-client.frames') {
      const decoded = url.searchParams.get('view') === 'decoded';
      return route.fulfill({
        json: {
          id: old,
          file,
          offset,
          total_bytes: 32,
          bytes_returned: 32,
          encoding: decoded ? 'websocket' : 'base64',
          truncated: false,
          content: decoded ? 'Offset 0 · Text\n{ "type": "response.create" }' : 'gf4D',
        },
      });
    }
    if (file === 'response.body') {
      await delayed.waiting;
      await route.fulfill({ status: 401 });
      delayedSent.release();
      return;
    }
    return route.fulfill({
      json: {
        id: old,
        file,
        offset,
        total_bytes: 65540,
        bytes_returned: offset ? 4 : 65536,
        encoding: 'utf8',
        truncated: true,
        content: offset ? 'tail' : payload,
      },
    });
  });
  await page.goto(`${base}?tab=requests`);
  const missingRow = page.getByRole('button', { name: 'POST /uncaptured', exact: true });
  const captureRow = page.getByRole('button', { name: 'POST /captured', exact: true });
  const reveal = page.getByRole('button', { name: 'View raw payload' });
  const preview = page.locator('pre[aria-label="Capture preview"]');
  await expect(captureRow).toBeVisible();
  expect(debug).toEqual([]);
  await missingRow.click();
  await expect(page.getByText('No raw payload capture for this request.')).toBeVisible();
  await expect(reveal).toHaveCount(0);
  await captureRow.click();
  await expect(reveal).toBeVisible();
  expect(debug).toEqual([`GET debug/captures/${absent}`, `GET debug/captures/${old}`]);
  await expect(preview).toHaveCount(0);
  await reveal.click();
  await expect(preview).toHaveText(payload);
  await expect(preview.locator('img')).toHaveCount(0);
  await expect(page.getByRole('combobox', { name: 'File', exact: true })).toBeFocused();
  const details = page.locator('.request-detail');
  await details.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(preview).toHaveText('tail');
  await expect(details.getByRole('button', { name: 'Next', exact: true })).toBeDisabled();
  expect(debug.slice(2)).toEqual([
    `GET debug/captures/${old}/request.body?offset=0&limit=65536`,
    `GET debug/captures/${old}/request.body?offset=65536&limit=65536`,
  ]);
  await page.getByLabel('File', { exact: true }).selectOption('websocket-client.frames');
  await expect(preview).toContainText('response.create');
  expect(debug.at(-1)).toBe(
    `GET debug/captures/${old}/websocket-client.frames?offset=0&limit=65536&view=decoded`,
  );
  await details.getByRole('button', { name: 'Raw bytes', exact: true }).click();
  await expect(preview).toHaveText('gf4D');
  expect(debug.at(-1)).toBe(
    `GET debug/captures/${old}/websocket-client.frames?offset=0&limit=65536`,
  );
  try {
    await page.getByLabel('File', { exact: true }).selectOption('response.body');
    await expect
      .poll(() => debug.at(-1))
      .toBe(`GET debug/captures/${old}/response.body?offset=0&limit=65536`);
    await missingRow.click();
    await expect(preview).toHaveCount(0);
  } finally {
    delayed.release();
  }
  await delayedSent.waiting;
  await expect(page.getByText('No raw payload capture for this request.')).toBeVisible();
  await expect(page).toHaveURL(`${base}?tab=requests`);
  await expect(page.locator('body')).not.toContainText(payload);
  expect(
    await page.evaluate(async () => ({
      local: Object.keys(localStorage),
      session: Object.keys(sessionStorage),
      cache: await caches.keys(),
      databases: await indexedDB.databases(),
    })),
  ).toEqual({ local: [], session: [], cache: [], databases: [] });
});
