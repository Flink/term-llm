import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test, type Page } from '@playwright/test';
import type { ProxyMoneySettings, ProxyState } from '../src/api/proxy-client';
import type {
  ChartGroup,
  ChartRange,
  MoneyTimeline,
  TimelineSeries,
} from '../src/api/proxy-timeline';

// Like proxy-layout.spec.ts, exercise the built standalone UI without a server or credentials.
const dist =
  process.env.TERM_LLM_PROXY_DIST ??
  resolve(import.meta.dirname, '../../internal/serveui/static/dist');
function asset(name: string): Buffer {
  try {
    return readFileSync(resolve(dist, name));
  } catch {
    return gunzipSync(readFileSync(resolve(dist, `${name}.gz`)));
  }
}
const javascript = asset('proxy.js');
const stylesheet = asset('proxy.css');
const home = 'http://proxy.test/_proxy/';
const snapshot: ProxyState = {
  defaults: null,
  auth: 'passkey',
  mode: 'ui',
  revision: 1,
  routes: [],
  providers: [],
  secrets: [],
  callers: [],
  admin_keys: [],
  csrf: 'synthetic-timeline-csrf',
};

function timeline(range: ChartRange, group: ChartGroup): MoneyTimeline {
  const labels = {
    model: [
      'Claude Opus 4.7',
      'GPT-6.2',
      'Gemini 4',
      'Claude Sonnet 5',
      'Grok 4.7',
      'GLM 5.3',
      'Gemini Flash',
      'Mini',
      'Local',
    ],
    client: ['Studio desktop', 'Kitchen Mac'],
    provider: ['Example provider'],
    total: ['All activity'],
  }[group];
  const slots = { today: 12, '7d': 7, '30d': 30 }[range];
  const interval = range === 'today' ? 3600000 : 86400000;
  const end = Date.parse(
    range === 'today' ? '2026-11-20T12:00:00+11:00' : '2026-11-21T00:00:00+11:00',
  );
  const buckets = Array.from({ length: slots }, (_, i) => {
    const start = new Date(end - (slots - i) * interval).toISOString();
    const series = labels.map((label, j): TimelineSeries => {
      const variation = 5 + ((i * 13 + j * 3) % 9) + (i === 20 ? 9 : 0);
      const micros = variation * [120000, 80000, 55000, 45000, 25000, 20000, 1000, 100, 20][j]!;
      return {
        key: `${group}-${j}`,
        label,
        costs: [
          {
            currency: 'USD',
            micros,
            estimated: j === 0,
            ...(j === 0 ? { plan_estimate_micros: micros } : {}),
          },
          { currency: 'AUD', micros: micros * 2, estimated: false },
        ],
        tokens: micros / 2,
        token_parts: {
          input: micros / 5,
          output: micros / 10,
          cache_read: micros / 10,
          cache_write: micros / 10,
        },
        unknown_cost: j === 4 && i % 7 === 0 ? 1 : 0,
        unknown_tokens: 0,
        plan: j === 0 ? 4 : 0,
      };
    });
    // One expensive use must outrank the frequent inexpensive models on Cost.
    if (group === 'model' && i === slots - 1)
      series.push({
        key: 'rare-pro',
        label: 'Rare Pro',
        costs: [{ currency: 'USD', micros: 39000000, estimated: false }],
        tokens: 10,
        token_parts: { input: 4, output: 3, cache_read: 2, cache_write: 1 },
        unknown_cost: 0,
        unknown_tokens: 0,
        plan: 0,
      });
    return {
      start,
      end: new Date(end - (slots - i - 1) * interval).toISOString(),
      label: start,
      series,
    };
  });
  return {
    timezone: 'Australia/Sydney',
    range,
    start: buckets[0]!.start,
    end: new Date(end).toISOString(),
    currencies: ['USD', 'AUD'],
    buckets,
    previous: {
      key: 'total',
      label: 'Total',
      costs: [
        { currency: 'USD', micros: 60000000, estimated: true },
        { currency: 'AUD', micros: 120000000, estimated: false },
      ],
      tokens: 30000000,
      unknown_cost: 0,
      unknown_tokens: 0,
      plan: 0,
    },
  };
}

test.beforeEach(async ({ page }) => {
  let settings: ProxyMoneySettings = { timezone: 'Australia/Sydney', rates: [] };
  await page.route('http://proxy.test/**', async (route) => {
    const url = new URL(route.request().url());
    switch (url.pathname) {
      case '/_proxy/assets/proxy.js':
        return route.fulfill({ contentType: 'text/javascript', body: javascript });
      case '/_proxy/assets/proxy.css':
        return route.fulfill({ contentType: 'text/css; charset=utf-8', body: stylesheet });
      case '/_proxy/api/auth/status':
        return route.fulfill({ json: { auth: 'passkey', origin: 'http://proxy.test' } });
      case '/_proxy/api/state':
        return route.fulfill({ json: snapshot });
      case '/_proxy/api/timeline': {
        const range = url.searchParams.get('range');
        const group = url.searchParams.get('group');
        if (
          (range !== 'today' && range !== '7d' && range !== '30d') ||
          (group !== 'model' && group !== 'client' && group !== 'provider' && group !== 'total')
        )
          return route.fulfill({
            status: 400,
            json: { error: 'Expected a timeline range and group' },
          });
        return route.fulfill({ json: timeline(range, group) });
      }
      case '/_proxy/api/money':
        if (route.request().method() === 'PUT') {
          settings = route.request().postDataJSON() as ProxyMoneySettings;
          return route.fulfill({ status: 204 });
        }
        return route.fulfill({ json: settings });
      case '/_proxy/':
        return route.fulfill({
          contentType: 'text/html',
          body: '<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/_proxy/assets/proxy.css"></head><body><div id="proxy-app"></div><script type="module" src="/_proxy/assets/proxy.js"></script></body></html>',
        });
      default:
        return route.abort();
    }
  });
});

async function expectQuery(page: Page, expected: Record<string, string>) {
  await expect.poll(() => Object.fromEntries(new URL(page.url()).searchParams)).toEqual(expected);
}

const defaultQuery = { range: '30d', group: 'model', metric: 'cost' };

test('Home filter controls persist across reload and bare URLs; a shared query wins as a whole', async ({
  page,
}) => {
  await page.goto(home);
  const chart = page.locator('.timeline');
  const group = chart.getByRole('combobox', { name: 'Grouping' });
  const currency = chart.getByRole('combobox', { name: 'Currency' });
  const pressed = (name: string) =>
    expect(chart.getByRole('button', { name, exact: true })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  await expectQuery(page, defaultQuery);
  await expect(group).toHaveValue('model');
  await pressed('30d');
  await pressed('Cost');
  await expect(chart.locator('[data-bucket]')).toHaveCount(30);

  await chart.getByRole('button', { name: '7d', exact: true }).click();
  await expect(chart.locator('[data-bucket]')).toHaveCount(7);
  await group.selectOption('client');
  await expect(chart.locator('.timeline-breakdown')).toContainText('Studio desktop');
  await currency.selectOption('AUD');
  await expect(chart.locator('.timeline-total')).toContainText('A$');
  await expectQuery(page, { range: '7d', group: 'client', metric: 'cost', currency: 'AUD' });
  await chart.getByRole('button', { name: 'Tokens', exact: true }).click();
  await expect(chart.locator('.timeline-total')).toContainText('tokens');
  await expect(currency).toBeHidden();
  const savedQuery = { range: '7d', group: 'client', metric: 'tokens', currency: 'AUD' };
  await expectQuery(page, savedQuery);

  for (const enterHome of [() => page.reload(), () => page.goto(home)]) {
    await enterHome();
    await expect(chart.locator('.timeline-breakdown')).toContainText('Studio desktop');
    await expect(chart.locator('[data-bucket]')).toHaveCount(7);
    await pressed('7d');
    await pressed('Tokens');
    await expect(group).toHaveValue('client');
    await expectQuery(page, savedQuery);
  }

  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  await expectQuery(page, { tab: 'routes' });
  await page.getByRole('button', { name: 'Home', exact: true }).click();
  await expectQuery(page, savedQuery);

  // Omitted query fields must use defaults, not the conflicting saved view.
  await page.goto(`${home}?range=today`);
  await pressed('Today');
  await pressed('Cost');
  await expect(group).toHaveValue('model');
  await expect(currency).toHaveValue('USD');
  await expect(chart.locator('[data-bucket]')).toHaveCount(12);
  await expectQuery(page, { ...defaultQuery, range: 'today' });
  await chart.getByRole('button', { name: 'Reset view', exact: true }).click();
  await expect(chart.locator('[data-bucket]')).toHaveCount(30);
  await expectQuery(page, defaultQuery);
});

test('Home opens and saves money settings without losing the selected timeline view', async ({
  page,
}) => {
  await page.goto(`${home}?range=7d&group=model&metric=cost`);
  const chart = page.locator('.timeline');
  await expect(chart.locator('[data-bucket]')).toHaveCount(7);
  await expect(page.getByLabel('Billing timezone')).toBeHidden();
  await chart.locator('.timeline-footer').getByText('Money settings', { exact: true }).click();
  const timezone = page.getByLabel('Billing timezone');
  await expect(timezone).toHaveValue('Australia/Sydney');
  await timezone.fill('UTC');
  const save = page.waitForRequest(
    (request) => request.url() === `${home}api/money` && request.method() === 'PUT',
  );
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  const request = await save;
  expect(request.postDataJSON()).toEqual({ timezone: 'UTC', rates: [] });
  expect(request.headers()['x-csrf-token']).toBe(snapshot.csrf);
  await expect(page.getByText('Money settings saved.', { exact: true })).toBeVisible();
  await expect(chart.locator('[data-bucket]')).toHaveCount(7);
  await expectQuery(page, { ...defaultQuery, range: '7d' });
});

test('Home timeline fits and renders the chart and breakdown on desktop and mobile', async ({
  page,
}, testInfo) => {
  await page.goto(home);
  const chart = page.locator('.timeline');
  const bars = chart.locator('[data-bucket]');
  await expect(bars).toHaveCount(30);
  await expect(chart.locator('.timeline-breakdown')).toContainText('Claude Opus 4.7');
  await expect(chart.locator('.timeline-footer')).toContainText('Australia/Sydney');
  await expect(chart.locator('.timeline-footer')).toContainText(
    '5 requests with incomplete cost data',
  );
  if (testInfo.project.name === 'mobile') {
    await bars.nth(20).tap();
  } else {
    await bars.nth(19).focus();
    await bars.nth(19).press('ArrowRight');
    await expect(bars.nth(20)).toBeFocused();
  }
  await expect(bars.nth(20)).toHaveAttribute('aria-pressed', 'true');
  await expect(chart.locator('.timeline-tooltip')).toContainText('Claude Opus 4.7');
  const expectNoOverflow = () =>
    expect
      .poll(() =>
        page.evaluate(
          () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
        ),
      )
      .toBeLessThanOrEqual(0);
  await expectNoOverflow();
  await page.screenshot({
    path: testInfo.outputPath(`proxy-timeline-${testInfo.project.name}.png`),
    fullPage: true,
  });
  if (testInfo.project.name === 'mobile') {
    await page.setViewportSize({ width: 320, height: 844 });
    await expectNoOverflow();
    await chart.getByRole('button', { name: 'Tokens', exact: true }).tap();
    await expect(chart.locator('.timeline-total')).toContainText('tokens');
    await expectNoOverflow();
  }
});

test('Home names even one-off expensive models and exposes the four token components', async ({
  page,
}) => {
  await page.goto(home);
  const rail = page.locator('.timeline-breakdown');
  const lead = rail.locator('li').first();
  await expect(lead).toContainText('Rare Pro');
  await expect(lead).toContainText('US$39.00');
  await rail.locator('summary').click();
  await expect(rail.getByText('Local', { exact: true })).toBeVisible();
  await expect(rail.locator('li')).toHaveCount(10);
  await page.getByRole('button', { name: 'Tokens', exact: true }).click();
  const mix = page.getByRole('region', { name: 'Token mix' });
  for (const label of ['Input', 'Output', 'Cache read', 'Cache write'])
    await expect(mix.getByText(label, { exact: true })).toBeVisible();
  const rare = rail.locator('li').filter({ hasText: 'Rare Pro' });
  await expect(rare).toContainText('Input: 4 · Output: 3 · Cache read: 2 · Cache write: 1');
  await page.locator('[data-bucket="29"]').focus();
  await expect(page.locator('.timeline-tooltip')).toContainText(
    'Input: 4 · Output: 3 · Cache read: 2 · Cache write: 1',
  );
  await page.getByRole('button', { name: 'Cost', exact: true }).click();
  await expect(mix).toHaveCount(0);
});

test('A crowded interval shows its top 20 and the rest can be reached from the card', async ({
  page,
  isMobile,
}) => {
  const fixture = timeline('30d', 'model');
  const bucket = fixture.buckets[0]!;
  bucket.series = Array.from({ length: 23 }, (_, i) => ({
    ...bucket.series[0]!,
    key: `crowded-${i}`,
    label: `Contributor ${i}`,
    costs: [{ currency: 'USD', micros: (i + 1) * 1_000_000, estimated: false }],
    plan: 0,
  }));
  await page.route('**/_proxy/api/timeline?**', (route) => route.fulfill({ json: fixture }));
  await page.goto(home);
  const bar = page.locator('[data-bucket="0"]');
  if (isMobile) await bar.tap();
  else await bar.hover();
  const tip = page.locator('.timeline-tooltip');
  await expect(tip).toContainText('Top 20 of 23');
  const rows = tip.locator('.timeline-tooltip-row');
  await expect(rows).toHaveCount(20);
  await expect(rows.first()).toContainText('Contributor 22');
  await expect(tip.locator('.timeline-tooltip-total')).toContainText('US$276.00');
  const more = tip.getByText('3 more contributors');
  if (isMobile) await more.tap();
  else await more.click();
  await expect(rows).toHaveCount(23);
  await expect(rows.last()).toContainText('Contributor 0');
  await tip.getByRole('button', { name: 'Dismiss bucket details' }).click();
  await expect(tip).toBeHidden();
});
