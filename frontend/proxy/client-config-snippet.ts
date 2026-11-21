import type { ProviderGroup } from './provider-groups';
import { actions, button, el, field, type UIContext } from './dom';

const DEFAULT_TOKEN_REF = 'file:///run/secrets/proxy-client-token';
const SAFE_YAML_SCALAR = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

function yamlSingleQuote(value: string): string {
  return `'${value.replaceAll("'", "''")}'`;
}

function yamlProviderID(id: string): string {
  return SAFE_YAML_SCALAR.test(id) ? id : yamlSingleQuote(id);
}

/**
 * Formats a top-level term-llm `proxy:` block for ~/.config/term-llm/config.yaml.
 * Omit `token` for display so the raw secret never appears in DOM textContent;
 * pass `token` only when building an explicit clipboard payload.
 */
export function termLLMProxyYAML(
  origin: string,
  providerIDs?: readonly string[],
  token?: string,
): string {
  const ids = [...new Set(providerIDs?.filter(Boolean))];
  const tokenLine = token === undefined ? DEFAULT_TOKEN_REF : yamlSingleQuote(token);
  const lines = [`proxy:`, `  url: ${origin}`, `  token: ${tokenLine}`];
  if (providerIDs !== undefined) lines.push(`  providers: [${ids.map(yamlProviderID).join(', ')}]`);
  return lines.join('\n');
}

function copyButton(
  label: string,
  context: UIContext,
  options: {
    getText: () => string | undefined;
    copiedAnnouncement: string;
    emptyAnnouncement?: string;
    onFallback: () => void;
    unavailableAnnouncement: string;
    failedAnnouncement: string;
    primary?: boolean;
  },
): HTMLButtonElement {
  const node = button(label, () => {
    void context.run(async () => {
      const value = options.getText();
      if (!value) {
        if (options.emptyAnnouncement) context.announce(options.emptyAnnouncement);
        return;
      }
      if (!navigator.clipboard) {
        options.onFallback();
        context.announce(options.unavailableAnnouncement);
        return;
      }
      try {
        await navigator.clipboard.writeText(value);
        node.textContent = 'Copied ✓';
        setTimeout(() => {
          if (node.isConnected) node.textContent = label;
        }, 2000);
        context.announce(options.copiedAnnouncement);
      } catch {
        options.onFallback();
        context.announce(options.failedAnnouncement);
      }
    }, node);
  });
  node.setAttribute('aria-label', label);
  if (options.primary) node.className = 'primary';
  return node;
}

/** Pairs the one-time Client token input with an aligned Copy token button. */
export function tokenCopyRow(token: HTMLInputElement, context: UIContext): HTMLElement {
  const selectToken = () => {
    token.focus();
    token.select();
  };
  const copy = copyButton('Copy token', context, {
    getText: () => token.value,
    copiedAnnouncement: 'Client token copied. Store it safely before dismissing.',
    onFallback: selectToken,
    unavailableAnnouncement: 'Clipboard unavailable. Copy the selected token manually.',
    failedAnnouncement: 'Copy unavailable. Copy the selected token manually.',
    primary: true,
  });
  const row = el('div', field('Client token', token), copy);
  row.className = 'token-copy-row';
  return row;
}

/**
 * Easy-to-copy term-llm config.yaml snippet shown after issuing a client token.
 * The visible `<pre><code>` always uses a file reference placeholder so the
 * secret is never duplicated into DOM textContent; `Copy with token` reads the
 * live input on click and formats a ready-to-paste block into the clipboard.
 */
export function termLLMConfigSection(
  origin: string,
  groups: readonly ProviderGroup[],
  context: UIContext,
  options: {
    heading?: 'h3' | 'h4';
    getToken?: () => string;
    onTokenFallback?: () => void;
  } = {},
): HTMLElement | undefined {
  const ids = [...new Set(groups.map((group) => group.id).filter(Boolean))];
  if (!ids.length) return undefined;

  // Omit providers so the client discovers every granted, advertised integration,
  // including classifiers. An explicit list remains available via termLLMProxyYAML.
  const listed = undefined;
  const safeYAML = termLLMProxyYAML(origin, listed);
  const code = el('code', safeYAML);
  const pre = el('pre', code);

  const selectCode = () => {
    const selection = window.getSelection();
    if (!selection) return;
    const range = document.createRange();
    range.selectNodeContents(code);
    selection.removeAllRanges();
    selection.addRange(range);
  };

  const copySafe = copyButton('Copy YAML', context, {
    getText: () => safeYAML,
    copiedAnnouncement:
      'term-llm config.yaml snippet copied. Point token at the saved client token.',
    onFallback: selectCode,
    unavailableAnnouncement: 'Clipboard unavailable. Copy the selected YAML manually.',
    failedAnnouncement: 'Copy unavailable. Copy the selected YAML manually.',
  });

  const headerActions = actions(copySafe);

  if (options.getToken) {
    const readToken = options.getToken;
    const copyReady = copyButton('Copy with token', context, {
      getText: () => {
        const value = readToken();
        return value ? termLLMProxyYAML(origin, listed, value) : undefined;
      },
      copiedAnnouncement:
        'term-llm config.yaml snippet with client token copied. Keep config.yaml private.',
      emptyAnnouncement: 'Client token is no longer available.',
      onFallback: () => options.onTokenFallback?.(),
      unavailableAnnouncement: 'Clipboard unavailable. Copy the YAML and client token manually.',
      failedAnnouncement: 'Copy unavailable. Copy the YAML and client token manually.',
    });
    headerActions.append(copyReady);
  }

  const head = el(
    'div',
    el(options.heading ?? 'h3', 'term-llm client configuration'),
    headerActions,
  );
  head.className = 'config-snippet-head';

  const mergeIDs = ids.map(yamlProviderID).join(', ');
  const providerHint = `If you already have a proxy.providers list, keep it and add ${mergeIDs}. Without a list, the client discovers providers approved for its token, but unapproved integrations may be unavailable, including local ones like Ollama. To keep specific integrations direct without a list, use proxy.direct.`;
  const tokenHint = options.getToken
    ? ' Point token at a private file, ${TERM_LLM_PROXY_TOKEN}, or use Copy with token.'
    : ' Change the file reference to the private file where you saved this client token.';
  const hint = el(
    'p',
    'Paste into ',
    el('code', '~/.config/term-llm/config.yaml'),
    `. ${providerHint}${tokenHint}`,
  );
  hint.className = 'config-snippet-hint';

  const section = el('div', head, hint, pre);
  section.className = 'config-snippet';

  const approvedSet = new Set(ids);
  const seenVision = new Set<string>();
  for (const group of groups) {
    const visionRef = group.config?.vision_via?.trim();
    if (!visionRef) continue;
    const visionProvider = visionRef.split(':')[0];
    if (approvedSet.has(visionProvider)) continue;
    const key = `${group.id}:${visionRef}`;
    if (seenVision.has(key)) continue;
    seenVision.add(key);
    const note = el(
      'p',
      `${groups.length === 1 ? 'This provider' : group.label} routes vision via ${visionRef}. Approve ${visionProvider} if clients use image inputs. If you use an explicit proxy.providers list, add it there too.`,
    );
    note.className = 'config-snippet-note';
    section.append(note);
  }

  return section;
}
