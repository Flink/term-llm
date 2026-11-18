import type {
  ProxyChatGPTDeviceSignIn,
  ProxyClient,
  ProxyPreset,
  ProxyRoute,
  ProxyState,
} from '../src/api/proxy-client';
import type { PasskeyPlatform } from '../src/hub/platform/passkeys';
import { claudeConnectionDetails, isClaudeRoute } from './claude';
import { actions, button, disclosure, el, field, input, submit, type UIContext } from './dom';
import { assertPasskey } from './passkeys';

const chatGPTDeviceURL = 'https://auth.openai.com/codex/device';

async function freshAuth(state: ProxyState, api: ProxyClient, platform: PasskeyPlatform) {
  if (state.auth === 'passkey') await assertPasskey(api, platform, 'reauth');
}

function chatGPTSignIn(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  platform: PasskeyPlatform,
  close: () => void,
  route?: ProxyRoute,
): HTMLElement {
  const view = el('div');
  view.className = 'provider-onboarding';
  const name = input();
  name.placeholder = 'chatgpt';
  name.pattern = '[A-Za-z0-9][A-Za-z0-9_-]{0,63}';
  name.maxLength = 64;
  const form = el(
    'form',
    el(
      'p',
      route
        ? 'Sign in again to renew the account used by this route. Existing client access stays in place.'
        : 'Sign in with your ChatGPT subscription. The proxy keeps the login; you decide who can use the route after setup.',
    ),
  );
  if (!route) {
    form.append(disclosure('Route name (optional)', field('Route name', name)));
  }
  const cancelSetup = button('Cancel', close);
  form.append(cancelSetup);
  let active: string | undefined;

  async function cancel(id: string) {
    if (active !== id) return;
    active = undefined;
    await api.cancelChatGPTSetup(id);
  }

  function renderPrompt(signIn: ProxyChatGPTDeviceSignIn) {
    const code = input(signIn.user_code);
    code.readOnly = true;
    const copy = button('Copy code', () => {
      void context.run(async () => {
        try {
          await navigator.clipboard.writeText(code.value);
          context.announce('ChatGPT sign-in code copied.');
        } catch {
          code.focus();
          code.select();
          context.announce('Copy the selected code manually.');
        }
      }, copy);
    });
    const link = el('a', 'Continue to OpenAI');
    link.href = chatGPTDeviceURL;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    const back = button('Cancel sign-in', () => {
      void context.run(async () => {
        try {
          await cancel(signIn.id);
        } finally {
          close();
        }
      }, back);
    });
    const waiting = el('p', 'Waiting for you to finish signing in at OpenAI…');
    waiting.role = 'status';
    view.replaceChildren(
      el('p', 'Open OpenAI, enter this code, and approve the sign-in. Then return here.'),
      field('One-time sign-in code', code),
      actions(copy, link, back),
      waiting,
    );
    // Let the submit action settle before moving focus from its now-detached button.
    link.focus();
  }

  function poll(id: string) {
    window.setTimeout(() => {
      if (active !== id) return;
      if (!view.isConnected) {
        void cancel(id).catch(() => {}); // Abandoned attempts also expire on the server.
        return;
      }
      let ran = false;
      void context
        .run(async () => {
          ran = true;
          const result = await api.chatGPTSetupStatus(id);
          if (active !== id || !view.isConnected) return;
          if (result.status === 'pending') {
            poll(id);
            return;
          }
          active = undefined;
          if (result.status === 'failed') {
            view.replaceChildren(form);
            const message =
              result.error ===
              'Sign in to the original ChatGPT account and workspace, or create a separate route'
                ? result.error
                : 'ChatGPT sign-in did not finish. Start again to get a fresh code.';
            throw new Error(message);
          }
          const message = route
            ? 'ChatGPT reconnected. Existing client access is unchanged.'
            : 'ChatGPT route created. Grant access when you’re ready to use it.';
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
            poll(id);
            return;
          }
          if (!ok && active === id && view.isConnected) {
            void cancel(id).catch(() => {});
            view.replaceChildren(form);
          }
        });
    }, 2000);
  }

  submit(
    form,
    context,
    async () => {
      await freshAuth(state, api, platform);
      const requested = name.value.trim();
      const signIn = await api.startChatGPTSetup(
        route ? { account: route.account } : requested ? { name: requested } : {},
      );
      active = signIn.id;
      if (!view.isConnected) {
        await cancel(signIn.id);
        return;
      }
      if (signIn.verification_url !== chatGPTDeviceURL) {
        await cancel(signIn.id);
        throw new Error('Unexpected ChatGPT sign-in destination.');
      }
      renderPrompt(signIn);
      poll(signIn.id);
    },
    route ? 'Reconnect ChatGPT' : 'Sign in to ChatGPT',
  );
  view.append(form);
  return view;
}

function claudeLauncherSetup(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  platform: PasskeyPlatform,
  route?: ProxyRoute,
): HTMLElement {
  const view = el('div');
  view.className = 'provider-onboarding';
  const create = button(route ? 'Create device launcher' : 'Create route & get launcher', () => {
    if (context.hasProtectedToken?.()) {
      context.announce('Save or dismiss the open client token before adding another device.');
      return;
    }
    void context.run(async () => {
      context.protectToken(true, true);
      let issued = false;
      try {
        await freshAuth(state, api, platform);
        const connection = route
          ? await api.setupClaude(state.revision, route.name)
          : await api.setupClaude(state.revision);
        const details = claudeConnectionDetails(connection, context);
        const dismiss = button('I saved it — dismiss', () => {
          void context.run(async () => {
            await context.refresh(route?.name ?? 'claude');
            details.querySelectorAll('textarea').forEach((text) => (text.value = ''));
            context.protectToken(false);
          }, dismiss);
        });
        issued = true;
        context.protectToken(true);
        view.replaceChildren(
          el('p', 'Shown once. Save this launcher securely; its token cannot be retrieved later.'),
          details,
          dismiss,
        );
        view.querySelector('textarea')?.focus();
      } finally {
        if (!issued) context.protectToken(false);
      }
    }, create);
  });
  create.className = 'primary';
  view.append(
    el(
      'p',
      'Run the launcher on your device and sign in inside Claude Code. Your usual Claude setup stays unchanged.',
    ),
    create,
  );
  return view;
}

export function providerOnboarding(
  preset: ProxyPreset,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  platform: PasskeyPlatform,
  close: () => void,
): HTMLElement | undefined {
  if (preset.id === 'chatgpt') return chatGPTSignIn(state, api, context, platform, close);
  if (preset.id === 'claude-code') return claudeLauncherSetup(state, api, context, platform);
}

export function providerRouteAction(
  route: ProxyRoute,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  platform: PasskeyPlatform,
): HTMLElement | undefined {
  if (state.mode === 'file') return;
  if (isClaudeRoute(route, state)) {
    if (!route.enabled) return;
    return disclosure('Add device', claudeLauncherSetup(state, api, context, platform, route));
  }
  const account = (state.accounts ?? []).find((value) => value.name === route.account);
  if (account?.kind !== 'chatgpt') return;
  const details = disclosure(account.state === 'ready' ? 'Reconnect' : 'Sign-in needed');
  details.append(chatGPTSignIn(state, api, context, platform, () => (details.open = false), route));
  return details;
}

export function clearClaudeSetup(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  platform: PasskeyPlatform,
): HTMLButtonElement {
  const clear = button('Clear all Claude setup', () => {
    if (context.hasProtectedToken?.()) {
      context.announce('Save or dismiss the open client token before clearing Claude setup.');
      return;
    }
    if (
      !window.confirm(
        'Remove all Claude proxy routes, device access, and saved proxy sign-ins? Other providers and your normal Claude setup are unchanged.',
      )
    )
      return;
    void context.run(async () => {
      await freshAuth(state, api, platform);
      await api.clearClaude(state.revision);
      await context.refresh();
      context.announce('Claude proxy setup cleared. Other providers are unchanged.');
    }, clear);
  });
  clear.className = 'danger';
  return clear;
}
