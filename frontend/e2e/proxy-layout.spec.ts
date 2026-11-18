import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { gunzipSync } from 'node:zlib';
import { expect, test, type Page } from '@playwright/test';
import type { ProxyState } from '../src/api/proxy-client';

// Exercise the built standalone bundle without a server, credentials, or upstreams.
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
const snapshot: ProxyState = {
  auth: 'passkey',
  mode: 'ui',
  revision: 1,
  routes: [],
  secrets: [],
  callers: [],
  admin_keys: [],
  csrf: 'synthetic-layout-csrf',
};

test.beforeEach(async ({ page }) => {
  await page.route('http://proxy.test/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/_proxy/assets/proxy.js')
      return route.fulfill({ contentType: 'text/javascript', body: javascript });
    if (path === '/_proxy/assets/proxy.css')
      return route.fulfill({ contentType: 'text/css; charset=utf-8', body: stylesheet });
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

async function choosePreset(page: Page, name: string) {
  await page.getByRole('button', { name: 'Add route', exact: true }).click();
  const picker = page.locator('.route-picker');
  await picker.getByRole('button', { name, exact: true }).click();
}

for (const scheme of ['light', 'dark'] as const) {
  test(`proxy layout aligns preset actions with ${scheme} OS preference`, async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    await expect(page.getByRole('heading', { name: 'term-llm Proxy' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add route', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Add a route' })).toBeFocused();
    // The standalone proxy deliberately keeps its existing light palette.
    await expect(page.locator('body')).toHaveCSS('background-color', 'rgb(255, 255, 255)');
    const buttons = await page
      .locator('.route-preset-choice, .route-preset-custom')
      .evaluateAll((items) =>
        items.map((item) => {
          const rect = item.getBoundingClientRect();
          return { x: rect.x, width: rect.width };
        }),
      );
    expect(buttons).toHaveLength(1);
    for (const button of buttons) {
      expect(button.x).toBeCloseTo(buttons[0].x, 0);
      if (page.viewportSize()!.width > 640) expect(button.width).toBeCloseTo(buttons[0].width, 0);
    }
    await page.screenshot({ path: testInfo.outputPath(`proxy-${scheme}.png`), fullPage: true });
    await page.getByRole('button', { name: 'Custom endpoint', exact: true }).click();
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

test('proxy mobile layout keeps navigation and the preset picker usable at narrow widths', async ({
  page,
}, testInfo) => {
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Custom endpoint', exact: true })).toBeVisible();
    const lastTab = await page.getByRole('button', { name: 'Passkeys', exact: true }).boundingBox();
    expect(lastTab!.x + lastTab!.width).toBeLessThanOrEqual(width);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    const picker = (await page.locator('.route-picker').boundingBox())!;
    const choice = (await page
      .getByRole('button', { name: 'Custom endpoint', exact: true })
      .boundingBox())!;
    expect(choice.x).toBeGreaterThanOrEqual(picker.x);
    expect(choice.x + choice.width).toBeLessThanOrEqual(picker.x + picker.width);
    await page.screenshot({
      path: testInfo.outputPath(`proxy-mobile-${width}.png`),
      fullPage: true,
    });
    await page.getByRole('button', { name: 'Custom endpoint', exact: true }).click();
    await expect(page.getByRole('textbox', { name: 'Name', exact: true })).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  }
});

test('Admin separates configuration download from replacement without nested disclosures', async ({
  page,
}, testInfo) => {
  const yaml = 'routes: []\n';
  await page.route('http://proxy.test/_proxy/api/export', (route) =>
    route.fulfill({ contentType: 'text/yaml', body: yaml }),
  );
  await page.route('http://proxy.test/_proxy/api/import', (route) =>
    route.fulfill({ json: { ok: true } }),
  );
  await page.goto('http://proxy.test/_proxy/?tab=admin');
  await expect(page.getByRole('heading', { name: 'Admin', exact: true })).toBeVisible();
  await expect(
    page.getByRole('heading', { name: 'Download configuration', exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('heading', { name: 'Replace route configuration', exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/not a full backup/i)).toBeVisible();
  await expect(page.getByLabel('YAML file', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Route configuration YAML')).not.toBeVisible();
  const downloaded = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download routes.yaml' }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe('routes.yaml');
  expect(readFileSync((await download.path())!, 'utf8')).toBe(yaml);
  await page.getByLabel('Import source', { exact: true }).selectOption('paste');
  await expect(page.getByLabel('Route configuration YAML')).toBeVisible();
  await expect(page.getByLabel('YAML file', { exact: true })).not.toBeVisible();
  await page.getByLabel('Import source', { exact: true }).selectOption('file');
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
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    await choosePreset(page, id);
    await expect(page.getByLabel('Upstream base URL', { exact: true })).toBeHidden();
    await page
      .locator('.route-form summary')
      .filter({ hasText: /^Advanced$/ })
      .click();
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

test('OpenCode Go preset is one route with one shared key and three subpath mappings', async ({
  page,
}, testInfo) => {
  const preset = {
    id: 'opencode-go',
    label: 'OpenCode Go',
    // Actual backend preset: the rules own every destination.
    upstream: '',
    auth: { header: '', prefix: '' },
    billing: 'plan',
    usage: 'auto',
    rules: [
      {
        path_prefix: '/',
        upstream: 'https://opencode.ai/zen/go/v1',
        auth: { header: 'Authorization', prefix: 'Bearer ' },
      },
      {
        path_prefix: '/messages/',
        upstream: 'https://opencode.ai/zen/go',
        auth: { header: 'x-api-key', prefix: '' },
      },
      {
        path_prefix: '/gemini/',
        upstream: 'https://opencode.ai/inference/go/v1',
        auth: { header: 'x-goog-api-key', prefix: '' },
      },
    ],
  };
  await page.route('http://proxy.test/_proxy/api/state', (route) =>
    route.fulfill({
      json: {
        ...snapshot,
        presets: [preset],
        secrets: [{ name: 'opencode-go-key', origin: 'https://opencode.ai', source: 'stored' }],
      },
    }),
  );
  await page.route('http://proxy.test/_proxy/api/config', (route) =>
    route.fulfill({ json: { revision: 2 } }),
  );
  await page.goto('http://proxy.test/_proxy/?tab=routes');
  await choosePreset(page, 'OpenCode Go');
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('opencode-go');
  await expect(page.getByLabel('Local prefix', { exact: true })).toHaveValue('/opencode-go/');
  await expect(page.getByLabel('Billing', { exact: true })).toHaveValue('plan');
  const ruleRows = page.locator('.subpath-rule');
  await expect(ruleRows).toHaveCount(3);
  // Technical mappings, protocol auth, and edits stay in Advanced.
  const summary = page.locator('.route-rule-summary li');
  await expect(summary).toHaveCount(3);
  await expect(summary.nth(0)).toContainText('/ → https://opencode.ai/zen/go/v1');
  await expect(summary.nth(1)).toContainText('/messages/ → https://opencode.ai/zen/go');
  await expect(summary.nth(2)).toContainText('/gemini/ → https://opencode.ai/inference/go/v1');
  await expect(
    page.getByText('One key and one client grant cover all three protocol paths.', { exact: true }),
  ).toBeHidden();
  await expect(page.getByText('Edit subpath rules', { exact: true })).toBeHidden();
  // One shared key and no unused parent upstream/auth controls.
  await expect(page.getByLabel('Upstream base URL', { exact: true })).toHaveCount(3);
  await expect(page.getByLabel('Secret name', { exact: true })).toHaveCount(1);
  await expect(page.getByLabel('Local client authentication header', { exact: true })).toHaveCount(
    0,
  );
  await expect(page.locator('.route-form details[open]')).toHaveCount(0);
  await expect(ruleRows.nth(0)).toBeHidden();
  await page.getByLabel('Secret name', { exact: true }).selectOption('opencode-go-key');
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath('preset-opencode-go.png'),
    fullPage: true,
  });
  await page
    .locator('.route-form summary')
    .filter({ hasText: /^Advanced$/ })
    .click();
  await page.getByText('Edit subpath rules', { exact: true }).click();
  await expect(ruleRows.nth(0)).toBeVisible();
  await expect(ruleRows.nth(0).getByLabel('Subpath', { exact: true })).toHaveValue('/');
  await expect(ruleRows.nth(0).getByLabel('Upstream base URL', { exact: true })).toHaveValue(
    'https://opencode.ai/zen/go/v1',
  );
  await expect(ruleRows.nth(1).getByLabel('Subpath', { exact: true })).toHaveValue('/messages/');
  await expect(ruleRows.nth(1).getByLabel('Upstream base URL', { exact: true })).toHaveValue(
    'https://opencode.ai/zen/go',
  );
  await expect(ruleRows.nth(2).getByLabel('Subpath', { exact: true })).toHaveValue('/gemini/');
  await expect(ruleRows.nth(2).getByLabel('Upstream base URL', { exact: true })).toHaveValue(
    'https://opencode.ai/inference/go/v1',
  );
  // Protocol headers and the regrant warning live in Advanced.
  const protocol = page.locator('.route-protocol-details li');
  await expect(protocol).toHaveCount(3);
  await expect(protocol.nth(0)).toContainText('/ · client Authorization: Bearer');
  await expect(protocol.nth(1)).toContainText('/messages/ · client x-api-key');
  await expect(protocol.nth(2)).toContainText('/gemini/ · client x-goog-api-key');
  await expect(page.locator('.route-form')).toContainText('Changing a subpath mapping');
  await page.screenshot({
    path: testInfo.outputPath('preset-opencode-go-rules.png'),
    fullPage: true,
  });
  const saved = page.waitForRequest('http://proxy.test/_proxy/api/config');
  await page.getByRole('button', { name: 'Save route', exact: true }).click();
  const payload = (await saved).postDataJSON() as {
    revision: number;
    routes: Record<string, unknown>[];
  };
  expect(payload.revision).toBe(1);
  expect(payload.routes).toHaveLength(1);
  expect(payload.routes[0]).toMatchObject({
    name: 'opencode-go',
    local_prefix: '/opencode-go/',
    upstream: '',
    caller_auth: { header: '', prefix: '' },
    set_headers: {},
    billing: 'plan',
    usage: 'auto',
    rules: [
      {
        path_prefix: '/',
        upstream: 'https://opencode.ai/zen/go/v1',
        caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
        set_headers: { Authorization: { secret: 'opencode-go-key', prefix: 'Bearer ' } },
      },
      {
        path_prefix: '/messages/',
        upstream: 'https://opencode.ai/zen/go',
        caller_auth: { header: 'x-api-key', prefix: '' },
        set_headers: { 'x-api-key': { secret: 'opencode-go-key', prefix: '' } },
      },
      {
        path_prefix: '/gemini/',
        upstream: 'https://opencode.ai/inference/go/v1',
        caller_auth: { header: 'x-goog-api-key', prefix: '' },
        set_headers: { 'x-goog-api-key': { secret: 'opencode-go-key', prefix: '' } },
      },
    ],
  });
  await expect(page.getByRole('status')).toContainText('No upstream request was made');
});

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
  await page.goto('http://proxy.test/_proxy/?tab=routes');
  await choosePreset(page, 'Venice');
  await page
    .locator('.route-form summary')
    .filter({ hasText: /^Advanced$/ })
    .click();
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
  await expect(page.getByRole('heading', { name: 'Set up Venice' })).toBeVisible();
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

for (const width of [1440, 320]) {
  test(`request Filters matches Refresh and toggles natively at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route('http://proxy.test/_proxy/api/requests?*', (route) =>
      route.fulfill({ json: { total: 0, page: 1, page_size: 25, requests: [] } }),
    );
    await page.goto('http://proxy.test/_proxy/?tab=requests');
    const disclosure = page.locator('.request-filters');
    const summary = disclosure.locator('summary');
    const refresh = page.getByRole('button', { name: 'Refresh', exact: true });
    await expect(page.getByRole('button', { name: 'Refresh state', exact: true })).toHaveCount(0);
    await expect(summary).toHaveText('Filters');
    await expect(refresh).toBeEnabled();
    const controlStyle = (element: Element) => {
      const style = getComputedStyle(element);
      return [
        style.fontFamily,
        style.fontSize,
        style.fontWeight,
        style.lineHeight,
        style.letterSpacing,
        style.color,
        style.backgroundColor,
        style.borderRadius,
      ];
    };
    expect(await summary.evaluate(controlStyle)).toEqual(await refresh.evaluate(controlStyle));
    const bounds = (await summary.boundingBox())!;
    const refreshBounds = (await refresh.boundingBox())!;
    expect(bounds.y).toBe(refreshBounds.y);
    expect(bounds.height).toBe(refreshBounds.height);
    expect(bounds.x + bounds.width).toBeLessThan(refreshBounds.x);
    if (width === 320) {
      expect(bounds.height).toBeGreaterThanOrEqual(44);
      expect(bounds.width).toBeGreaterThanOrEqual(44);
    }
    const alignment = await summary.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      const label = range.getBoundingClientRect();
      const trigger = element.getBoundingClientRect();
      const marker = getComputedStyle(element, '::after');
      const gap = parseFloat(getComputedStyle(element).columnGap);
      return {
        x:
          label.left +
          (label.width + gap + parseFloat(marker.width)) / 2 -
          trigger.x -
          trigger.width / 2,
        y: label.top + label.height / 2 - trigger.y - trigger.height / 2,
        marker: marker.content,
      };
    });
    expect(alignment.marker).toContain('›');
    expect(Math.abs(alignment.x)).toBeLessThan(1);
    expect(Math.abs(alignment.y)).toBeLessThan(1);

    const path = page.getByLabel('Path', { exact: true });
    await expect(path).toBeHidden();
    await refresh.focus();
    await page.keyboard.press('Shift+Tab');
    await expect(summary).toBeFocused();
    await expect(summary).toHaveCSS('outline-style', 'solid');
    await page.keyboard.press('Enter');
    await expect(disclosure).toHaveAttribute('open');
    await expect(path).toBeVisible();
    const clearFilters = page.getByRole('button', { name: 'Clear', exact: true });
    if (width === 320) {
      for (const control of [page.getByRole('button', { name: 'Apply filters' }), clearFilters]) {
        const bounds = (await control.boundingBox())!;
        expect(bounds.height).toBeGreaterThanOrEqual(44);
        expect(bounds.x).toBeGreaterThanOrEqual(0);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
      }
    }
    const rotation = () =>
      summary.evaluate((element) => getComputedStyle(element, '::after').rotate);
    expect(await rotation()).toBe('90deg');
    await path.fill('/v1/messages');
    await expect(summary).toHaveText('Filters');
    const apply = page.waitForRequest(/\/_proxy\/api\/requests\?/);
    await path.press('Enter');
    expect(new URL((await apply).url()).searchParams.get('path')).toBe('/v1/messages');
    await expect(summary).toHaveText('Filters (1)');
    await path.fill('/draft');
    await summary.focus();
    await page.keyboard.press('Space');
    await expect(disclosure).not.toHaveAttribute('open');
    await expect(summary).toBeFocused();
    await expect(summary).toHaveText('Filters (1)');
    expect(await rotation()).toBe('none');

    const reload = page.waitForRequest(/\/_proxy\/api\/requests\?/);
    await refresh.click();
    expect(new URL((await reload).url()).searchParams.get('path')).toBe('/v1/messages');
    await expect(disclosure).not.toHaveAttribute('open');
    await summary.press('Enter');
    await expect(path).toHaveValue('/draft');
    const clear = page.waitForRequest(/\/_proxy\/api\/requests\?/);
    await clearFilters.click();
    const cleared = new URL((await clear).url()).searchParams;
    expect(cleared.has('path')).toBe(false);
    expect(cleared.get('page')).toBe('1');
    await expect(summary).toHaveText('Filters');
    await expect(path).toHaveValue('');
  });
}

for (const width of [1440, 1024, 768, 390, 320]) {
  test(`request ledger aligns metrics and expands grouped failures at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const started = Date.now();
    await page.route('http://proxy.test/_proxy/api/debug/captures/request-*', (route) =>
      route.fulfill({ status: 404, json: { error: 'Capture not found' } }),
    );
    await page.route('http://proxy.test/_proxy/api/requests?*', (route) => {
      const number = Number(new URL(route.request().url()).searchParams.get('page'));
      return route.fulfill({
        json: {
          total: 2225,
          page: number,
          page_size: 25,
          next: number < 89 ? 2225 - number * 25 : 0,
          requests: Array.from({ length: 25 }, (_, i) => {
            const failed = i >= 3 && i <= 5;
            const openai = i === 2;
            const usage = failed
              ? undefined
              : {
                  protocol: openai ? 'responses' : 'messages',
                  usage: {
                    source: 'reported',
                    model: openai ? 'gpt-5.5' : 'claude-haiku-5-5',
                    input: openai ? 1000 : i === 0 ? 2 : i === 1 ? 14 : 0,
                    output: i === 0 ? 19 : i === 1 ? 846 : 0,
                    cached_input: openai ? 750 : i === 1 ? 36098 : 0,
                    cache_write: openai ? -1 : i === 0 ? 36098 : 0,
                    reasoning: -1,
                    total: -1,
                    turns: 1,
                    tier: 'standard',
                    ambiguous: false,
                  },
                };
            return {
              id: `request-${number}-${i}`,
              seq: 2225 - (number - 1) * 25 - i,
              started: started - (i === 24 ? 86400000 : i * 1000),
              ended: started,
              caller_id: 'arch',
              caller_name: 'arch',
              route: failed ? 'venice' : openai ? 'openai' : 'claude',
              revision: 17,
              method: 'POST',
              local_prefix: failed ? '/venice/' : openai ? '/openai/' : '/claude/',
              path: failed ? '/chat/completions' : '/v1/messages',
              peer: '127.0.0.1',
              status: failed ? 400 : 200,
              outcome: failed ? 'http_error' : openai ? 'incomplete' : 'complete',
              sent: 113112,
              received: 5059,
              total_ms: i === 0 ? 1916 : 230,
              header_ms: 200,
              first_byte_ms: 210,
              upstream_id: '',
              usage,
              money: failed
                ? { billing: 'pay_as_you_go', kind: 'unmeasured' }
                : {
                    billing: 'plan',
                    kind: 'included',
                    api_equivalent: {
                      kind: 'estimated',
                      amount_micros: i === 0 ? 4522 : i === 1 ? 786 : 0,
                      currency: 'USD',
                      source: 'models.dev/anthropic',
                      asof: '2026-11-17',
                    },
                  },
            };
          }),
        },
      });
    });
    await page.goto('http://proxy.test/_proxy/?tab=requests');
    const top = page.getByRole('navigation', { name: 'Request pages', exact: true });
    const pager = page.getByRole('navigation', { name: 'Request pages (bottom)', exact: true });
    await expect(top).toContainText('Page 1 of 89');
    await expect(page.locator('.request-row')).toHaveCount(23);
    await expect(page.locator('.request-filters')).not.toHaveAttribute('open');
    const first = page.locator('.request-row').first();
    await expect(first.locator('.request-input')).toHaveText('36.1k');
    await expect(first.locator('.request-price')).toContainText('0.0045');
    await expect(first.locator('.request-price')).toContainText('API');
    await expect(first.locator('.request-time')).toHaveText(/^[0-2]\d:[0-5]\d:[0-5]\d$/);
    await expect(first.locator('.request-duration')).toHaveText('1.9s');
    await expect(first.locator('.request-open')).toHaveAccessibleDescription('claude-haiku-5-5');
    await expect(page.locator('.request-row').nth(2).locator('.request-input')).toHaveText('1k');
    await expect(page.locator('.request-row').nth(2).locator('.request-cache')).toHaveText('75%');
    await expect(page.locator('.request-row').nth(2).locator('.request-status')).toHaveClass(
      /status-warning/,
    );
    await expect(first.locator('.request-status')).not.toHaveClass(/outcome/);
    await expect(page.locator('.request-date-group')).toHaveCount(2);
    const wide = width > 1024;
    if (wide) {
      // Layout follows width; touch devices retain larger hit targets.
      expect((await first.boundingBox())!.height).toBeLessThanOrEqual(
        testInfo.project.use.isMobile ? 52 : 40,
      );
      for (const name of ['input', 'output', 'cache', 'duration', 'price']) {
        const edges = await page
          .locator(`.request-row > .request-${name}`)
          .evaluateAll((nodes) => nodes.map((node) => node.getBoundingClientRect().right));
        expect(Math.max(...edges) - Math.min(...edges)).toBeLessThan(1);
      }
    } else {
      expect((await first.boundingBox())!.height).toBeLessThanOrEqual(76);
      await expect(first.locator('.request-mobile-usage')).toBeVisible();
      await expect(first.locator('.request-input')).not.toBeVisible();
    }
    const fallback = page.locator('.request-row').nth(3);
    await expect(fallback.locator('.request-open')).toHaveText('POST /venice/chat/completions');
    if (wide) {
      await expect(fallback.locator('.request-input')).toHaveText('~113.11K');
      await expect(fallback.locator('.request-output')).toHaveText('~5.06K');
    } else {
      await expect(fallback.locator('.request-mobile-usage')).toHaveText('~113.11K→~5.06K');
    }
    const repeated = page.getByRole('button', { name: 'Expand 3 repeated requests' });
    await repeated.click();
    await expect(page.locator('.request-group-rows .request-row')).toHaveCount(3);
    await page.getByRole('button', { name: 'Collapse 3 repeated requests' }).click();
    await expect(page.locator('.request-group-rows .request-row')).toHaveCount(0);
    await first.locator('.request-open').click();
    await expect(first.locator('.request-detail')).toBeVisible();
    await expect(first.locator('.request-detail')).toContainText('Cache write');
    await expect(first.locator('.request-detail')).toContainText('No raw payload capture');
    await expect(first.getByRole('heading', { name: 'Request details' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(first.locator('.request-detail')).toHaveCount(0);
    await expect(first.locator('.request-open')).toBeFocused();
    await pager.getByRole('button', { name: 'Page 89', exact: true }).click();
    await expect(pager).toContainText('Page 89 of 89');
    await expect(pager.getByRole('button', { name: 'Next' })).toBeDisabled();
    await pager.getByRole('button', { name: 'Page 1', exact: true }).click();
    await expect(top).toContainText('Page 1 of 89');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
    await page.screenshot({ path: testInfo.outputPath(`ledger-${width}.png`), fullPage: true });
    await page.getByText('Filters', { exact: true }).click();
    await expect(page.getByLabel('Path', { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
      true,
    );
  });
}

test('proxy Debug toggles capture and safely browses bounded payloads on mobile', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  let enabled = false;
  let changes = 0;
  const payload = '<script>window.debugInjected=true</script>' + 'x'.repeat(70_000) + 'PAYLOAD END';
  await page.route('http://proxy.test/_proxy/api/debug**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === '/_proxy/api/debug') {
      if (request.method() === 'POST') {
        const body = request.postDataJSON();
        if (body.enabled) expect(body.acknowledge_sensitive).toBe(true);
        enabled = body.enabled;
        changes++;
      }
      return route.fulfill({
        json: {
          enabled,
          directory: '/synthetic/debug-payloads',
          preview_limit_bytes: 65_536,
          remaining_bytes: 256 << 20,
          record_count: 1,
          record_limit: 1000,
        },
      });
    }
    if (url.pathname === '/_proxy/api/debug/captures')
      return route.fulfill({
        json: {
          captures: [
            {
              id: 'px_fixture',
              started: 1,
              method: 'POST',
              route: 'fixture',
              local_path: '/fixture/messages',
              outcome: 'complete',
              finished: true,
              websocket: false,
              status: 200,
              files: ['manifest.json', 'response.body'],
            },
          ],
          limit: 50,
        },
      });
    const file = url.pathname.split('/').at(-1);
    const body = file === 'manifest.json' ? '{"id":"px_fixture"}' : payload;
    const offset = Number(url.searchParams.get('offset') || 0);
    const content = body.slice(offset, offset + 65_536);
    return route.fulfill({
      json: {
        id: 'px_fixture',
        file,
        encoding: 'utf8',
        content,
        offset,
        total_bytes: body.length,
        bytes_returned: content.length,
        truncated: offset > 0 || offset + content.length < body.length,
      },
    });
  });
  await page.goto('http://proxy.test/_proxy/?tab=debug');
  await expect(page.getByRole('heading', { name: 'Debug', exact: true })).toBeVisible();
  const enable = page.getByRole('button', { name: 'Enable capture', exact: true });
  await expect(enable).toBeEnabled();
  page.once('dialog', (dialog) => dialog.dismiss());
  await enable.click();
  await expect(enable).toBeEnabled();
  expect(changes).toBe(0);
  page.once('dialog', (dialog) => dialog.accept());
  await enable.click();
  const disable = page.getByRole('button', { name: 'Disable capture', exact: true });
  await expect(disable).toBeEnabled();
  expect(changes).toBe(1);
  await page.getByLabel('Capture', { exact: true }).selectOption('px_fixture');
  await page.getByLabel('File', { exact: true }).selectOption('response.body');
  const preview = page.getByLabel('Capture preview', { exact: true });
  await expect(preview).toContainText('<script>window.debugInjected=true</script>');
  expect(await page.evaluate(() => 'debugInjected' in window)).toBe(false);
  await page.getByRole('button', { name: 'End', exact: true }).click();
  await expect(preview).toContainText('PAYLOAD END');
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
    ),
  ).toBe(true);
  await disable.click();
  await expect(enable).toBeEnabled();
  expect(changes).toBe(2);
  await page.getByRole('button', { name: 'Routes', exact: true }).click();
  await expect(preview).toHaveCount(0);
});

test('Clients stays scoped to generic tokens without a Claude pairing wizard', async ({ page }) => {
  await page.goto('http://proxy.test/_proxy/?tab=secrets');
  await expect(page.getByRole('heading', { name: 'Secrets', exact: true })).toBeVisible();
  await expect(page.locator('header')).not.toContainText('Connect Claude');
  await expect(page.getByRole('button', { name: 'Connect Claude', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Clients', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Clients', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Create client', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Connect Claude', exact: true })).toHaveCount(0);
  await expect(page.locator('header')).not.toContainText('Connect Claude');
  await page.getByRole('button', { name: 'Routes', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Connect Claude', exact: true })).toHaveCount(0);
});

for (const width of [1440, 390]) {
  test(`Claude route status badges match Venice's compact size at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    let enabled = true;
    await page.route('http://proxy.test/_proxy/api/state', (route) => {
      const defaults = {
        enabled,
        allow_insecure_http: false,
        caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
        set_headers: {},
      };
      const state: ProxyState = {
        ...snapshot,
        accounts: [
          { name: 'native', kind: 'claude-code', state: 'native_login', generation: 1, updated: 1 },
        ],
        routes: [
          {
            ...defaults,
            name: 'native',
            account: 'native',
            local_prefix: '/claude-code/',
            upstream: 'https://api.anthropic.com',
          },
          {
            ...defaults,
            name: 'venice',
            local_prefix: '/venice/',
            upstream: 'https://api.venice.ai/api/v1',
          },
        ],
      };
      return route.fulfill({ json: state });
    });
    const claude = page.locator('[data-focus="route:native"]');
    const venice = page.locator('[data-focus="route:venice"]');
    for (const status of ['Needs client access', 'Disabled']) {
      enabled = status !== 'Disabled';
      await page.goto('http://proxy.test/_proxy/?tab=routes');
      const claudeBadge = claude.getByText(status, { exact: true });
      const veniceBadge = venice.getByText(status, { exact: true });
      await expect(claudeBadge).toBeVisible();
      await expect(veniceBadge).toBeVisible();
      const claudeSize = (await claudeBadge.boundingBox())!;
      const veniceSize = (await veniceBadge.boundingBox())!;
      expect(claudeSize.width).toBeCloseTo(veniceSize.width, 0);
      expect(claudeSize.height).toBeCloseTo(veniceSize.height, 0);
      await expect(claude.getByRole('heading', { name: 'Client access' })).toBeVisible();
      await expect(venice.getByRole('heading', { name: 'Provider setup' })).toBeVisible();
    }
  });

  test(`Claude setup, one-time native copy, another connection and clear at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.addInitScript(() => {
      const copies: string[] = [];
      Object.defineProperty(window, 'proxyCopies', { value: copies });
      Object.defineProperty(navigator, 'clipboard', {
        value: {
          writeText: async (text: string) => {
            copies.push(text);
          },
        },
      });
    });
    const state: ProxyState = {
      ...snapshot,
      auth: 'token',
      accounts: [],
      callers: [],
      presets: [
        {
          id: 'claude-code',
          label: 'Claude Code plan',
          upstream: 'https://api.anthropic.com',
          auth: { header: 'Authorization', prefix: 'Bearer ' },
          account_kind: 'claude-code',
        },
        {
          id: 'openai',
          label: 'OpenAI API',
          upstream: 'https://api.openai.com/v1',
          auth: { header: 'Authorization', prefix: 'Bearer ' },
        },
      ],
    };
    const mutations: { method: string; body: unknown }[] = [];
    const legacyCalls: string[] = [];
    let connections = 0;
    await page.route('http://proxy.test/_proxy/api/**', async (request) => {
      const path = new URL(request.request().url()).pathname;
      if (path.endsWith('/state')) return request.fulfill({ json: state });
      if (path === '/_proxy/api/claude/setup') {
        const method = request.request().method();
        mutations.push({ method, body: request.request().postDataJSON() });
        state.revision++;
        if (method === 'DELETE') {
          state.accounts = [];
          state.routes = [];
          state.callers.forEach((caller) => {
            caller.revoked = true;
            caller.grants = [];
          });
          return request.fulfill({ status: 204 });
        }
        connections++;
        state.accounts = [
          { name: 'native', kind: 'claude-code', state: 'native_login', generation: 1, updated: 1 },
        ];
        state.routes = [
          {
            name: 'native',
            account: 'native',
            local_prefix: '/claude-code/',
            upstream: 'https://api.anthropic.com',
            enabled: true,
            allow_insecure_http: false,
            caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
            set_headers: {},
          },
        ];
        state.callers.push({
          id: `client-${connections}`,
          name: `Claude connection ${connections}`,
          grants: ['native'],
          revoked: false,
          created: 1,
          created_by: '',
          external_id: '',
        });
        return request.fulfill({
          json: {
            revision: state.revision,
            url: 'https://proxy.example.test/claude-code/',
            token: `scoped-browser-canary-${connections}`,
            caller_id: `client-${connections}`,
          },
        });
      }
      if (path.includes('/connect/') || path.includes('/accounts')) legacyCalls.push(path);
      return request.fallback();
    });
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    await choosePreset(page, 'Claude Code');
    await expect(page.getByRole('heading', { name: 'Set up Claude Code' })).toBeVisible();
    await page.getByRole('button', { name: 'Create route & get launcher', exact: true }).click();
    const command = page.getByLabel('Launcher command', { exact: true });
    await expect(command).toHaveValue(
      "term-llm proxy run claude --url 'https://proxy.example.test/claude-code/' --token 'scoped-browser-canary-1'",
    );
    await expect(command).toBeFocused();
    await expect(page.getByRole('button', { name: 'Copy launcher command' })).toHaveClass(
      'primary',
    );
    await page.getByRole('button', { name: 'Copy launcher command' }).click();
    await expect(page.getByRole('status')).toContainText('Launcher command copied');
    await expect(page.getByLabel('Provider YAML')).toBeHidden();
    await page.getByText('Optional term-llm integration', { exact: true }).click();
    await page.getByRole('button', { name: 'Copy provider YAML' }).click();
    const copies = await page.evaluate(
      () => (window as unknown as { proxyCopies: string[] }).proxyCopies,
    );
    expect(copies).toEqual([
      "term-llm proxy run claude --url 'https://proxy.example.test/claude-code/' --token 'scoped-browser-canary-1'",
      "providers:\n  claude-bin:\n    proxy:\n      url: 'https://proxy.example.test/claude-code/'\n      token: 'scoped-browser-canary-1'\n",
    ]);
    expect(
      await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage })),
    ).not.toContain('scoped-browser-canary');
    expect(page.url()).not.toContain('scoped-browser-canary');
    page.once('dialog', (dialog) => dialog.dismiss());
    await page.getByRole('button', { name: 'Refresh state', exact: true }).click();
    await expect(command).toHaveValue(/scoped-browser-canary-1/);
    await page.getByRole('button', { name: 'I saved it — dismiss' }).click();
    const row = page.locator('[data-focus="route:native"]');
    await expect(row.getByRole('heading', { name: 'native' })).toBeVisible();
    await expect(row.locator('.route-summary code')).toHaveText('/claude-code/');
    await expect(row).toContainText('Native sign-in on each device');
    await expect(row.getByText('https://api.anthropic.com', { exact: true })).toBeHidden();
    await expect(row).not.toContainText('scoped-browser-canary');
    await expect(page.locator('.account-controls', { hasText: 'native' })).toHaveCount(0);
    await page.getByRole('button', { name: 'Clients', exact: true }).click();
    await expect(page.getByText('Claude connection 1', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Routes', exact: true }).click();
    await row.getByText('Add device', { exact: true }).click();
    await row.getByRole('button', { name: 'Create device launcher', exact: true }).click();
    await expect(command).toHaveValue(/scoped-browser-canary-2/);
    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    await expect(command).toHaveValue(/scoped-browser-canary-2/);
    await expect(page.locator('.route-picker')).toHaveCount(0);
    await page.screenshot({
      path: testInfo.outputPath(`claude-setup-${width}.png`),
      fullPage: true,
    });
    await page.getByRole('button', { name: 'I saved it — dismiss' }).click();
    await expect(command).toHaveCount(0);
    await page.locator('.claude-settings > summary').first().click();
    const clear = page.getByRole('button', { name: 'Clear all Claude setup', exact: true });
    page.once('dialog', async (dialog) => {
      expect(dialog.message()).toContain(
        'Remove all Claude proxy routes, device access, and saved proxy sign-ins',
      );
      expect(dialog.message()).toContain('your normal Claude setup are unchanged');
      await dialog.dismiss();
    });
    await clear.click();
    expect(mutations).toHaveLength(2);
    await expect(row).toBeVisible();
    page.once('dialog', (dialog) => dialog.accept());
    await clear.click();
    await expect(
      page.getByRole('heading', { name: 'Start with one route', exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('status')).toContainText('Claude proxy setup cleared.');
    expect(mutations).toEqual([
      { method: 'POST', body: { revision: 1 } },
      { method: 'POST', body: { revision: 2, route: 'native' } },
      { method: 'DELETE', body: { revision: 3 } },
    ]);
    expect(legacyCalls).toEqual([]);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  });
}

function configuredClaudeState(mode: ProxyState['mode'] = 'ui'): ProxyState {
  return {
    ...snapshot,
    auth: 'token',
    mode,
    has_requests: true,
    callers: [
      {
        id: 'native-client',
        name: 'Native client',
        created: 1,
        revoked: false,
        grants: ['manual-claude'],
        created_by: '',
        external_id: '',
      },
    ],
    accounts: [
      { name: 'manual-slot', kind: 'claude-code', state: 'ready', generation: 1, updated: 1 },
    ],
    routes: [
      {
        name: 'manual-claude',
        account: 'manual-slot',
        local_prefix: '/manual/',
        upstream: 'https://api.anthropic.com',
        enabled: true,
        allow_insecure_http: false,
        caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
        set_headers: {},
      },
      {
        name: 'openai',
        local_prefix: '/openai/',
        upstream: 'https://api.openai.com/v1',
        enabled: true,
        allow_insecure_http: false,
        caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
        set_headers: {},
      },
    ],
  };
}

for (const status of [409, 503]) {
  test(`Claude clear failure retains the route; clear is blocked during token display (${status})`, async ({
    page,
  }) => {
    const state = configuredClaudeState();
    let deletes = 0;
    await page.route('http://proxy.test/_proxy/api/**', async (request) => {
      const path = new URL(request.request().url()).pathname;
      if (path.endsWith('/state')) return request.fulfill({ json: state });
      if (path === '/_proxy/api/claude/setup') {
        if (request.request().method() === 'DELETE') {
          deletes++;
          return request.fulfill({ status, json: { error: 'Claude clear unavailable.' } });
        }
        state.revision++;
        return request.fulfill({
          json: {
            url: 'https://proxy.test/manual/',
            token: 'scoped-failure-canary',
            caller_id: 'client',
            revision: state.revision,
          },
        });
      }
      return request.fallback();
    });
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    const row = page.locator('[data-focus="route:manual-claude"]');
    await row.getByText('Add device', { exact: true }).click();
    await row.getByRole('button', { name: 'Create device launcher', exact: true }).click();
    const command = page.getByLabel('Launcher command', { exact: true });
    await expect(command).toHaveValue(/scoped-failure-canary/);
    await page.locator('.claude-settings > summary').first().click();
    const clear = page.getByRole('button', { name: 'Clear all Claude setup', exact: true });
    await clear.click();
    await expect(page.getByRole('status')).toContainText('Save or dismiss');
    await expect(command).toHaveValue(/scoped-failure-canary/);
    expect(deletes).toBe(0);
    await page.getByRole('button', { name: 'I saved it — dismiss' }).click();
    await expect(command).toHaveCount(0);
    await page.locator('.claude-settings > summary').first().click();
    page.once('dialog', (dialog) => dialog.accept());
    await clear.click();
    await expect(page.getByRole('status')).toContainText(
      status === 409 ? 'Claude clear unavailable.' : '503',
    );
    await expect(row).toBeVisible();
    await expect(page.getByRole('status')).not.toContainText('cleared');
    expect(deletes).toBe(1);
  });
}

for (const width of [1440, 390]) {
  test(`Claude file-managed explanation has no mutation controls at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.route('http://proxy.test/_proxy/api/state', (request) =>
      request.fulfill({ json: configuredClaudeState('file') }),
    );
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    const claude = page.locator('[data-focus="route:manual-claude"]');
    await expect(page.getByText(/Routes are file-managed and read-only/)).toBeVisible();
    await expect(claude.getByText('Add device', { exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Add route', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Edit manual-claude', exact: true })).toHaveCount(
      0,
    );
    await expect(page.locator('.route-list')).toContainText('manual-claude');
    await expect(page.locator('.route-list')).toContainText('openai');
    await expect(page.locator('.account-controls', { hasText: 'manual-slot' })).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  });
}

for (const width of [1440, 390]) {
  test(`Routes stays focused and Admin owns configuration at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    const configured = {
      ...snapshot,
      has_requests: true,
      accounts: [
        {
          name: 'claude',
          kind: 'claude-code',
          state: 'ready',
          auth_mode: 'token_only',
          generation: 1,
          updated: 1,
        },
      ],
      routes: [
        {
          name: 'claude',
          local_prefix: '/claude/',
          upstream: 'https://api.anthropic.com',
          enabled: true,
          account: 'claude',
          billing: 'plan',
          caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
          set_headers: {},
        },
      ],
      presets: [
        {
          id: 'openai',
          label: 'OpenAI API',
          upstream: 'https://api.openai.com/v1',
          auth: { header: 'Authorization', prefix: 'Bearer ' },
          billing: 'pay_as_you_go',
        },
        {
          id: 'long-label',
          label: 'ChatGPT OAuth embeddings and live — billing not specified',
          upstream: 'https://example.invalid',
          auth: { header: 'Authorization', prefix: 'Bearer ' },
        },
      ],
    };
    await page.route('http://proxy.test/_proxy/api/state', (route) =>
      route.fulfill({ json: configured }),
    );
    await page.goto('http://proxy.test/_proxy/?tab=routes');
    await expect(page.getByRole('heading', { name: 'Routes', exact: true })).toBeVisible();
    await expect(page.locator('.onboarding')).toHaveCount(0);
    await expect(page.locator('.preset-pill')).toHaveCount(0);
    const navigationWidths = await page
      .getByRole('navigation', { name: 'Proxy administration', exact: true })
      .getByRole('button')
      .evaluateAll((nodes) =>
        nodes.map((node) => ({ visible: node.clientWidth, content: node.scrollWidth })),
      );
    for (const size of navigationWidths) expect(size.content).toBeLessThanOrEqual(size.visible + 1);
    await expect(page.getByRole('button', { name: 'Download routes.yaml' })).toHaveCount(0);
    await expect(
      page.getByRole('button', { name: 'Clear all Claude setup', exact: true }),
    ).toHaveCount(0);
    await page.locator('.claude-settings > summary').first().click();
    await expect(
      page.getByRole('button', { name: 'Clear all Claude setup', exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('button', { name: 'Manage', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Edit claude', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add Claude account', exact: true })).toHaveCount(
      0,
    );
    await choosePreset(page, 'OpenAI');
    await expect(page.getByLabel('Name', { exact: true })).toHaveValue('openai');
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath(`routes-focused-${width}.png`),
      fullPage: true,
    });
    await page.getByRole('button', { name: 'Admin', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Admin', exact: true })).toBeVisible();
    await expect(page).toHaveURL(/tab=admin/);
    await expect(
      page.getByRole('button', { name: 'Download routes.yaml', exact: true }),
    ).toBeVisible();
    await page.getByLabel('Import source', { exact: true }).selectOption('paste');
    const yaml = page.getByLabel('Route configuration YAML', { exact: true });
    await expect(yaml).toBeVisible();
    expect(await yaml.evaluate((node) => node.closest('details'))).toBeNull();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath(`admin-config-${width}.png`),
      fullPage: true,
    });
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Admin', exact: true })).toBeVisible();
  });
}

test('ChatGPT sign-in creates a route before granting an existing client access', async ({
  page,
}) => {
  const chatGPTRoute = {
    name: 'chatgpt',
    local_prefix: '/chatgpt/',
    upstream: 'https://chatgpt.com/backend-api/codex',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  };
  const state: ProxyState = {
    ...snapshot,
    auth: 'token',
    accounts: [],
    routes: [
      {
        ...chatGPTRoute,
        name: 'legacy',
        local_prefix: '/legacy/',
        upstream: 'https://example.test/v1',
      },
    ],
    callers: [
      {
        id: 'desktop',
        name: 'Desktop',
        grants: ['legacy'],
        created: 1,
        revoked: false,
        created_by: '',
        external_id: '',
      },
    ],
    presets: [
      {
        id: 'chatgpt',
        label: 'ChatGPT plan',
        upstream: chatGPTRoute.upstream,
        auth: chatGPTRoute.caller_auth,
        account_kind: 'chatgpt',
      },
    ],
  };
  let approved = false;
  const grants: unknown[] = [];
  await page.route('http://proxy.test/_proxy/api/**', async (request) => {
    const url = new URL(request.request().url());
    const method = request.request().method();
    if (url.pathname.endsWith('/state')) return request.fulfill({ json: state });
    if (url.pathname.endsWith('/chatgpt/setup')) {
      if (method === 'POST') {
        expect(request.request().postDataJSON()).toEqual({});
        return request.fulfill({
          json: {
            id: 'mock-attempt',
            user_code: 'TEST-1234',
            verification_url: 'https://auth.openai.com/codex/device',
          },
        });
      }
      expect(url.searchParams.get('id')).toBe('mock-attempt');
      if (!approved) return request.fulfill({ json: { status: 'pending' } });
      state.routes.push({ ...chatGPTRoute, account: 'chatgpt-account' });
      state.accounts = [
        { name: 'chatgpt-account', kind: 'chatgpt', state: 'ready', generation: 1, updated: 1 },
      ];
      state.revision = 2;
      return request.fulfill({ json: { status: 'connected', route: chatGPTRoute.name } });
    }
    if (url.pathname.endsWith('/callers') && method === 'PUT') {
      grants.push(request.request().postDataJSON());
      state.callers[0].grants.push(chatGPTRoute.name);
      state.revision = 3;
      return request.fulfill({ status: 204 });
    }
    return request.fallback();
  });
  await page.goto('http://proxy.test/_proxy/?tab=routes');
  await choosePreset(page, 'ChatGPT');
  await page.getByRole('button', { name: 'Sign in to ChatGPT', exact: true }).click();
  const code = page.getByLabel('One-time sign-in code', { exact: true });
  await expect(code).toHaveValue('TEST-1234');
  await expect(page.getByRole('link', { name: 'Continue to OpenAI' })).toHaveAttribute(
    'href',
    'https://auth.openai.com/codex/device',
  );
  const row = page.locator('[data-focus="route:chatgpt"]');
  await expect(row).toHaveCount(0);
  approved = true;
  await expect(row.getByRole('heading', { name: 'chatgpt', exact: true })).toBeFocused();
  await expect(row).toContainText('Needs client access');
  await expect(code).toHaveCount(0);
  expect(grants).toEqual([]);
  await row.getByRole('button', { name: 'Grant access for route chatgpt', exact: true }).click();
  await row.getByLabel('Client', { exact: true }).selectOption('desktop');
  await row.getByRole('button', { name: 'Grant access', exact: true }).click();
  await expect(
    row.getByRole('button', { name: 'Manage access for route chatgpt', exact: true }),
  ).toBeVisible();
  await expect(row.locator('.route-summary')).toContainText('Configured');
  expect(grants).toEqual([{ id: 'desktop', grants: ['legacy', 'chatgpt'], revision: 2 }]);
});

test('Routes consistently offers one Add route action before and after recorded requests', async ({
  page,
}) => {
  let hasRequests = false;
  await page.route('http://proxy.test/_proxy/api/state', (route) =>
    route.fulfill({ json: { ...snapshot, has_requests: hasRequests } }),
  );
  await page.goto('http://proxy.test/_proxy/?tab=routes');
  const add = page.getByRole('button', { name: 'Add route', exact: true });
  await expect(add).toHaveCount(1);
  await expect(page.locator('.onboarding')).toHaveCount(0);
  hasRequests = true;
  await page.getByRole('button', { name: 'Refresh state', exact: true }).click();
  await expect(add).toHaveCount(1);
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Routes', exact: true })).toBeVisible();
  await expect(add).toHaveCount(1);
});
