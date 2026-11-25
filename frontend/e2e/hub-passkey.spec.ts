import { expect, test } from '@playwright/test';
import { writeFileSync } from 'node:fs';

const smokeURL = process.env.TERM_LLM_HUB_SMOKE_URL || '';
const smokeToken = process.env.TERM_LLM_HUB_SMOKE_TOKEN || '';
const credentialFile = process.env.TERM_LLM_HUB_CREDENTIAL_FILE || '';

test('enrolls and authenticates with a virtual passkey', async ({ page, context }) => {
  test.skip(!smokeURL || !smokeToken || !credentialFile, 'passkey Hub smoke is not configured');
  const cdp = await context.newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  const { authenticatorId: primaryAuthenticatorId } = await cdp.send(
    'WebAuthn.addVirtualAuthenticator',
    {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    },
  );

  await page.goto(smokeURL);
  await expect(page).toHaveURL(/\/hub\/auth\/setup$/);
  await page.getByLabel('One-time setup code').fill(smokeToken);
  await page.getByLabel('Passkey name').fill('Virtual platform passkey');
  await page.getByRole('button', { name: 'Verify and create passkey' }).click();
  await expect(page).toHaveURL(/\/hub\/$/);
  const sessionCookie = (await context.cookies()).find(
    (cookie) => cookie.name === 'term_llm_hub_session',
  );
  expect(sessionCookie).toMatchObject({
    httpOnly: true,
    secure: true,
    sameSite: 'Strict',
    path: '/hub/',
  });
  await expect(page.getByRole('button', { name: 'Security', exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Security', exact: true }).click();
  await expect(page.getByText('Virtual platform passkey')).toBeVisible();

  await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'usb',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });
  page.once('dialog', (dialog) => void dialog.accept('Virtual second key'));
  await page.getByRole('button', { name: 'Add passkey' }).click();
  await expect(page.getByText('Virtual second key')).toBeVisible();
  let second = page.locator('.security-credential').filter({ hasText: 'Virtual second key' });
  page.once('dialog', (dialog) => void dialog.accept('Renamed virtual key'));
  await second.getByRole('button', { name: 'Rename' }).click();
  await expect(page.getByText('Renamed virtual key')).toBeVisible();
  second = page.locator('.security-credential').filter({ hasText: 'Renamed virtual key' });
  await second.getByRole('button', { name: 'Remove' }).click();
  await expect(page.getByText('Renamed virtual key')).toHaveCount(0);
  await expect(
    page
      .locator('.security-credential')
      .filter({ hasText: 'Virtual platform passkey' })
      .getByRole('button', { name: 'Remove' }),
  ).toBeDisabled();
  await page.getByRole('button', { name: 'Revoke other sessions' }).click();
  await expect(page.getByText('Revoked 0 other sessions.')).toBeVisible();

  await page.getByRole('button', { name: 'Close security' }).click();
  const providerURL = 'https://oauth-provider.test/authorize';
  const nodeID = 'missing-oauth-smoke-node';
  const callbackPath = `/hub/node/${nodeID}/v1/mcp/oauth/callback`;
  const callbackURL = new URL(callbackPath, smokeURL);
  const oauthValues = { code: 'synthetic-oauth-code', state: 'synthetic-oauth-state' };
  callbackURL.search = new URLSearchParams(oauthValues).toString();
  await page.route(providerURL, (route) =>
    route.fulfill({
      contentType: 'text/html',
      body: `<a href="${callbackURL.href.replaceAll('&', '&amp;')}">Return to Hub</a>`,
    }),
  );
  await page.goto(providerURL);
  await expect(page).toHaveURL(providerURL);
  // Click from a different site so Chromium applies its top-level navigation cookie rules.
  await page.getByRole('link', { name: 'Return to Hub' }).click();
  const loginURL = new URL('/hub/auth/login', smokeURL);
  await expect(page).toHaveURL(
    (url) => url.origin === loginURL.origin && url.pathname === loginURL.pathname,
  );
  const login = new URL(page.url());
  expect([...login.searchParams.keys()]).toEqual(['return']);
  const returnPath = login.searchParams.get('return') ?? '';
  const continuation = new URL(returnPath, smokeURL);
  expect(continuation.pathname).toBe(callbackPath);
  expect(returnPath).toBe(`${callbackPath}${continuation.search}`);
  expect([...continuation.searchParams.keys()]).toEqual(['_term_llm_resume']);
  expect(continuation.searchParams.get('_term_llm_resume')).toBeTruthy();
  for (const value of Object.values(oauthValues)) {
    expect(login.href).not.toContain(value);
  }
  expect(
    (await context.cookies()).find((cookie) => cookie.name === 'term_llm_hub_mcp_oauth'),
  ).toMatchObject({
    httpOnly: true,
    secure: true,
    sameSite: 'Strict',
  });

  const [resumed] = await Promise.all([
    page.waitForResponse((response) => response.url() === continuation.href),
    page.getByRole('button', { name: 'Sign in with a passkey' }).click(),
  ]);
  await expect(page).toHaveURL(continuation.href);
  for (const value of Object.values(oauthValues)) {
    expect(page.url()).not.toContain(value);
  }
  // Reaching node lookup proves the continuation was accepted, even with no node to forward to.
  expect(resumed.status()).toBe(404);
  expect(await resumed.text()).toContain(`unknown node "${nodeID}"`);

  await page.goto(smokeURL);
  await page.getByRole('button', { name: 'Security', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
  await page.getByRole('button', { name: 'Sign out' }).click();
  await expect(page).toHaveURL(/\/hub\/auth\/login$/);

  const { credentials } = await cdp.send('WebAuthn.getCredentials', {
    authenticatorId: primaryAuthenticatorId,
  });
  expect(credentials).toHaveLength(1);
  writeFileSync(credentialFile, JSON.stringify(credentials[0]));
});
