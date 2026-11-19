import { afterEach, expect, it, vi } from 'vitest';
import { fireEvent, getByLabelText, getByRole, waitFor } from '@testing-library/preact';
import { ProxyClient, type ProxyRoute, type ProxyState } from '../src/api/proxy-client';
import { providerOnboarding, providerRouteAction } from './provider-onboarding';
import type { UIContext } from './dom';

const route: ProxyRoute = {
  name: 'chatgpt-work',
  account: 'work',
  local_prefix: '/chatgpt-work/',
  upstream: 'https://chatgpt.com/backend-api/codex',
  enabled: true,
  allow_insecure_http: false,
  caller_auth: { header: 'Authorization', prefix: 'Bearer ' },
  set_headers: {},
};
const preset = {
  id: 'chatgpt',
  label: 'ChatGPT plan',
  upstream: route.upstream,
  auth: route.caller_auth,
};
const verificationURL = 'https://auth.openai.com/codex/device';

afterEach(() => {
  document.body.replaceChildren();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function fixture(reconnect = false) {
  const state: ProxyState = {
    auth: 'token',
    mode: 'ui',
    revision: 1,
    csrf: 'csrf',
    routes: reconnect ? [route] : [],
    secrets: [],
    callers: [],
    admin_keys: [],
    accounts: reconnect
      ? [{ name: 'work', kind: 'chatgpt', state: 'ready', generation: 1, updated: 1 }]
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
  vi.spyOn(api, 'startChatGPTSetup').mockResolvedValue({
    id: 'attempt',
    user_code: 'ABCD-1234',
    verification_url: verificationURL,
  });
  vi.spyOn(api, 'cancelChatGPTSetup').mockResolvedValue();
  const view = reconnect
    ? (providerRouteAction(route, state, api, context)! as HTMLDetailsElement)
    : providerOnboarding(preset, state, api, context, close)!;
  if (view instanceof HTMLDetailsElement) view.open = true;
  document.body.append(view);
  return { api, view, close, errors, context };
}

it('starts with an optional name, waits for OpenAI approval, then refreshes to the route without granting a client', async () => {
  const { api, view, context } = fixture();
  vi.spyOn(api, 'chatGPTSetupStatus')
    .mockResolvedValueOnce({ status: 'pending' })
    .mockResolvedValueOnce({ status: 'connected', route: route.name });
  const createCaller = vi.spyOn(api, 'createCaller');
  (view.querySelector('details') as HTMLDetailsElement).open = true;
  fireEvent.input(getByLabelText(view, 'Route name'), { target: { value: route.name } });
  vi.useFakeTimers();
  fireEvent.click(getByRole(view, 'button', { name: 'Sign in to ChatGPT' }));
  await vi.advanceTimersByTimeAsync(0);
  expect(api.startChatGPTSetup).toHaveBeenCalledExactlyOnceWith({ name: route.name });
  expect(getByLabelText(view, 'One-time sign-in code')).toHaveValue('ABCD-1234');
  expect(getByRole(view, 'link', { name: 'Continue to OpenAI' })).toHaveAttribute(
    'href',
    verificationURL,
  );
  await vi.advanceTimersByTimeAsync(2000);
  expect(context.refresh).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(2000);
  expect(api.chatGPTSetupStatus).toHaveBeenCalledTimes(2);
  expect(context.refresh).toHaveBeenCalledExactlyOnceWith(`route:${route.name}`);
  expect(context.announce).toHaveBeenCalledWith(expect.stringContaining('Approve clients'));
  expect(createCaller).not.toHaveBeenCalled();
});

it('reconnects the existing account and preserves route access', async () => {
  const { api, view, context } = fixture(true);
  vi.spyOn(api, 'chatGPTSetupStatus').mockResolvedValue({ status: 'connected' });
  vi.useFakeTimers();
  fireEvent.click(getByRole(view, 'button', { name: 'Reconnect ChatGPT' }));
  await vi.advanceTimersByTimeAsync(2000);
  expect(api.startChatGPTSetup).toHaveBeenCalledExactlyOnceWith({ account: 'work' });
  expect(context.refresh).toHaveBeenCalledExactlyOnceWith(`route:${route.name}`);
  expect(context.announce).toHaveBeenCalledWith(expect.stringContaining('access is unchanged'));
});

it('cancels an unfinished sign-in without fetching status or refreshing routes', async () => {
  const { api, view, close, context } = fixture();
  const status = vi.spyOn(api, 'chatGPTSetupStatus');
  vi.useFakeTimers();
  fireEvent.click(getByRole(view, 'button', { name: 'Sign in to ChatGPT' }));
  await vi.advanceTimersByTimeAsync(0);
  fireEvent.click(getByRole(view, 'button', { name: 'Cancel sign-in' }));
  await vi.advanceTimersByTimeAsync(2000);
  expect(api.cancelChatGPTSetup).toHaveBeenCalledExactlyOnceWith('attempt');
  expect(close).toHaveBeenCalledOnce();
  expect(status).not.toHaveBeenCalled();
  expect(context.refresh).not.toHaveBeenCalled();
});

it('does not display or navigate to an unexpected device URL', async () => {
  const { api, view, errors } = fixture();
  vi.mocked(api.startChatGPTSetup).mockResolvedValue({
    id: 'attempt',
    user_code: 'ABCD-1234',
    verification_url: 'https://other.example/',
  });
  fireEvent.click(getByRole(view, 'button', { name: 'Sign in to ChatGPT' }));
  await waitFor(() => expect(errors).toHaveLength(1));
  expect(api.cancelChatGPTSetup).toHaveBeenCalledExactlyOnceWith('attempt');
  expect(view.querySelector('a')).toBeNull();
});
