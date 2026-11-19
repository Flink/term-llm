import { ProxyAPIError, type ProxyClient, type ProxyState } from '../src/api/proxy-client';
import {
  actions,
  button,
  check,
  confirmAction,
  dateTime,
  el,
  field,
  input,
  keyedCard,
  shellQuote,
  submit,
  type UIContext,
} from './dom';

const focusKey = 'admin-keys';

function examples(revision: number): HTMLElement {
  const base = `${location.origin}/_proxy/manage/v1`;
  const auth = '-H "Authorization: Bearer $TERM_LLM_PROXY_ADMIN_KEY"';
  return el(
    'div',
    el('h3', 'Use an API key from your shell'),
    el(
      'p',
      'Set TERM_LLM_PROXY_ADMIN_KEY to the saved key. List routes first, then use a route name and the revision returned by that request to create a client.',
    ),
    el('pre', `curl ${auth} ${shellQuote(`${base}/routes`)}`),
    el(
      'pre',
      `curl -X POST ${auth} -H 'Content-Type: application/json' \\\n  -d '{"name":"build runner","grants":["example-route"],"revision":${revision}}' \\\n  ${shellQuote(`${base}/clients`)}`,
    ),
  );
}

export function adminKeysView(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  let current = state;
  const name = input();
  name.required = true;
  name.maxLength = 80;
  const form = el('form', field('API key name', name));
  const list = el('div');
  const usage = examples(state.revision);
  const manage = el('div', form, list);
  const oneTime = el('div');
  const view = keyedCard(
    focusKey,
    'API keys',
    el(
      'p',
      'An admin API key can create clients for any grantable route, including routes added later. It can list, change grants for, revoke, or purge only clients created through the API, including clients created by other keys. It cannot manage browser-created clients or other proxy settings, forward traffic, or sign in to the browser UI.',
    ),
    el('p', 'Use admin API keys only over loopback or HTTPS.'),
    oneTime,
    manage,
    usage,
  );

  function renderKeys() {
    list.replaceChildren(el('h3', 'Issued keys'));
    if (!current.admin_keys.length) list.append(el('p', 'No API keys yet.'));
    for (const key of current.admin_keys) {
      const [label, cascade] = check('Also revoke clients created by this key', false);
      const revoke = confirmAction(
        'Revoke…',
        async () => {
          const revokeClients = cascade.checked;
          manage.inert = true;
          try {
            const result = await api.revokeAdminKey(key.id, revokeClients);
            await context.refresh(focusKey);
            const count = result.revoked_clients;
            context.announce(
              `Revoked API key ${key.name}. ${count} ${count === 1 ? 'client' : 'clients'} revoked.`,
            );
          } finally {
            manage.inert = false;
          }
        },
        context,
        `Revoke API key ${key.name}? This cannot be undone. Its clients stay active unless you selected “Also revoke clients created by this key.”`,
      );
      const title = el('h4', key.name);
      title.id = `admin-key-${key.id}`;
      cascade.setAttribute('aria-describedby', title.id);
      revoke.setAttribute('aria-label', `Revoke API key ${key.name}`);
      list.append(
        el(
          'article',
          title,
          el(
            'p',
            `Created ${dateTime(key.created, 'Unknown')} · Last used ${dateTime(key.last_used)}`,
          ),
          label,
          actions(revoke),
        ),
      );
    }
  }

  function showToken(value: string, keyName: string) {
    manage.hidden = true;
    const token = input(value);
    token.readOnly = true;
    token.spellcheck = false;
    token.className = 'caller-token';
    const copy = button('Copy token', () => {
      void context.run(async () => {
        try {
          if (!navigator.clipboard) throw new Error('Clipboard unavailable');
          await navigator.clipboard.writeText(token.value);
          context.announce('API key copied. Store it securely before dismissing.');
        } catch {
          token.focus();
          token.select();
          context.announce('Clipboard unavailable. Copy the selected token manually.');
        }
      }, copy);
    });
    const dismiss = button('I saved it — dismiss', () => {
      void context.run(async () => {
        token.value = '';
        oneTime.replaceChildren();
        context.protectToken(false);
        manage.hidden = false;
        name.focus();
        await context.refresh(focusKey);
        context.announce('The one-time API key token has been dismissed.');
      }, dismiss);
    });
    oneTime.append(
      el('h3', 'Copy this token now'),
      el('p', 'Shown once. Save it securely before dismissing; it cannot be retrieved later.'),
      field('API key token', token),
      actions(copy, dismiss),
    );
    token.focus();
    token.select();
    context.announce(`Created API key ${keyName}. Copy the token now; it is shown once.`);
  }

  submit(
    form,
    context,
    async () => {
      const requested = name.value.trim();
      if (!requested) throw new Error('Enter an API key name.');
      manage.inert = true;
      try {
        context.protectToken(true, true);
        let issued = false;
        try {
          context.announce('Creating API key… wait for the one-time token.');
          const { key, token } = await api.createAdminKey(requested);
          issued = true;
          context.protectToken(true);
          name.value = '';
          showToken(token, key.name);
          current = { ...current, admin_keys: [...current.admin_keys, key] };
          // Reconcile CSRF, metadata and example revision without replacing the token view.
          try {
            const latest = await context.refresh(undefined, true);
            if (latest) {
              current = latest;
              usage.replaceChildren(...examples(current.revision).childNodes);
            }
          } catch (error) {
            if (error instanceof ProxyAPIError && error.status === 401) throw error;
            context.announce(
              'API key created. Save or dismiss the token now; state refresh is unavailable.',
            );
          }
          renderKeys();
        } finally {
          if (!issued) context.protectToken(false);
        }
      } finally {
        manage.inert = false;
      }
    },
    'Create API key',
  );
  renderKeys();
  return view;
}
