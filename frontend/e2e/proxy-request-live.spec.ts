import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test } from '@playwright/test';
import type { ProxyRequest, ProxyState } from '../src/api/proxy-client';

// Exercise the standalone bundle against mocked assets and API responses only.
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
const base = 'http://proxy.test/_proxy/';
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
const row = (id: string, seq: number): ProxyRequest => ({
  id,
  seq,
  started: 1700000000000 + seq,
  ended: 1700000001000 + seq,
  method: 'GET',
  path: `/${id}`,
  caller_id: 'caller-1',
  caller_name: 'Test client',
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
});
const initial = [row('first', 2), row('other', 1)];
const secondPage = [row('older-a', 0), row('older-b', -1)];

test('Requests refreshes filtered page one without replacing unchanged rows and stops off page one', async ({
  page,
}) => {
  await page.clock.install();
  const calls: URL[] = [];
  let result = initial;
  await page.route(`${base}**`, async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.slice('/_proxy/'.length);
    switch (path) {
      case '':
        return route.fulfill({
          contentType: 'text/html',
          body: '<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="assets/proxy.css"></head><body><div id="proxy-app"></div><script type="module" src="assets/proxy.js"></script></body></html>',
        });
      case 'assets/proxy.js':
        return route.fulfill({ contentType: 'text/javascript', body: js });
      case 'assets/proxy.css':
        return route.fulfill({ contentType: 'text/css; charset=utf-8', body: css });
      case 'api/auth/status':
        return route.fulfill({ json: { auth: 'token' } });
      case 'api/state':
        return route.fulfill({ json: snapshot });
      case 'api/requests': {
        calls.push(url);
        const pageNumber = Number(url.searchParams.get('page') ?? 1);
        const records = pageNumber === 1 ? result : secondPage;
        return route.fulfill({
          json: {
            requests: records,
            total: 26,
            next: pageNumber === 1 ? 1 : 0,
            page: pageNumber,
            page_size: 25,
          },
        });
      }
    }
    return route.abort();
  });

  await page.goto(`${base}?tab=requests`);
  await expect(page.getByRole('button', { name: 'GET /first', exact: true })).toBeVisible();
  const pager = page.getByRole('navigation', { name: 'Request pages (bottom)', exact: true });
  await page.locator('.request-filters summary').click();
  const filter = page.getByLabel('Path', { exact: true });
  await filter.fill('/first');
  await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
  await expect(page.locator('.request-filters summary')).toHaveText('Filters (1)');
  await filter.fill('/draft');

  const firstRow = page.getByRole('button', { name: 'GET /first', exact: true });
  await firstRow.evaluate((button) => button.setAttribute('data-original-row', 'true'));
  const filtered = () => calls.filter((url) => url.searchParams.get('path') === '/first');
  await page.clock.runFor(1000);
  await expect.poll(() => filtered().length).toBeGreaterThanOrEqual(2);
  expect(filtered().every((url) => url.searchParams.get('page') === '1')).toBe(true);
  await expect(firstRow).toHaveAttribute('data-original-row', 'true');

  result = [row('newest', 3), ...initial];
  await page.clock.runFor(1000);
  await expect(page.getByRole('button', { name: 'GET /newest', exact: true })).toBeVisible();
  await expect(filter).toHaveValue('/draft');
  await pager.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('button', { name: 'GET /older-a', exact: true })).toBeVisible();
  const pageTwoCount = calls.length;
  await page.clock.runFor(2500);
  expect(calls).toHaveLength(pageTwoCount);

  await pager.getByRole('button', { name: 'Previous', exact: true }).click();
  await expect(firstRow).toBeVisible();
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const leftCount = calls.length;
  await page.clock.runFor(2500);
  expect(calls).toHaveLength(leftCount);
});
