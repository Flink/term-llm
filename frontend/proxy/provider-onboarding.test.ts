import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import {
  ProxyAPIError,
  ProxyClient,
  type ProxyAdminRoute,
  type ProxyDeviceSetupStatus,
  type ProxyDeviceSignIn,
  type ProxyState,
} from '../src/api/proxy-client';
import { providerOnboarding, providerRouteAction } from './provider-onboarding';
import type { UIContext } from './dom';

afterEach(() => {
  document.body.replaceChildren();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe.each([
  ['chatgpt', 'ChatGPT', 'OpenAI', 'https://auth.openai.com/codex/device'],
  ['grok', 'Grok', 'xAI', 'https://accounts.x.ai/device'],
] as const)('%s device sign-in', (provider, label, issuer, verificationURL) => {
  const route: ProxyAdminRoute = {
    serves: [],
    profile: provider,
    provider: `${provider}-work`,
    name: `${provider}-work`,
    account: 'work',
    local_prefix: `/${provider}-work/`,
    upstream:
      provider === 'chatgpt'
        ? 'https://chatgpt.com/backend-api/codex'
        : 'https://cli-chat-proxy.grok.com/v1',
    enabled: true,
    allow_insecure_http: false,
    caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
    set_headers: {},
  };
  const preset = {
    family: 'inference' as const,
    id: provider,
    label,
    upstream: route.upstream,
    auth: route.caller_auth,
  };

  function fixture(reconnect = false) {
    const state: ProxyState = {
      defaults: null,
      providers: [],
      auth: 'token',
      mode: 'ui',
      revision: 1,
      csrf: 'csrf',
      routes: reconnect ? [route] : [],
      secrets: [],
      callers: [],
      admin_keys: [],
      accounts: reconnect
        ? [{ name: 'work', kind: provider, state: 'ready', generation: 1, updated: 1 }]
        : [],
    };
    const api = new ProxyClient(vi.fn());
    const errors: unknown[] = [];
    const context: UIContext = {
      announce: vi.fn(),
      refresh: vi.fn().mockResolvedValue(state),
      navigate: vi.fn(),
      protectToken: vi.fn(),
      async run(action) {
        try {
          await action();
          return true;
        } catch (error) {
          errors.push(error);
          return false;
        }
      },
    };
    const close = vi.fn();
    vi.spyOn(api, 'startDeviceSetup').mockResolvedValue({
      id: 'attempt',
      user_code: 'ABCD-1234',
      verification_url: verificationURL,
    });
    vi.spyOn(api, 'cancelDeviceSetup').mockResolvedValue();
    const view = reconnect
      ? (providerRouteAction(route, state, api, context)! as HTMLDetailsElement)
      : providerOnboarding(preset, state, api, context, close)!;
    if (view instanceof HTMLDetailsElement) view.open = true;
    document.body.append(view);
    return { api, view, close, errors, context };
  }

  it('starts with an optional name, waits for approval, then refreshes to the route without granting a client', async () => {
    const { api, view, context } = fixture();
    vi.spyOn(api, 'deviceSetupStatus')
      .mockResolvedValueOnce({ status: 'pending' })
      .mockResolvedValueOnce({ status: 'connected', route: route.name });
    const createCaller = vi.spyOn(api, 'createCaller');
    (view.querySelector('details') as HTMLDetailsElement).open = true;
    fireEvent.input(getByLabelText(view, 'Provider name'), { target: { value: route.name } });
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await vi.advanceTimersByTimeAsync(0);
    expect(api.startDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, { name: route.name });
    expect(getByLabelText(view, 'One-time sign-in code')).toHaveValue('ABCD-1234');
    expect(getByRole(view, 'link', { name: `Continue to ${issuer}` })).toHaveAttribute(
      'href',
      verificationURL,
    );
    await vi.advanceTimersByTimeAsync(2000);
    expect(context.refresh).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(2000);
    expect(api.deviceSetupStatus).toHaveBeenCalledTimes(2);
    expect(context.refresh).toHaveBeenCalledExactlyOnceWith(`route:${route.name}`);
    expect(context.announce).toHaveBeenCalledWith(expect.stringContaining('Approve clients'));
    expect(createCaller).not.toHaveBeenCalled();
  });

  it('reconnects the existing account and preserves route access', async () => {
    const { api, view, context } = fixture(true);
    vi.spyOn(api, 'deviceSetupStatus').mockResolvedValue({ status: 'connected' });
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Reconnect ${label}` }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(api.startDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, { account: 'work' });
    expect(context.refresh).toHaveBeenCalledExactlyOnceWith(`route:${route.name}`);
    expect(context.announce).toHaveBeenCalledWith(expect.stringContaining('access is unchanged'));
  });

  it('cancels an unfinished sign-in without fetching status or refreshing routes', async () => {
    const { api, view, close, context } = fixture();
    const status = vi.spyOn(api, 'deviceSetupStatus');
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await vi.advanceTimersByTimeAsync(0);
    fireEvent.click(getByRole(view, 'button', { name: 'Cancel sign-in' }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(api.cancelDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, 'attempt');
    expect(close).toHaveBeenCalledOnce();
    expect(status).not.toHaveBeenCalled();
    expect(context.refresh).not.toHaveBeenCalled();
  });

  it('works without URL.parse and allows an explicit default HTTPS port', async () => {
    vi.stubGlobal(
      'URL',
      Object.defineProperty(class extends URL {}, 'parse', { value: undefined }),
    );
    const { api, view } = fixture();
    vi.mocked(api.startDeviceSetup).mockResolvedValue({
      id: 'attempt',
      user_code: 'ABCD',
      verification_url: verificationURL.replace(/(https:\/\/[^/]+)/, '$1:443'),
    });
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await vi.advanceTimersByTimeAsync(0);
    expect(getByRole(view, 'link')).toHaveAttribute('href', verificationURL);
  });

  it.each([
    'not an absolute URL',
    'https://accounts.x.ai.other.example/',
    'http://x.ai/device',
    'https://someone@accounts.x.ai/device',
    verificationURL.replace(/(https:\/\/[^/]+)/, '$1:8443'),
    provider === 'chatgpt' ? verificationURL + '?next=elsewhere' : 'https://notx.ai/device',
  ])('rejects unexpected device URL %s', async (unsafeURL) => {
    const { api, view, errors } = fixture();
    vi.mocked(api.startDeviceSetup).mockResolvedValue({
      id: 'attempt',
      user_code: 'ABCD-1234',
      verification_url: unsafeURL,
    });
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await waitFor(() => expect(errors).toHaveLength(1));
    expect(api.cancelDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, 'attempt');
    expect(view.querySelector('a')).toBeNull();
  });

  it.each(['connected', 'failed', 'pending', 'offline'] as const)(
    'handles a deferred %s status after navigation without affecting the new view',
    async (outcome) => {
      const { api, view, context, errors } = fixture();
      let finish!: () => void;
      vi.spyOn(api, 'deviceSetupStatus').mockImplementation(
        async (): Promise<ProxyDeviceSetupStatus> => {
          await new Promise<void>((resolve) => {
            finish = resolve;
          });
          if (outcome === 'offline') throw new TypeError('offline');
          return { status: outcome };
        },
      );
      vi.mocked(api.cancelDeviceSetup).mockRejectedValue(new Error('cleanup unavailable'));
      vi.useFakeTimers();
      fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
      await vi.advanceTimersByTimeAsync(2000);
      expect(api.deviceSetupStatus).toHaveBeenCalledExactlyOnceWith(provider, 'attempt');
      view.remove();
      finish();
      await vi.advanceTimersByTimeAsync(0);
      if (outcome === 'pending' || outcome === 'offline') {
        expect(api.cancelDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, 'attempt');
      } else {
        expect(api.cancelDeviceSetup).not.toHaveBeenCalled();
      }
      expect(vi.getTimerCount()).toBe(0);
      expect(context.refresh).not.toHaveBeenCalled();
      expect(context.announce).not.toHaveBeenCalled();
      expect(errors).toEqual([]);
    },
  );

  it('silently abandons a start that completes after navigation', async () => {
    const { api, view, context, errors } = fixture();
    let finish!: (signIn: ProxyDeviceSignIn) => void;
    vi.mocked(api.startDeviceSetup).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    vi.mocked(api.cancelDeviceSetup).mockRejectedValue(new Error('cleanup unavailable'));
    const status = vi.spyOn(api, 'deviceSetupStatus');
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    view.remove();
    finish({ id: 'attempt', user_code: 'ABCD', verification_url: verificationURL });
    await vi.advanceTimersByTimeAsync(2000);
    expect(api.cancelDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, 'attempt');
    expect(status).not.toHaveBeenCalled();
    expect(context.refresh).not.toHaveBeenCalled();
    expect(context.announce).not.toHaveBeenCalled();
    expect(errors).toEqual([]);
  });

  it('keeps polling the same attempt after a transient status failure', async () => {
    const { api, view, context, errors } = fixture();
    vi.spyOn(api, 'deviceSetupStatus')
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValueOnce({ status: 'connected', route: route.name });
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(getByRole(view, 'link')).toBeVisible();
    await vi.advanceTimersByTimeAsync(2000);
    expect(errors).toEqual([]);
    expect(api.startDeviceSetup).toHaveBeenCalledOnce();
    expect(api.cancelDeviceSetup).not.toHaveBeenCalled();
    expect(context.refresh).toHaveBeenCalledExactlyOnceWith(`route:${route.name}`);
  });

  it.each([
    [503, 3],
    [401, 1],
  ])('stops after persistent HTTP %i status errors', async (httpStatus, reads) => {
    const { api, view, errors } = fixture();
    const error = new ProxyAPIError(httpStatus, 'Status unavailable');
    vi.spyOn(api, 'deviceSetupStatus').mockRejectedValue(error);
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await vi.advanceTimersByTimeAsync(20000);
    expect(errors).toEqual([error]);
    expect(api.deviceSetupStatus).toHaveBeenCalledTimes(reads);
    expect(api.cancelDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, 'attempt');
    expect(getByRole(view, 'button', { name: `Sign in to ${label}` })).toBeVisible();
  });

  it('shows the provider-specific identity mismatch guidance on reconnect', async () => {
    const { api, view, errors, context } = fixture(true);
    const message =
      provider === 'grok'
        ? 'Sign in to the original Grok account, or create a separate route'
        : 'Sign in to the original ChatGPT account and workspace, or create a separate route';
    vi.spyOn(api, 'deviceSetupStatus').mockResolvedValue({ status: 'failed', error: message });
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Reconnect ${label}` }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(errors).toEqual([new Error(message)]);
    expect(getByRole(view, 'button', { name: `Reconnect ${label}` })).toBeVisible();
    expect(context.refresh).not.toHaveBeenCalled();
  });

  it('uses canonical setup when no name is entered, and allows retry on failure', async () => {
    const { api, view, errors, context } = fixture();
    vi.spyOn(api, 'deviceSetupStatus').mockResolvedValue({
      status: 'failed',
      error: 'private upstream error',
    });
    vi.useFakeTimers();
    fireEvent.click(getByRole(view, 'button', { name: `Sign in to ${label}` }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(api.startDeviceSetup).toHaveBeenCalledExactlyOnceWith(provider, {});
    expect(errors).toEqual([
      new Error(`${label} sign-in did not finish. Start again to get a fresh code.`),
    ]);
    expect(getByRole(view, 'button', { name: `Sign in to ${label}` })).toBeVisible();
    expect(context.refresh).not.toHaveBeenCalled();
  });

  it('uses provider-specific setup endpoints and encodes attempt IDs', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json({ csrf: 'csrf' }))
      .mockResolvedValueOnce(
        Response.json({ id: 'a/b', verification_url: verificationURL, user_code: 'ABCD' }),
      )
      .mockResolvedValueOnce(Response.json({ status: 'pending' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    const api = new ProxyClient(fetcher);
    await api.login('admin');
    const attempt = await api.startDeviceSetup(provider, { name: route.name });
    await api.deviceSetupStatus(provider, attempt.id);
    await api.cancelDeviceSetup(provider, attempt.id);
    const path = `/_proxy/api/${provider}/setup`;
    const headers = expect.objectContaining({ 'X-CSRF-Token': 'csrf' });
    expect(fetcher.mock.calls.slice(1)).toEqual([
      [
        path,
        expect.objectContaining({
          method: 'POST',
          headers,
          body: JSON.stringify({ name: route.name }),
        }),
      ],
      [path + '?id=a%2Fb', expect.objectContaining({ method: 'GET' })],
      [path + '?id=a%2Fb', expect.objectContaining({ method: 'DELETE', headers })],
    ]);
  });
});
