import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test } from '@playwright/test';
import type { ProxyState } from '../src/api/proxy-client';

// Exercise the built standalone bundle without a server, credentials, or upstreams.
const dist = resolve(import.meta.dirname, '../../internal/serveui/static/dist');
function asset(name: string): Buffer {
  try {
    return readFileSync(resolve(dist, name));
  } catch {
    return gunzipSync(readFileSync(resolve(dist, `${name}.gz`)));
  }
}
const javascript = asset('proxy.js');
const stylesheet = asset('proxy.css');
const snapshot: ProxyState = {
  auth: 'passkey',
  mode: 'ui',
  revision: 1,
  routes: [],
  secrets: [],
  callers: [],
  csrf: 'synthetic-layout-csrf',
};

test.beforeEach(async ({ page }) => {
  await page.route('http://proxy.test/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/_proxy/assets/proxy.js')
      return route.fulfill({ contentType: 'text/javascript', body: javascript });
    if (path === '/_proxy/assets/proxy.css')
      return route.fulfill({ contentType: 'text/css', body: stylesheet });
    if (path === '/_proxy/api/auth/status')
      return route.fulfill({ json: { auth: 'passkey', origin: 'http://proxy.test' } });
    if (path === '/_proxy/api/state') return route.fulfill({ json: snapshot });
    if (path === '/_proxy/')
      return route.fulfill({
        contentType: 'text/html',
        body: '<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/_proxy/assets/proxy.css"></head><body><div id="proxy-app"></div><script type="module" src="/_proxy/assets/proxy.js"></script></body></html>',
      });
    return route.abort();
  });
});

for (const scheme of ['light', 'dark'] as const) {
  test(`proxy ${scheme} layout aligns setup actions and follows the Hub theme`, async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto('http://proxy.test/_proxy/');
    await expect(page.getByRole('heading', { name: 'term-llm Proxy' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Go to secrets' })).toBeVisible();
    await expect(page.locator('body')).toHaveCSS(
      'background-color',
      scheme === 'dark' ? 'rgb(33, 33, 33)' : 'rgb(255, 255, 255)',
    );
    const buttons = await page.locator('.onboarding li button').evaluateAll((items) =>
      items.map((item) => {
        const rect = item.getBoundingClientRect();
        return { x: rect.x, width: rect.width };
      }),
    );
    expect(buttons).toHaveLength(4);
    for (const button of buttons) {
      expect(button.x).toBeCloseTo(buttons[0].x, 0);
      if (page.viewportSize()!.width > 640) expect(button.width).toBeCloseTo(buttons[0].width, 0);
    }
    await page.screenshot({ path: testInfo.outputPath(`proxy-${scheme}.png`), fullPage: true });
    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    await expect(page.getByRole('textbox', { name: 'Name', exact: true })).toBeVisible();
    const name = page.getByRole('textbox', { name: 'Name', exact: true });
    await expect(name).toHaveAttribute('data-1p-ignore', 'true');
    await expect(name).toHaveAttribute('autocomplete', 'off');
    await expect(name).toBeFocused();
    await page.getByRole('textbox', { name: 'Upstream base URL', exact: false }).focus();
    await expect(name).not.toBeFocused();
    for (const control of await page.locator('.route-form input, .route-form select').all()) {
      if (!(await control.isVisible())) continue;
      await expect(control).toHaveCSS('border-top-style', 'solid');
      await expect(control).toHaveCSS('border-top-width', '1px');
      await expect(control).not.toHaveCSS('border-top-color', 'rgba(0, 0, 0, 0)');
    }
    await page.screenshot({
      path: testInfo.outputPath(`proxy-form-${scheme}.png`),
      fullPage: true,
    });

    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  });
}

test('proxy mobile layout keeps navigation and checklist usable at narrow widths', async ({
  page,
}, testInfo) => {
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    await page.goto('http://proxy.test/_proxy/');
    await expect(page.getByRole('button', { name: 'Go to clients' })).toBeVisible();
    const lastTab = await page.getByRole('button', { name: 'Passkeys', exact: true }).boundingBox();
    expect(lastTab!.x + lastTab!.width).toBeLessThanOrEqual(width);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    const row = page.locator('.onboarding li').first();
    const text = await row.locator('span').boundingBox();
    const action = await row.locator('button').boundingBox();
    expect(action!.y).toBeGreaterThan(text!.y);
    expect(action!.x).toBeCloseTo(text!.x, 0);
    await page.screenshot({
      path: testInfo.outputPath(`proxy-mobile-${width}.png`),
      fullPage: true,
    });
    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    await expect(page.getByRole('textbox', { name: 'Name', exact: true })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  }
});

test('configuration file separates download from replacement and keeps paste secondary', async ({
  page,
}, testInfo) => {
  const yaml = 'routes: []\n';
  await page.route('http://proxy.test/_proxy/api/export', (route) =>
    route.fulfill({ contentType: 'text/yaml', body: yaml }),
  );
  await page.route('http://proxy.test/_proxy/api/import', (route) =>
    route.fulfill({ json: { ok: true } }),
  );
  await page.goto('http://proxy.test/_proxy/');
  const configuration = page
    .locator('details')
    .filter({ has: page.getByText('Configuration file', { exact: true }) });
  await expect(configuration).not.toHaveAttribute('open');
  await page.getByText('Configuration file', { exact: true }).click();
  await expect(
    page.getByRole('heading', { name: 'Download configuration', exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('heading', { name: 'Replace route configuration', exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/This is not a full backup/)).toBeVisible();
  await expect(page.getByLabel('YAML file', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Route configuration YAML')).not.toBeVisible();
  const downloaded = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download routes.yaml' }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe('routes.yaml');
  expect(readFileSync((await download.path())!, 'utf8')).toBe(yaml);
  await page.getByText('Paste YAML instead', { exact: true }).click();
  await expect(page.getByLabel('Route configuration YAML')).toBeVisible();
  await expect(page.getByLabel('YAML file', { exact: true })).not.toBeVisible();
  await page.getByText('Paste YAML instead', { exact: true }).click();
  await page.getByLabel('YAML file', { exact: true }).setInputFiles({
    name: 'routes.yaml',
    mimeType: 'text/yaml',
    buffer: Buffer.from(yaml),
  });
  await page.screenshot({ path: testInfo.outputPath('configuration-file.png'), fullPage: true });
  page.once('dialog', async (dialog) => {
    expect(dialog.message()).toContain('lose client grants');
    await dialog.accept();
  });
  const replacement = page.waitForRequest('http://proxy.test/_proxy/api/import');
  await page.getByRole('button', { name: 'Replace routes from YAML' }).click();
  expect((await replacement).postDataJSON()).toEqual({ revision: 1, yaml });
  await expect(page.getByRole('status')).toContainText('Route configuration replaced');
});

for (const [id, upstream, header, prefix] of [
  ['openai', 'https://api.openai.com/v1', 'Authorization', 'Bearer '],
  ['anthropic', 'https://api.anthropic.com', 'x-api-key', ''],
]) {
  test(`${id} preset fills an editable route and saves with native client authentication`, async ({
    page,
  }, testInfo) => {
    await page.route('http://proxy.test/_proxy/api/state', (route) =>
      route.fulfill({
        json: {
          ...snapshot,
          presets: [{ id, label: id, upstream, auth: { header, prefix } }],
          secrets: [{ name: 'provider-key', origin: new URL(upstream).origin, source: 'stored' }],
        },
      }),
    );
    await page.route('http://proxy.test/_proxy/api/config', (route) =>
      route.fulfill({ json: { revision: 2 } }),
    );
    await page.goto('http://proxy.test/_proxy/');
    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    await page.getByLabel('Provider', { exact: true }).selectOption(id);
    await expect(page.getByLabel('Name', { exact: true })).toHaveValue(id);
    await expect(page.getByLabel('Upstream base URL', { exact: true })).toHaveValue(upstream);
    await expect(
      page.getByLabel('Local client authentication header', { exact: true }),
    ).toHaveValue(header);
    await page.getByLabel('Secret name', { exact: true }).selectOption('provider-key');
    await page.getByLabel('Name', { exact: true }).fill(`${id}-mine`);
    await page.screenshot({ path: testInfo.outputPath(`preset-${id}.png`), fullPage: true });
    const saved = page.waitForRequest('http://proxy.test/_proxy/api/config');
    await page.getByRole('button', { name: 'Save route', exact: true }).click();
    expect((await saved).postDataJSON()).toEqual({
      revision: 1,
      routes: [
        expect.objectContaining({
          name: `${id}-mine`,
          upstream,
          local_prefix: `/${id}-mine/`,
          caller_auth: { header, prefix },
          set_headers: { [header]: { secret: 'provider-key', prefix } },
        }),
      ],
    });
    await expect(page.getByRole('status')).toContainText('No upstream request was made');
  });
}

test('adds a missing API key without leaving or resetting the route', async ({
  page,
}, testInfo) => {
  let stored = false;
  let submittedKey: unknown;
  const preset = {
    id: 'venice',
    label: 'Venice',
    upstream: 'https://api.venice.ai/api/v1',
    auth: { header: 'Authorization', prefix: 'Bearer ' },
  };
  await page.route('http://proxy.test/_proxy/api/state', (route) =>
    route.fulfill({
      json: {
        ...snapshot,
        presets: [preset],
        revision: stored ? 2 : 1,
        secrets: stored
          ? [{ name: 'my-venice-key', origin: 'https://api.venice.ai', source: 'stored' }]
          : [],
      },
    }),
  );
  await page.route('http://proxy.test/_proxy/api/secrets', (route) => {
    submittedKey = route.request().postDataJSON();
    stored = true;
    return route.fulfill({ json: { revision: 2 } });
  });
  await page.route('http://proxy.test/_proxy/api/config', (route) =>
    route.fulfill({ json: { revision: 3 } }),
  );
  await page.goto('http://proxy.test/_proxy/');
  await page.getByRole('button', { name: 'Add route', exact: true }).click();
  await page.getByLabel('Provider', { exact: true }).selectOption('venice');
  await page.getByLabel('Name', { exact: true }).fill('my-venice');
  await page.getByLabel('Local prefix', { exact: true }).fill('/my-custom-path/');
  await page.getByRole('button', { name: 'Add API key', exact: true }).click();
  let dialog = page.getByRole('dialog', { name: 'Add API key' });
  await expect(dialog.getByLabel('Bound origin', { exact: true })).toHaveValue(
    'https://api.venice.ai',
  );
  await expect(dialog.getByLabel('Bound origin', { exact: true })).toHaveAttribute('readonly', '');
  await expect(dialog.getByLabel('New secret value', { exact: true })).toBeFocused();
  await expect(page.locator('.route-form form')).toHaveCount(0);
  await page.keyboard.press('Escape');
  await expect(page.locator('dialog')).toHaveCount(0);
  expect(stored).toBe(false);
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('my-venice');
  await page.getByRole('button', { name: 'Add API key', exact: true }).click();
  dialog = page.getByRole('dialog', { name: 'Add API key' });
  await page.screenshot({ path: testInfo.outputPath('add-api-key-dialog.png'), fullPage: true });
  await dialog
    .getByLabel('New secret value', { exact: true })
    .fill('synthetic-private-provider-key');
  await dialog.getByRole('button', { name: 'Store secret', exact: true }).click();
  await expect(page.locator('dialog')).toHaveCount(0);
  expect(submittedKey).toEqual({
    name: 'my-venice-key',
    origin: 'https://api.venice.ai',
    value: 'synthetic-private-provider-key',
    replace: false,
  });
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('my-venice');
  await expect(page.getByLabel('Local prefix', { exact: true })).toHaveValue('/my-custom-path/');
  await expect(page.getByLabel('Provider', { exact: true })).toHaveValue('venice');
  await expect(page.getByLabel('Secret name', { exact: true })).toHaveValue('my-venice-key');
  await expect(page.getByRole('status')).toContainText('Your route is not saved yet');
  const saved = page.waitForRequest('http://proxy.test/_proxy/api/config');
  await page.getByRole('button', { name: 'Save route', exact: true }).click();
  expect((await saved).postDataJSON()).toEqual({
    revision: 2,
    routes: [expect.objectContaining({ name: 'my-venice', local_prefix: '/my-custom-path/' })],
  });
});

test('Clients labels keep legacy bookmarks and API state compatible', async ({ page }) => {
  await page.goto('http://proxy.test/_proxy/?tab=callers');
  await expect(page.getByRole('button', { name: 'Clients', exact: true })).toHaveAttribute(
    'aria-current',
    'page',
  );
  await expect(page.getByRole('heading', { name: 'Clients', exact: true })).toBeVisible();
  await expect(page.getByLabel('Client name', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Create client', exact: true })).toBeVisible();
  await expect(page).toHaveURL('http://proxy.test/_proxy/?tab=clients');
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Clients', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Callers', exact: true })).toHaveCount(0);
});

for (const width of [1280, 390, 320]) {
  test(`request history is compact with numbered pagination at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route('http://proxy.test/_proxy/api/requests?*', (route) => {
      const number = Number(new URL(route.request().url()).searchParams.get('page'));
      return route.fulfill({
        json: {
          total: 2225,
          page: number,
          page_size: 25,
          next: number < 89 ? 2225 - number * 25 : 0,
          requests: Array.from({ length: 25 }, (_, i) => ({
            id: `request-${number}-${i}`,
            seq: 2225 - (number - 1) * 25 - i,
            started: 1794792001000,
            ended: 1794792001230,
            caller_id: 'term',
            caller_name: 'term',
            route: 'venice',
            revision: 17,
            method: 'POST',
            local_prefix: '/venice/',
            path: '/chat/completions',
            peer: '127.0.0.1',
            status: 400,
            outcome: 'http_error',
            sent: 9,
            received: 32,
            total_ms: 230,
            header_ms: 200,
            first_byte_ms: 210,
            upstream_id: '',
          })),
        },
      });
    });
    await page.goto('http://proxy.test/_proxy/?tab=requests');
    const topPager = page.getByRole('navigation', { name: 'Request pages', exact: true });
    await expect(topPager).toContainText('Page 1 of 89');
    await expect(page.locator('.request-row')).toHaveCount(25);
    await expect(page.locator('header')).not.toContainText(/managed|Revision/);
    await expect(page.locator('.request-filters')).not.toHaveAttribute('open');
    expect((await topPager.boundingBox())!.y).toBeLessThan(350);
    if (width === 1280 && !testInfo.project.use.isMobile) {
      expect(
        (await page.locator('.request-row').first().boundingBox())!.height,
      ).toBeLessThanOrEqual(50);
    }
    await topPager.getByRole('button', { name: 'Page 89', exact: true }).click();
    await expect(topPager).toContainText('Page 89 of 89');
    await expect(topPager.getByRole('button', { name: 'Next' })).toBeDisabled();
    await expect(page.locator('.request-row')).toHaveCount(25);
    await topPager.getByRole('button', { name: 'Page 1', exact: true }).click();
    await expect(topPager).toContainText('Page 1 of 89');
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Requests', exact: true })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`requests-${width}.png`), fullPage: true });
    await page.getByText('Filters (0)', { exact: true }).click();
    await expect(page.getByLabel('Path', { exact: true })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath(`requests-filters-${width}.png`),
      fullPage: true,
    });
  });
}
