import {
  type ProxyAccount,
  type ProxyNativeConnection,
  type ProxyNativeLauncher,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { button, el, field, shellQuote, type UIContext } from './dom';

export interface NativeProvider {
  kind: 'claude-code' | 'agy-bin';
  configKey: 'claude-bin' | 'agy-bin';
  launcher: ProxyNativeLauncher;
  label: string;
  shortName: string;
  operatorArgs: string;
  setupRequirements?: string;
  operatorSignIn: string;
  credentialHelp: string;
}

export const claudeProvider: NativeProvider = {
  kind: 'claude-code',
  configKey: 'claude-bin',
  launcher: 'claude',
  label: 'Claude Code',
  shortName: 'Claude',
  operatorArgs: '',
  operatorSignIn:
    'Run this command on your own device with an updated term-llm, then sign in to Claude in your browser.',
  credentialHelp:
    'Uses a scoped proxy token, not a Claude credential. Your normal Claude setup stays unchanged.',
};

export const agyProvider: NativeProvider = {
  kind: 'agy-bin',
  configKey: 'agy-bin',
  launcher: 'agy',
  label: 'Antigravity',
  shortName: 'Antigravity',
  operatorArgs: ' -- acp --login',
  setupRequirements:
    'Personal subscriptions only, not Enterprise. Use Linux with agy 1.5.12 or newer. macOS credential isolation has not been verified.',
  operatorSignIn:
    'Run this command on your own Linux device with an updated term-llm and follow the Antigravity prompt to sign in with your personal Google account.',
  credentialHelp:
    'Share only the scoped proxy token, not Google account credentials. Your normal Antigravity setup stays unchanged.',
};

export const nativeProviders = [claudeProvider, agyProvider];

export function nativeProvider(kind?: string): NativeProvider | undefined {
  return nativeProviders.find((provider) => provider.kind === kind);
}

function routeAccount(route: ProxyRoute, state: ProxyState): ProxyAccount | undefined {
  return state.accounts?.find((account) => account.name === route.account);
}

// Classify by the account mechanism, never by a user-chosen route name or URL.
export function routeNativeProvider(
  route: ProxyRoute,
  state: ProxyState,
): NativeProvider | undefined {
  return nativeProvider(routeAccount(route, state)?.kind);
}

export function isSharedNativeAccount(
  account?: ProxyAccount,
): account is ProxyAccount & { auth_mode: 'shared_vault' } {
  return account?.auth_mode === 'shared_vault' && !!nativeProvider(account.kind);
}

export function routeSharedNativeAccount(
  route: ProxyRoute,
  state: ProxyState,
): ProxyAccount | undefined {
  const account = routeAccount(route, state);
  return isSharedNativeAccount(account) ? account : undefined;
}

// Legacy native accounts are never eligible for new device access.
export function routeAllowsClientApproval(route: ProxyRoute, state: ProxyState): boolean {
  const account = routeAccount(route, state);
  return (
    !nativeProvider(account?.kind) ||
    (route.enabled && isSharedNativeAccount(account) && account.state === 'ready')
  );
}

export const legacyNativeNotice =
  'This route must be migrated to a shared subscription before the operator can sign in.';

export type NativeConnectionUse = 'shared-worker' | 'shared-operator';

export function nativeLauncher(
  provider: NativeProvider,
  url: string,
  token: string,
  use: NativeConnectionUse,
): string {
  const args = use === 'shared-operator' ? provider.operatorArgs : '';
  return `term-llm proxy run ${provider.launcher} --url ${shellQuote(url)} --token ${shellQuote(token)}${args}`;
}
export function nativeProviderYAML(provider: NativeProvider, url: string, token: string): string {
  const literal = (value: string) => `'${value.replaceAll("'", "''")}'`;
  return `providers:\n  ${provider.configKey}:\n    proxy:\n      url: ${literal(url)}\n      token: ${literal(token)}\n`;
}

// Both the dedicated flow and generic Clients use the same native connection examples.
export function nativeConnectionDetails(
  provider: NativeProvider,
  connection: Pick<ProxyNativeConnection, 'url' | 'token'>,
  context: UIContext,
  use: NativeConnectionUse,
): HTMLElement {
  function copyField(label: string, value: string, copyLabel: string, rows: number) {
    const text = el('textarea');
    text.value = value;
    text.readOnly = true;
    text.spellcheck = false;
    text.rows = rows;
    const copy = button(copyLabel, () => {
      void context.run(async () => {
        try {
          if (!navigator.clipboard) throw new Error('Clipboard unavailable');
          await navigator.clipboard.writeText(text.value);
          context.announce(`${label} copied. Keep the proxy token private.`);
        } catch {
          text.focus();
          text.select();
          context.announce('Clipboard unavailable. Copy the selected text manually.');
        }
      }, copy);
    });
    if (rows === 3) copy.className = 'primary';
    return el('div', field(label, text), copy);
  }
  const command = nativeLauncher(provider, connection.url, connection.token, use);
  if (use === 'shared-operator')
    return el(
      'div',
      el(
        'p',
        `${provider.operatorSignIn} When sign-in finishes, refresh the Providers page to check that the shared subscription is ready.`,
      ),
      copyField('Operator sign-in launcher command', command, 'Copy operator launcher command', 3),
      el(
        'small',
        'Keep this operator launcher private. Use Add device to give workers their own launcher with separately revocable access.',
      ),
    );
  const integration = el(
    'div',
    el('h4', 'Optional term-llm integration'),
    copyField(
      'Provider YAML',
      nativeProviderYAML(provider, connection.url, connection.token),
      'Copy provider YAML',
      5,
    ),
  );
  integration.className = 'native-integration';
  return el(
    'div',
    el(
      'p',
      `Run this command on the worker with an updated term-llm. It uses the shared ${provider.shortName} sign-in on the proxy and does not open a browser.`,
    ),
    copyField('Launcher command', command, 'Copy launcher command', 3),
    integration,
    el('small', provider.credentialHelp),
  );
}
