import {
  type ProxyClaudeConnection,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { button, disclosure, el, field, shellQuote, type UIContext } from './dom';

// Classify by the account mechanism, never by a user-chosen route name or URL.
export function isClaudeRoute(route: ProxyRoute, state: ProxyState): boolean {
  return (
    !!route.account &&
    (state.accounts ?? []).some(
      (account) => account.name === route.account && account.kind === 'claude-code',
    )
  );
}
export function claudeLauncher(url: string, token: string): string {
  return `term-llm proxy run claude --url ${shellQuote(url)} --token ${shellQuote(token)}`;
}
export function claudeProviderYAML(url: string, token: string): string {
  const literal = (value: string) => `'${value.replaceAll("'", "''")}'`;
  return `providers:\n  claude-bin:\n    proxy:\n      url: ${literal(url)}\n      token: ${literal(token)}\n`;
}

// Both the dedicated flow and generic Clients use the same native connection examples.
export function claudeConnectionDetails(
  connection: Pick<ProxyClaudeConnection, 'url' | 'token'>,
  context: UIContext,
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
    if (label === 'Launcher command') copy.className = 'primary';
    return el('div', field(label, text), copy);
  }
  return el(
    'div',
    el('p', 'Run this command, then sign in normally in Claude.'),
    copyField(
      'Launcher command',
      claudeLauncher(connection.url, connection.token),
      'Copy launcher command',
      3,
    ),
    disclosure(
      'Optional term-llm integration',
      copyField(
        'Provider YAML',
        claudeProviderYAML(connection.url, connection.token),
        'Copy provider YAML',
        7,
      ),
    ),
    el(
      'small',
      'Uses a scoped proxy token, not a Claude credential. Your normal Claude setup stays unchanged.',
    ),
  );
}
