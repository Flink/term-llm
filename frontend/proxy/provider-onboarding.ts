import type { ProxyClient, ProxyPreset, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import {
  legacyNativeNotice,
  nativeConnectionDetails,
  nativeProvider,
  routeNativeProvider,
  routeSharedNativeAccount,
  type NativeProvider,
} from './native';
import { actions, button, el, type UIContext } from './dom';
import { nativeSignInClientForm, type NativeSignInChoice } from './native-signin';

import { deviceSignIn, isDeviceProvider } from './device-signin';

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
        await context.refresh(route ? `route:${route.name}` : `native:${provider.launcher}`);
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
  if (isDeviceProvider(preset.id)) return deviceSignIn(preset.id, state, api, context, close);
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
  const provider = routeNativeProvider(route, state);
  if (provider) {
    if (state.mode === 'file') return;
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
  if (!account || !isDeviceProvider(account.kind)) return;
  // Older UI-managed installs offered reconnect without capability metadata.
  const legacyReady =
    account.device_reconnect === undefined && state.mode === 'ui' && account.state === 'ready';
  if (account.device_reconnect !== 'eligible' && !legacyReady) {
    if (account.state !== 'needs_login') return;
    if (account.device_reconnect === 'custom_settings') {
      return el(
        'p',
        'This account uses custom settings. Use Replace credentials to renew the same account.',
      );
    }
    if (account.device_reconnect !== 'missing_identity') {
      return el('p', 'Device reconnect eligibility is unknown. Check with the proxy operator.');
    }
    const recovery =
      state.mode === 'file'
        ? 'Ask the operator to configure a new provider and approve clients for it again.'
        : 'Create a new provider and approve clients for it again.';
    return el('p', `The original account identity is unavailable. ${recovery}`);
  }
  const view = deviceSignIn(account.kind, state, api, context, () => {}, route);
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
