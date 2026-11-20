import type {
  ProxyChatGPTDeviceSignIn,
  ProxyClient,
  ProxyPreset,
  ProxyRoute,
  ProxyState,
} from '../src/api/proxy-client';
import {
  legacyNativeNotice,
  nativeConnectionDetails,
  nativeProvider,
  routeNativeProvider,
  routeSharedNativeAccount,
  type NativeProvider,
} from './native';
import { actions, button, disclosure, el, field, input, submit, type UIContext } from './dom';
import { nativeSignInClientForm, type NativeSignInChoice } from './native-signin';

import { requireProviderName, slugPattern } from './provider-name';

const chatGPTDeviceURL = 'https://auth.openai.com/codex/device';

function chatGPTSignIn(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  close: () => void,
  route?: ProxyRoute,
): HTMLElement {
  const view = el('div');
  view.className = 'provider-onboarding';
  const name = input();
  name.placeholder = 'chatgpt';
  name.pattern = slugPattern;
  name.maxLength = 64;
  const form = el(
    'form',
    el(
      'p',
      route
        ? 'Sign in again to renew this provider’s account. Existing client access stays in place.'
        : 'Sign in with your ChatGPT subscription. The proxy keeps the login; you approve clients separately.',
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
          if (route) view.replaceChildren(form);
          else close();
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
            : 'ChatGPT connected. Approve clients for ChatGPT when you’re ready.';
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
      const requested = name.value.trim();
      if (!route && requested) requireProviderName(requested, state);
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

type NativeIssue =
  | [use: 'shared-operator', route: ProxyRoute | undefined, choice: NativeSignInChoice]
  | [use: 'shared-worker', route: ProxyRoute];

function issueNativeLauncher(
  view: HTMLElement,
  provider: NativeProvider,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  source: HTMLButtonElement,
  onDismiss: () => void,
  route: ProxyRoute,
) {
  if (context.hasProtectedToken?.()) {
    context.announce(
      'Save or dismiss the open launcher or client token before creating another launcher.',
    );
    return;
  }
  void context.run(
    () =>
      issueNativeConnection(view, provider, state, api, context, onDismiss, 'shared-worker', route),
    source,
  );
}

// Runs inside the caller's context.run. Arming an existing client issues no
// token, so there is nothing to protect.
async function issueNativeConnection(
  view: HTMLElement,
  provider: NativeProvider,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  onDismiss: () => void,
  ...[use, route, choice]: NativeIssue
): Promise<void> {
  const mintsToken = !choice || 'name' in choice.client;
  if (mintsToken) context.protectToken(true, true);
  let issued = false;
  try {
    const connection = !choice
      ? await api.setupNative(provider.launcher, state.revision, route!.name)
      : route
        ? await api.setupNative(
            provider.launcher,
            state.revision,
            route.name,
            'connect',
            choice.client,
          )
        : await api.setupNative(
            provider.launcher,
            state.revision,
            undefined,
            'create',
            choice.client,
          );
    const details = nativeConnectionDetails(provider, connection, context, use, choice?.label);
    const dismiss = button(mintsToken ? 'I saved it — dismiss' : 'Done', () => {
      void context.run(async () => {
        await context.refresh(route?.name ?? provider.launcher);
        details.querySelectorAll('textarea').forEach((text) => (text.value = ''));
        if (mintsToken) context.protectToken(false);
        onDismiss();
      }, dismiss);
    });
    issued = true;
    if (mintsToken) context.protectToken(true);
    view.replaceChildren(
      el(
        'p',
        mintsToken
          ? 'Shown once. Save this launcher securely; its token cannot be retrieved later.'
          : `${choice!.label} keeps its existing token; this command does not contain it.`,
      ),
      details,
      dismiss,
    );
    view.querySelector('textarea')?.focus();
  } finally {
    if (mintsToken && !issued) context.protectToken(false);
  }
}

// Shared sign-in first asks which client to arm, then shows its command.
function startNativeSignIn(
  view: HTMLElement,
  provider: NativeProvider,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  route: ProxyRoute | undefined,
  restore: () => void,
) {
  if (context.hasProtectedToken?.()) {
    context.announce('Save or dismiss the open launcher or client token before signing in.');
    return;
  }
  const form = nativeSignInClientForm(
    provider,
    state,
    context,
    route,
    (choice) =>
      issueNativeConnection(
        view,
        provider,
        state,
        api,
        context,
        restore,
        'shared-operator',
        route,
        choice,
      ),
    restore,
  );
  view.replaceChildren(form);
  form.querySelector('select')?.focus();
}

function nativeLauncherSetup(
  provider: NativeProvider,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  const view = el('div');
  view.className = 'provider-onboarding';
  const render = () => {
    const create = button('Create shared route & sign in', () =>
      startNativeSignIn(view, provider, state, api, context, undefined, render),
    );
    create.className = 'primary';
    view.replaceChildren(
      el(
        'p',
        'Sign in once from one of your own clients. Authorized devices use your subscription and its limits without signing in themselves. Only grant access to devices you trust.',
      ),
    );
    if (provider.setupRequirements) view.append(el('p', provider.setupRequirements));
    view.append(create);
  };
  render();
  return view;
}

export function providerOnboarding(
  preset: ProxyPreset,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  close: () => void,
): HTMLElement | undefined {
  if (preset.id === 'chatgpt') return chatGPTSignIn(state, api, context, close);
  const provider = nativeProvider(preset.id);
  if (!provider) return;
  return nativeLauncherSetup(provider, state, api, context);
}

export function providerRouteAction(
  route: ProxyRoute,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement | undefined {
  if (state.mode === 'file') return;
  const provider = routeNativeProvider(route, state);
  if (provider) {
    const shared = routeSharedNativeAccount(route, state);
    if (!shared) return el('p', legacyNativeNotice);
    if (!route.enabled) return;
    const view = el('div');
    view.className = 'provider-onboarding provider-native-actions';
    const ready = shared.state === 'ready';
    const needsLogin = shared.state === 'needs_login';
    const help = ready
      ? 'Create a separately revocable device launcher for each worker, or reconnect the shared sign-in.'
      : needsLogin
        ? 'Sign in to the shared subscription from one of your own clients, not a worker.'
        : 'Shared sign-in is refreshing. You can reconnect from one of your own clients if needed.';
    const renderControls = () => {
      const parts: HTMLElement[] = [el('p', help)];
      if (provider.setupRequirements) {
        const req = el('p', provider.setupRequirements);
        req.className = 'provider-note';
        parts.push(req);
      }
      const buttons: HTMLButtonElement[] = [];
      if (ready) {
        const addDevice = button('Add device', () =>
          issueNativeLauncher(
            view,
            provider,
            state,
            api,
            context,
            addDevice,
            renderControls,
            route,
          ),
        );
        addDevice.className = 'primary';
        buttons.push(addDevice);
      }
      const operator = button(needsLogin ? 'Sign in to shared subscription' : 'Reconnect', () =>
        startNativeSignIn(view, provider, state, api, context, route, renderControls),
      );
      if (needsLogin) operator.className = 'primary';
      buttons.push(operator);
      parts.push(actions(...buttons));
      view.replaceChildren(...parts);
    };
    renderControls();
    return view;
  }
  const account = (state.accounts ?? []).find((value) => value.name === route.account);
  if (account?.kind !== 'chatgpt') return;
  const view = chatGPTSignIn(state, api, context, () => {}, route);
  if (account.state !== 'ready') {
    const submitButton = view.querySelector<HTMLButtonElement>('button[type=submit]');
    if (submitButton) submitButton.className = 'primary';
  }
  return view;
}

export function clearNativeSetup(
  provider: NativeProvider,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLButtonElement {
  const clear = button(`Clear all ${provider.shortName} setup`, () => {
    if (context.hasProtectedToken?.()) {
      context.announce(
        `Save or dismiss the open client token before clearing ${provider.shortName} setup.`,
      );
      return;
    }
    if (
      !window.confirm(
        `Remove all ${provider.shortName} proxy routes, device access, and saved proxy sign-ins? Other providers and your normal ${provider.shortName} setup are unchanged.`,
      )
    )
      return;
    void context.run(async () => {
      await api.clearNative(provider.launcher, state.revision);
      await context.refresh();
      context.announce(`${provider.shortName} proxy setup cleared. Other providers are unchanged.`);
    }, clear);
  });
  clear.className = 'danger';
  return clear;
}
