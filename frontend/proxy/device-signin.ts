import { ProxyAPIError } from '../src/api/proxy-client';
import { clipboardButton, selectCopyField } from './clipboard';
import type {
  ProxyDeviceSignIn,
  ProxyDeviceProvider,
  ProxyClient,
  ProxyRoute,
  ProxyState,
} from '../src/api/proxy-client';
import { actions, button, disclosure, el, field, input, submit, type UIContext } from './dom';
import { requireProviderName, slugPattern } from './provider-name';

const chatGPTDeviceURL = 'https://auth.openai.com/codex/device';

export function isDeviceProvider(id?: string): id is ProxyDeviceProvider {
  return id === 'grok' || id === 'chatgpt';
}

function deviceURL(provider: ProxyDeviceProvider, value: string): string | undefined {
  if (provider === 'chatgpt') {
    return value === chatGPTDeviceURL || value === 'https://auth.openai.com:443/codex/device'
      ? chatGPTDeviceURL
      : undefined;
  }
  try {
    const url = new URL(value);
    if (
      url.protocol === 'https:' &&
      !url.port &&
      !url.username &&
      !url.password &&
      (url.hostname === 'x.ai' || url.hostname.endsWith('.x.ai'))
    )
      return url.href;
  } catch {
    // Malformed or relative destination.
  }
  return undefined;
}

function transientStatusError(error: unknown): boolean {
  return (
    !(error instanceof ProxyAPIError) ||
    error.status === 408 ||
    error.status === 429 ||
    error.status >= 500
  );
}

export function deviceSignIn(
  provider: ProxyDeviceProvider,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  close: () => void,
  route?: ProxyRoute,
): HTMLElement {
  const view = el('div');
  view.className = 'provider-onboarding';
  const name = input();
  const label = provider === 'chatgpt' ? 'ChatGPT' : 'Grok';
  const issuer = provider === 'chatgpt' ? 'OpenAI' : 'xAI';
  name.placeholder = provider;
  name.pattern = slugPattern;
  name.maxLength = 64;
  const form = el(
    'form',
    el(
      'p',
      route
        ? 'Sign in again to renew this provider’s account. Existing client access stays in place.'
        : `Sign in with your ${label} subscription. The proxy keeps the login; you approve clients separately.`,
    ),
  );
  if (!route) {
    form.append(disclosure('Provider name (optional)', field('Provider name', name)));
    form.append(button('Cancel', close));
  }
  let active: string | undefined;

  async function cancel(id: string) {
    if (active !== id) return;
    active = undefined;
    await api.cancelDeviceSetup(provider, id);
  }

  function abandon(id: string) {
    // Best effort: the server also expires abandoned attempts.
    void cancel(id).catch(() => {});
  }

  function renderPrompt(signIn: ProxyDeviceSignIn, destination: string) {
    const code = input(signIn.user_code);
    code.readOnly = true;
    const copy = clipboardButton('Copy code', context, {
      read: () => code.value,
      copied: `${label} sign-in code copied.`,
      fallback: () => selectCopyField(code),
      unavailable: 'Copy the selected code manually.',
    });
    const link = el('a', `Continue to ${issuer}`);
    link.href = destination;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    const back = button('Cancel sign-in', () => {
      void context.run(async () => {
        try {
          await cancel(signIn.id);
        } finally {
          if (route) view.replaceChildren(form);
          else close();
        }
      }, back);
    });
    const waiting = el('p', `Waiting for you to finish signing in at ${issuer}…`);
    waiting.role = 'status';
    view.replaceChildren(
      el('p', `Open ${issuer}, enter this code, and approve the sign-in. Then return here.`),
      field('One-time sign-in code', code),
      actions(copy, link, back),
      waiting,
    );
    // Let the submit action settle before moving focus from its now-detached button.
    link.focus();
  }

  function poll(id: string, failures = 0) {
    window.setTimeout(() => {
      if (active !== id) return;
      if (!view.isConnected) {
        abandon(id);
        return;
      }
      let ran = false;
      void context
        .run(async () => {
          ran = true;
          let result;
          try {
            result = await api.deviceSetupStatus(provider, id);
          } catch (error) {
            if (active !== id) return;
            if (!view.isConnected) {
              abandon(id);
              return;
            }
            // Retry just the status read, never the setup or post-success refresh.
            if (failures < 2 && transientStatusError(error)) {
              poll(id, failures + 1);
              return;
            }
            throw error;
          }
          if (active !== id) return;
          if (!view.isConnected) {
            if (result.status === 'pending') abandon(id);
            else active = undefined;
            return;
          }
          if (result.status === 'pending') {
            poll(id);
            return;
          }
          active = undefined;
          if (result.status === 'failed') {
            view.replaceChildren(form);
            const message =
              result.error ===
              (provider === 'chatgpt'
                ? 'Sign in to the original ChatGPT account and workspace, or create a separate route'
                : 'Sign in to the original Grok account, or create a separate route')
                ? result.error
                : `${label} sign-in did not finish. Start again to get a fresh code.`;
            throw new Error(message);
          }
          const message = route
            ? `${label} reconnected. Existing client access is unchanged.`
            : `${label} connected. Approve clients for ${label} when you’re ready.`;
          // Don’t invite another sign-in if setup succeeded but the view refresh fails.
          view.replaceChildren(el('p', message));
          if (!context.hasProtectedToken?.()) {
            const routeName = result.route ?? route?.name;
            await context.refresh(routeName && `route:${routeName}`);
          }
          context.announce(message);
        })
        .then((ok) => {
          if (!ran && active === id) {
            poll(id, failures);
            return;
          }
          if (!ok && active === id) {
            abandon(id);
            view.replaceChildren(form);
          }
        });
    }, 2000);
  }

  submit(
    form,
    context,
    async () => {
      const requested = name.value.trim();
      if (!route && requested) requireProviderName(requested, state);
      let signIn: ProxyDeviceSignIn;
      try {
        signIn = await api.startDeviceSetup(
          provider,
          route ? { account: route.account } : requested ? { name: requested } : {},
        );
      } catch (error) {
        if (!view.isConnected) return;
        throw error;
      }
      active = signIn.id;
      if (!view.isConnected) {
        abandon(signIn.id);
        return;
      }
      const destination = deviceURL(provider, signIn.verification_url);
      if (!destination) {
        await cancel(signIn.id);
        throw new Error(`Unexpected ${label} sign-in destination.`);
      }
      renderPrompt(signIn, destination);
      poll(signIn.id);
    },
    route ? `Reconnect ${label}` : `Sign in to ${label}`,
  );
  view.append(form);
  return view;
}
