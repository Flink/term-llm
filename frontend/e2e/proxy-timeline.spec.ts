import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test, type Page } from '@playwright/test';
import type { ProxyMoneySettings, ProxyState } from '../src/api/proxy-client';
import type {
  ChartGroup,
  ChartRange,
  MoneyTimeline,
  TimelineScope,
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

const timelinePath = '/_proxy/api/timeline';
const timezone = 'Australia/Sydney';
// Pin the browser's requested timezone and number formatting for exact assertions.
test.use({ timezoneId: timezone, locale: 'en-US', serviceWorkers: 'block' });
const scopeGroups = ['client', 'provider', 'model'] as const;
const clients = ['Studio desktop', 'Kitchen Mac'];
const providers = ['Example provider', 'Another provider'];
const models = [
  'Claude Opus 4.7',
  'GPT-6.2',
  'Gemini 4',
  'Claude Sonnet 5',
  'Grok 4.7',
  'GLM 5.3',
  'Gemini Flash',
  'Mini',
  'Local',
  'Rare Pro',
];

function aggregate(rows: TimelineSeries[]): TimelineSeries[] {
  const grouped = new Map<string, TimelineSeries>();
  for (const row of rows) {
    const sum = grouped.get(row.key) ?? {
      key: row.key,
      label: row.label,
      costs: [],
      tokens: 0,
      token_parts: { input: 0, output: 0, cache_read: 0, cache_write: 0 },
      unknown_cost: 0,
      unknown_tokens: 0,
      plan: 0,
    };
    for (const cost of row.costs) {
      const entry = sum.costs.find((entry) => entry.currency === cost.currency);
      if (!entry) sum.costs.push({ ...cost });
      else {
        entry.micros += cost.micros;
        entry.estimated ||= cost.estimated;
        if (cost.plan_estimate_micros !== undefined)
          entry.plan_estimate_micros =
            (entry.plan_estimate_micros ?? 0) + cost.plan_estimate_micros;
      }
    }
    sum.tokens! += row.tokens!;
    for (const part of ['input', 'output', 'cache_read', 'cache_write'] as const)
      sum.token_parts![part] += row.token_parts![part];
    sum.unknown_cost += row.unknown_cost;
    sum.unknown_tokens += row.unknown_tokens;
    sum.plan += row.plan;
    grouped.set(row.key, sum);
  }
  return [...grouped.values()];
}

function timeline(range: ChartRange, group: ChartGroup, scope: TimelineScope = {}): MoneyTimeline {
  const slots = { today: 12, '7d': 7, '30d': 30 }[range];
  const interval = range === 'today' ? 3600000 : 86400000;
  const end = Date.parse(
    range === 'today' ? '2026-11-20T12:00:00+11:00' : '2026-11-21T00:00:00+11:00',
  );
  function usage(i: number, grouping: ChartGroup): TimelineSeries[] {
    return models.flatMap((model, j): TimelineSeries[] => {
      const client = j % 2;
      const rare = model === 'Rare Pro';
      const provider = j < 4 || rare ? 0 : 1;
      const keys = {
        model: `model:${model}`,
        client: `client-${client}`,
        provider: `provider-${provider}`,
        total: 'total',
      };
      // Filter source usage before regrouping so the amounts change with every scope.
      if (scopeGroups.some((level) => scope[level] !== undefined && scope[level] !== keys[level]))
        return [];
      // One expensive use must outrank the frequent inexpensive models on Cost.
      if (rare && i !== slots - 1) return [];
      const labels = {
        model,
        client: clients[client]!,
        provider: providers[provider]!,
        total: 'Total',
      };
      const variation = 5 + ((i * 13 + j * 3) % 9) + (i === 20 ? 9 : 0);
      const micros = rare
        ? 39000000
        : variation * [120000, 80000, 55000, 45000, 25000, 20000, 1000, 100, 20][j]!;
      return [
        {
          key: keys[grouping],
          label: labels[grouping],
          costs: [
            {
              currency: 'USD',
              micros,
              estimated: j === 0,
              ...(j === 0 ? { plan_estimate_micros: micros } : {}),
            },
            ...(rare ? [] : [{ currency: 'AUD', micros: micros * 2, estimated: false }]),
          ],
          tokens: rare ? 10 : micros / 2,
          token_parts: rare
            ? { input: 4, output: 3, cache_read: 2, cache_write: 1 }
            : {
                input: micros / 5,
                output: micros / 10,
                cache_read: micros / 10,
                cache_write: micros / 10,
              },
          unknown_cost: j === 4 && i % 7 === 0 ? 1 : 0,
          unknown_tokens: 0,
          plan: j === 0 ? 4 : 0,
        },
      ];
    });
  }
  const buckets = Array.from({ length: slots }, (_, i) => {
    const start = new Date(end - (slots - i) * interval).toISOString();
    return {
      start,
      end: new Date(end - (slots - i - 1) * interval).toISOString(),
      label: start,
      series: aggregate(usage(i, group)),
    };
  });
  return {
    timezone: 'Australia/Sydney',
    range,
    start: buckets[0]!.start,
    end: new Date(end).toISOString(),
    currencies: [
      ...new Set(
        buckets.flatMap(({ series }) =>
          series.flatMap(({ costs }) => costs.map(({ currency }) => currency)),
        ),
      ),
    ],
    buckets,
    // Use a separate synthetic slice of prior activity, subject to the same filters.
    previous: aggregate(usage(1, 'total'))[0],
  };
}

test.beforeEach(async ({ page }) => {
  let settings: ProxyMoneySettings = { timezone: 'Australia/Sydney', rates: [] };
  await page.route('**/*', async (route) => {
    const url = new URL(route.request().url());
    if (url.origin !== new URL(home).origin) return route.abort();
    switch (url.pathname) {
      case '/_proxy/assets/proxy.js':
        return route.fulfill({ contentType: 'text/javascript', body: javascript });
      case '/_proxy/assets/proxy.css':
        return route.fulfill({ contentType: 'text/css; charset=utf-8', body: stylesheet });
      case '/_proxy/api/auth/status':
        return route.fulfill({ json: { auth: 'passkey', origin: 'http://proxy.test' } });
      case '/_proxy/api/state':
        return route.fulfill({ json: snapshot });
      case timelinePath: {
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
        const scope: TimelineScope = {};
        for (const level of scopeGroups) {
          const key = url.searchParams.get(`scope_${level}`);
          if (key !== null) scope[level] = key;
        }
        return route.fulfill({ json: timeline(range, group, scope) });
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

async function expectTimelineRequest(
  page: Page,
  expected: Record<string, string>,
  action: () => Promise<unknown>,
) {
  // Capture the first request after the action even if its query is wrong.
  const [request] = await Promise.all([
    page.waitForRequest((request) => new URL(request.url()).pathname === timelinePath),
    action(),
  ]);
  const actual = [...new URL(request.url()).searchParams].sort();
  expect(actual).toEqual(Object.entries({ timezone, ...expected }).sort());
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

test('Client row scopes the model timeline; regrouping retains filters and reselecting clears only that level', async ({
  page,
  isMobile,
}) => {
  const query = { range: '7d', group: 'client', metric: 'tokens' };
  const clientScope = { scope_client: 'client-0' };
  const modelScope = { scope_model: 'model:Gemini 4' };
  await page.goto(`${home}?${new URLSearchParams(query)}`);
  const chart = page.locator('.timeline');
  const rail = chart.locator('.timeline-breakdown');
  const total = chart.locator('.timeline-total');
  const scope = chart.getByRole('navigation', { name: 'Usage scope' });
  const grouping = chart.getByRole('combobox', { name: 'Grouping' });
  const client = rail.getByRole('button', { name: /^Studio desktop:/ });
  const model = rail.getByRole('button', { name: /^Gemini 4:/ });
  await expect(rail.getByRole('heading')).toContainText('By client');
  await expectTimelineRequest(page, { range: '7d', group: 'model', ...clientScope }, () =>
    isMobile ? client.tap() : client.click(),
  );
  await expect(scope).toBeVisible();
  await expect(scope.getByRole('button', { name: 'All clients', exact: true })).toBeVisible();
  await expect(scope.getByText('Studio desktop', { exact: true })).toBeVisible();
  await expect(scope.getByRole('button', { name: 'Clear client filter' })).toBeVisible();
  await expectQuery(page, { ...query, group: 'model' });
  await expect(grouping).toHaveValue('model');
  await expect(rail.getByRole('heading')).toContainText('By model');
  await expect(chart.locator('[data-bucket]')).toHaveCount(7);
  await expect(chart.getByRole('button', { name: 'Tokens', exact: true })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(total).toHaveText('6,375,620 tokens');
  await expect(rail.getByRole('button', { name: /^GPT-6\.2:/ })).toHaveCount(0);

  await expectTimelineRequest(
    page,
    { range: '7d', group: 'model', ...clientScope, ...modelScope },
    () => model.press('Enter'),
  );
  await expect(scope.getByRole('button', { name: 'Studio desktop', exact: true })).toBeVisible();
  await expect(scope.getByText('Gemini 4', { exact: true })).toBeVisible();
  await expect(scope.getByRole('button', { name: 'Clear model filter' })).toBeVisible();
  await expect(total).toHaveText('1,705,000 tokens');
  await expect(rail.getByRole('button')).toHaveCount(1);
  await expect(model).toBeVisible();

  await expectTimelineRequest(page, { range: '7d', group: 'model', ...clientScope }, () =>
    model.click(),
  );
  await expect(scope.getByRole('button', { name: 'Clear client filter' })).toBeVisible();
  await expect(scope.getByRole('button', { name: 'Clear model filter' })).toHaveCount(0);
  await expect(total).toHaveText('6,375,620 tokens');
  await expect(model).toBeFocused();
  await expectTimelineRequest(
    page,
    { range: '7d', group: 'model', ...clientScope, ...modelScope },
    () => page.keyboard.press('Enter'),
  );
  await expect(total).toHaveText('1,705,000 tokens');

  await expectTimelineRequest(
    page,
    { range: '7d', group: 'client', ...clientScope, ...modelScope },
    () => grouping.selectOption('client'),
  );
  await expect(scope.getByRole('button', { name: 'All clients', exact: true })).toBeVisible();
  await expect(scope.getByText('Gemini 4', { exact: true })).toBeVisible();
  await expect(rail.getByRole('heading')).toContainText('By client');
  await expect(client).toHaveAttribute('aria-pressed', 'true');
  await expectTimelineRequest(page, { range: '7d', group: 'client', ...modelScope }, () =>
    client.click(),
  );
  await expect(client).not.toHaveAttribute('aria-pressed', 'true');
  await expect(scope.getByRole('button', { name: 'All clients', exact: true })).toHaveCount(0);
  await expect(scope.getByRole('button', { name: 'All models', exact: true })).toBeVisible();
  await expect(total).toHaveText('1,705,000 tokens');

  await expectTimelineRequest(page, { range: '7d', group: 'model' }, () =>
    scope.getByRole('button', { name: 'Clear model filter' }).click(),
  );
  await expect(scope).toBeHidden();
  await expect(grouping).toHaveValue('model');
  await expect(rail.getByRole('button', { name: /^GPT-6\.2:/ })).toBeVisible();
});

test('Provider row opens its model breakdown and the breadcrumb clears the provider filter', async ({
  page,
}) => {
  const query = { range: 'today', group: 'provider', metric: 'tokens' };
  await page.goto(`${home}?${new URLSearchParams(query)}`);
  const chart = page.locator('.timeline');
  const rail = chart.locator('.timeline-breakdown');
  const scope = chart.getByRole('navigation', { name: 'Usage scope' });
  const grouping = chart.getByRole('combobox', { name: 'Grouping' });
  const provider = rail.getByRole('button', { name: /^Example provider:/ });
  await expect(rail.getByRole('heading')).toContainText('By provider');
  await expectTimelineRequest(
    page,
    { range: 'today', group: 'model', scope_provider: 'provider-0' },
    () => provider.click(),
  );
  await expect(scope).toBeVisible();
  await expect(scope.getByRole('button', { name: 'All providers', exact: true })).toBeVisible();
  await expect(scope.getByText('Example provider', { exact: true })).toBeVisible();
  await expectQuery(page, { ...query, group: 'model' });
  await expect(grouping).toHaveValue('model');
  await expect(rail.getByRole('heading')).toContainText('By model');
  await expect(rail.getByRole('button')).toHaveCount(5);
  await expect(chart.locator('[data-bucket]')).toHaveCount(12);
  await expect(chart.locator('.timeline-total')).toHaveText('16,200,010 tokens');

  await expectTimelineRequest(page, { range: 'today', group: 'provider' }, () =>
    scope.getByRole('button', { name: 'Clear provider filter' }).click(),
  );
  await expect(scope).toBeHidden();
  await expect(grouping).toHaveValue('provider');
  await expect(rail.getByRole('button', { name: /^Another provider:/ })).toBeVisible();
});

test('Total opens unfiltered models and an overflow model can be isolated and toggled off by keyboard', async ({
  page,
}) => {
  await page.goto(`${home}?group=total`);
  const chart = page.locator('.timeline');
  const rail = chart.locator('.timeline-breakdown');
  const scope = chart.getByRole('navigation', { name: 'Usage scope' });
  const total = chart.locator('.timeline-total');
  await expect(rail.getByRole('button')).toHaveCount(1);
  await expectTimelineRequest(page, { range: '30d', group: 'model' }, () =>
    rail.getByRole('button', { name: /^Total:/ }).click(),
  );
  await expectQuery(page, defaultQuery);
  await expect(rail.getByRole('heading')).toContainText('By model');
  await expect(scope).toBeHidden();
  await expect(total).toContainText('US$135.57');

  await rail.locator('summary').click();
  // Check both the lead rows and the expanded overflow have actual row buttons.
  await expect(rail.locator('li')).toHaveCount(10);
  await expect(rail.locator('li').getByRole('button')).toHaveCount(10);
  const local = rail.getByRole('button', { name: /^Local:/ });
  await expect(rail.locator('details').getByRole('button', { name: /^Local:/ })).toBeVisible();
  await expect(local).toHaveAttribute('data-series-key', 'model:Local');
  await local.focus();
  await expect(local).toBeFocused();
  await expectTimelineRequest(
    page,
    { range: '30d', group: 'model', scope_model: 'model:Local' },
    () => page.keyboard.press('Enter'),
  );
  await expect(scope).toBeVisible();
  await expect(scope.getByRole('button', { name: 'All models', exact: true })).toBeVisible();
  await expect(scope.getByText('Local', { exact: true })).toBeVisible();
  await expect(scope.getByRole('button', { name: 'Clear model filter' })).toBeVisible();
  await expect(rail.getByRole('button')).toHaveCount(1);
  await expect(total).toHaveText('US$0.00558');

  const selected = rail.locator('[data-series-key="model:Local"]');
  await expect(selected).toHaveAttribute('aria-pressed', 'true');
  await selected.focus();
  await expect(selected).toBeFocused();
  await expectTimelineRequest(page, { range: '30d', group: 'model' }, () =>
    page.keyboard.press('Space'),
  );
  await expect(scope).toBeHidden();
  await expect(total).toContainText('US$135.57');
  await expect(selected).toBeFocused();
  await expect(selected).not.toHaveAttribute('aria-pressed', 'true');
});
