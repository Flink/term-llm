import { nativeProvider } from './native';
import { accountDialog, accountStateLabel } from './accounts';
import { secretForm } from './security';
import { presetLabel } from './route-presets';
import { companionRouteName } from './route-companions';
import type { ProviderGroup } from './provider-groups';
import { requireProviderName, slugPattern } from './provider-name';
import {
  ProxyAPIError,
  type ProxyClient,
  type ProxyBilling,
  type ProxyPreset,
  type ProxyRoute,
  type ProxyRouteRule,
  type ProxyState,
} from '../src/api/proxy-client';
import {
  authLabel,
  routeAuthority as authority,
  ruleCredentialHeader,
  ruleNeedsSecret,
} from './route-rules';
import {
  actions,
  button,
  card,
  check,
  confirmAction,
  disclosure,
  el,
  field,
  input,
  select,
  submit,
  type UIContext,
} from './dom';

export function routeForm(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  close: () => void,
  route?: ProxyRoute,
  preset?: ProxyPreset,
  provider?: ProviderGroup,
): HTMLElement {
  const defaults = route ? undefined : preset;
  const suggestedName = defaults && provider ? companionRouteName(provider, defaults, state) : '';
  const inheritedSecret = provider?.routes
    .flatMap((r) => Object.values(r.set_headers))
    .find((header) => header.secret)?.secret;
  const inheritedAccount = provider?.routes.find((r) => r.account)?.account;

  const presetRules = defaults?.rules ?? [];
  const rulesMode = !!route?.rules?.length || presetRules.length > 0;
  // Existing rule routes load their saved rules verbatim. Presets only supply
  // creation defaults; an existing single-upstream route is never converted.
  const initialRules: ProxyRouteRule[] = route?.rules?.length
    ? route.rules.map((rule) => ({
        ...rule,
        caller_auth: { ...rule.caller_auth },
        set_headers: { ...rule.set_headers },
      }))
    : presetRules.map((rule) => ({
        path_prefix: rule.path_prefix,
        upstream: rule.upstream,
        allow: rule.allow,
        caller_auth: { ...rule.auth },
        set_headers: rule.auth.header
          ? { [rule.auth.header]: { secret: '', prefix: rule.auth.prefix } }
          : {},
      }));
  const initial = route ?? {
    name: suggestedName,
    local_prefix: suggestedName ? `/${suggestedName}/` : '',
    upstream: preset?.upstream ?? '',
    // A unified route authenticates every request through the selected rule's
    // caller_auth; the backend rejects a parent caller_auth when rules exist.
    caller_auth: rulesMode
      ? { header: '', prefix: '' }
      : preset?.auth.header
        ? preset.auth
        : { header: 'Authorization', prefix: 'Bearer ' },
    // A trusted account supplies the upstream credential itself; never invent
    // a static secret substitution beside it. Rule routes keep credentials on
    // their rules, and one shared key replaces them all.
    set_headers:
      !rulesMode && preset?.auth.header && !preset.account_kind
        ? { [preset.auth.header]: { secret: inheritedSecret ?? '', prefix: preset.auth.prefix } }
        : {},
  };
  const form = el('form');
  const formRoot = card(
    route ? `Edit ${route.name}` : preset ? `Set up ${presetLabel(preset)}` : 'Custom endpoint',
    form,
  );
  form.className = 'route-form';
  const name = input(initial.name);
  name.required = true;
  name.readOnly = !!route;
  name.pattern = slugPattern;
  name.maxLength = 64;
  const local = input(initial.local_prefix);
  local.required = true;
  let prefixEdited = !!route;
  local.addEventListener('input', () => {
    prefixEdited = true;
  });
  name.addEventListener('input', () => {
    if (!prefixEdited) local.value = name.value.trim() ? `/${name.value.trim()}/` : '';
  });
  const upstream = input(initial.upstream);
  upstream.required = !rulesMode;
  const header = input(initial.caller_auth.header);
  header.required = true;
  const prefix = input(initial.caller_auth.prefix);
  const allow = allowInput(route?.allow ?? defaults?.allow);
  const requestID = input(route?.request_id_header);
  const billing = select(
    [
      ['unspecified', 'Not specified'],
      ['pay_as_you_go', 'Pay as you go'],
      ['plan', 'Subscription'],
      ['local', 'Local'],
    ],
    route?.billing || defaults?.billing || 'unspecified',
  );
  // Offered only where the route needs custody: a new account-kind provider,
  // or an existing route that already names an account.
  const currentAccount = (state.accounts ?? []).find((a) => a.name === route?.account);
  const accountKind = route ? currentAccount?.kind : preset?.account_kind;
  const needsAccount = !!(route?.account || preset?.account_kind);
  const candidates = (state.accounts ?? []).filter((a) => a.kind === accountKind);
  const accountOptions: [string, string][] = candidates.map((a) => [
    a.name,
    `${a.name} · ${accountStateLabel(a)}`,
  ]);
  if (route?.account && !currentAccount)
    accountOptions.unshift([route.account, `${route.account} · unavailable`]);
  const account = select(
    [['', 'Choose account'], ...accountOptions],
    route?.account ?? inheritedAccount ?? '',
  );
  account.required = needsAccount;
  account.disabled = !!provider && !!inheritedAccount;
  const websocket = check(
    'Forward WebSocket upgrades',
    route ? !!route.websocket : !!preset?.websocket,
  );
  const usage = select(
    [
      ['off', 'Off'],
      ['auto', 'Automatic'],
    ],
    (route ? route.usage : preset?.usage) || 'off',
  );
  const [enabledLabel, enabled] = check('Enabled', route?.enabled ?? true);
  const [httpLabel, http] = check(
    'Allow insecure HTTP upstream (credentials may travel unencrypted)',
    route?.allow_insecure_http ?? false,
  );
  httpLabel.classList.add('insecure-option');
  type SubpathRow = {
    source: ProxyRouteRule;
    path: HTMLInputElement;
    upstream: HTMLInputElement;
    allow: HTMLTextAreaElement;
  };
  const ruleRows: SubpathRow[] = [];
  const rulesSummary = el('ul');
  rulesSummary.className = 'route-rule-summary';
  const ruleEditors = el('details');
  ruleEditors.className = 'route-rule-editors';
  const rulesBlock = el('fieldset', el('legend', 'Subpath rules'));
  rulesBlock.className = 'route-rules';
  if (rulesMode) {
    ruleEditors.append(
      el('summary', 'Edit subpath rules'),
      el('small', 'Each subpath forwards to its own upstream base URL.'),
    );
    initialRules.forEach((rule, index) => {
      const path = input(rule.path_prefix);
      path.required = true;
      const ruleUpstream = input(rule.upstream);
      ruleUpstream.required = true;
      const ruleAllow = allowInput(rule.allow);
      const node = el(
        'fieldset',
        el('legend', `Subpath rule ${index + 1}`),
        field('Subpath', path, 'For example /messages/. Must start and end with /.'),
        field('Upstream base URL', ruleUpstream),
        allowField(ruleAllow),
      );
      node.className = 'subpath-rule';
      ruleEditors.append(node);
      ruleRows.push({ source: rule, path, upstream: ruleUpstream, allow: ruleAllow });
    });
    const covers =
      ruleRows.length === 3
        ? 'all three protocol paths'
        : ruleRows.length === 1
          ? 'this protocol path'
          : `all ${ruleRows.length} protocol paths`;
    rulesBlock.append(
      el(
        'p',
        `${initialRules.some(ruleNeedsSecret) ? 'One key and one client grant cover' : 'One client grant covers'} ${covers}.`,
      ),
      rulesSummary,
      ruleEditors,
    );
  }
  const protocolDetails = el('ul');
  protocolDetails.className = 'route-protocol-details';
  // Inside Manage, avoid nested disclosures: render route fields directly.
  const embedded = !!route || !!provider;
  const advancedContent = [
    el(
      'p',
      'Names are permanent (letters, numbers, - or _). Local prefixes must be unique non-root paths ending in /. Upstream URLs cannot include credentials, a query or a fragment.',
    ),
    el(
      'p',
      'Billing categorizes future records only; it does not change provider charges, guarantee included usage or set a spending limit. Home price cards match the route name.',
    ),
    websocket[0],
    field(
      'Usage measurement',
      usage,
      'Automatic reads reported token counts and model names from uncompressed responses for spending estimates; no content is stored by usage measurement.',
    ),
    field('Upstream request ID header (optional)', requestID),
  ];
  const advanced: HTMLElement = embedded
    ? el('div', el('h4', 'Advanced settings'), ...advancedContent)
    : disclosure('Advanced', ...advancedContent);
  if (embedded) advanced.className = 'route-form-advanced';
  if (!rulesMode)
    advanced.append(
      allowField(allow),
      field('Local client authentication header', header),
      field(
        'Local client authentication prefix',
        prefix,
        'Include the trailing space, e.g. "Bearer ". Authenticates proxy clients only.',
      ),
    );
  const coreGrid = el('div', field('Name', name));
  coreGrid.className = 'route-form-grid';
  const technical = preset ? advanced : coreGrid;
  technical.append(field('Local prefix', local));
  if (!rulesMode) technical.append(field('Upstream base URL', upstream));
  technical.append(field('Billing', billing));
  if (needsAccount) {
    const accountField = field(
      'Trusted account',
      account,
      candidates.length || !accountKind ? '' : `Connect a ${accountKind} account to continue.`,
    );
    if (accountKind && !nativeProvider(accountKind) && !account.disabled)
      accountField.append(
        button('Connect new account', () =>
          accountDialog({ state, api, context }, formRoot, accountKind, (stored) => {
            const option = el('option', `${stored.name} · ${accountStateLabel(stored)}`);
            option.value = stored.name;
            account.append(option);
            account.value = stored.name;
            previewRoute();
          }),
        ),
      );
    (route ? advanced : coreGrid).append(accountField);
  }
  form.append(coreGrid);
  const credentials = el('div');
  form.append(credentials);
  if (rulesMode) technical.append(rulesBlock);
  (preset ? advanced : form).append(httpLabel);
  if (route) advanced.append(enabledLabel);
  if (preset && !rulesMode && !upstream.value && advanced instanceof HTMLDetailsElement)
    advanced.open = true;
  // Native validation must be able to report errors even on controls inside
  // a closed Advanced panel (or its subpath editor).
  form.addEventListener(
    'invalid',
    (event) => {
      if (!(event.target instanceof HTMLElement)) return;
      for (
        let parent = event.target.parentElement;
        parent && parent !== form;
        parent = parent.parentElement
      ) {
        if (parent instanceof HTMLDetailsElement) parent.open = true;
      }
    },
    true,
  );
  if (rulesMode)
    advanced.append(
      el(
        'p',
        'Changing a subpath mapping, rule upstream or rule credential can remove client grants and require explicit regranting. Dispatched requests may continue.',
      ),
      el(
        'p',
        'Client protocol authentication is per subpath; a unified route has no parent client authentication.',
      ),
      protocolDetails,
    );
  if (preset?.note) advanced.append(el('p', preset.note));
  const substitutions = el('div');
  const headerControls = el('div');
  const preview = el('p');
  preview.className = 'preview';
  type HeaderRow = {
    node: HTMLElement;
    header: HTMLInputElement;
    kind: HTMLSelectElement;
    value: HTMLInputElement;
    secret: HTMLSelectElement;
    savedSecret?: string;
    prefix: HTMLInputElement;
    update: () => void;
  };
  const rows: HeaderRow[] = [];
  const keyRules = ruleRows.filter((row) => ruleNeedsSecret(row.source));
  const sharedSecretSelect = select([['', 'Choose secret']]);
  let sharedSecret =
    keyRules.flatMap((row) => Object.values(row.source.set_headers)).find((header) => header.secret)
      ?.secret ?? '';
  let changedSharedSecret = false;
  const sharedSecretWarning = el('small');
  function refreshSharedSecret() {
    sharedSecretSelect.replaceChildren(
      ...select(
        [
          ['', 'Choose secret'],
          ...state.secrets.map((secret): [string, string] => [secret.name, secret.name]),
        ],
        sharedSecret,
      ).options,
    );
    sharedSecretSelect.value = sharedSecret;
    const names = new Set(
      keyRules.flatMap(({ source }) =>
        Object.values(source.set_headers).flatMap((header) =>
          header.secret ? [header.secret] : [],
        ),
      ),
    );
    sharedSecretWarning.textContent =
      names.size > 1
        ? 'These subpath rules currently use different stored secrets. Selecting a key applies it to all of them.'
        : '';
    sharedSecretWarning.hidden = !sharedSecretWarning.textContent;
  }
  if (rulesMode && keyRules.length) {
    const addKey = button('Add API key', () => {
      openSecretDialog(`${(name.value || 'upstream').slice(0, 56)}-key`, (stored) => {
        sharedSecret = stored;
        changedSharedSecret = true;
        refreshSharedSecret();
        previewRoute();
      });
    });
    const keyBlock = el(
      'fieldset',
      el('legend', 'Upstream key'),
      field('Secret name', sharedSecretSelect, 'Select an existing key or add one.'),
      sharedSecretWarning,
      addKey,
    );
    keyBlock.className = 'route-key';
    credentials.append(keyBlock);
    sharedSecretSelect.addEventListener('change', () => {
      sharedSecret = sharedSecretSelect.value;
      changedSharedSecret = true;
      previewRoute();
    });
  }
  function previewRoute() {
    const upstreams = rulesMode ? ruleRows.map((row) => row.upstream.value) : [upstream.value];
    httpLabel.hidden = !upstreams.some((value) => value.toLowerCase().startsWith('http:'));
    headerControls.hidden = needsAccount && !rows.length;
    if (rulesMode) {
      rulesSummary.replaceChildren(
        ...ruleRows.map((row) =>
          el(
            'li',
            el('code', row.path.value || '/…/'),
            ` → ${row.upstream.value || 'choose a base URL'}`,
          ),
        ),
      );
      protocolDetails.replaceChildren(
        ...ruleRows.map((row) =>
          el('li', `${row.path.value || '/…/'} · client ${authLabel(row.source.caller_auth)}`),
        ),
      );
      const credential = sharedSecret
        ? `shared key ••••`
        : keyRules.length
          ? 'choose a shared key'
          : 'no upstream key';
      preview.textContent = [
        `Client → ${location.origin}${local.value || '/…/'}…`,
        `One client token and one client grant cover ${ruleRows.length} subpath ${ruleRows.length === 1 ? 'rule' : 'rules'}.`,
        ...ruleRows.map(
          (row) =>
            `${row.path.value || '/…/'} → ${row.upstream.value || 'choose a base URL'} · client ${authLabel(row.source.caller_auth)} · ${ruleNeedsSecret(row.source) ? credential : 'no upstream key'}`,
        ),
      ].join('\n');
      return;
    }
    preview.textContent = `Client → ${location.origin}${local.value || '/…/'}… · ${header.value}: ${JSON.stringify(prefix.value)} + <token>\nUpstream ← ${upstream.value || 'choose a base URL'}${needsAccount ? ` · account ${account.value || '(choose one)'}` : ''}${rows.length ? ' · ' + rows.map((r) => `${r.header.value || 'Header'}: ${r.kind.value === 'secret' ? JSON.stringify(r.prefix.value) + ' + ••••' : '(public literal)'}`).join(', ') : needsAccount ? '' : ' · no substitutions'}`;
  }
  // One dialog serves both the legacy single substitution and the shared
  // subpath key; it stores a secret without discarding the route draft.
  function openSecretDialog(base: string, onStored: (name: string) => void) {
    const dialog = el('dialog', el('h2', 'Add API key'));
    dialog.setAttribute('aria-label', 'Add API key');
    const notice = el('p');
    notice.role = 'alert';
    notice.tabIndex = -1;
    const localContext: UIContext = {
      ...context,
      async run(action, source) {
        notice.textContent = '';
        const ok = await context.run(async () => {
          try {
            await action();
          } catch (error) {
            notice.textContent = error instanceof Error ? error.message : 'Could not store secret.';
            throw error;
          }
        }, source);
        if (!ok && dialog.isConnected) notice.focus();
        return ok;
      },
    };
    let suggestion = base;
    for (let n = 2; state.secrets.some((s) => s.name === suggestion); n++)
      suggestion = `${base}-${n}`;
    const form = secretForm(state, api, localContext, {
      name: suggestion,
      cancel: () => dialog.close(),
      stored: async (storedName) => {
        let latest: ProxyState;
        try {
          const refreshed = await context.refresh(undefined, true);
          if (!refreshed) {
            dialog.close();
            return;
          }
          latest = refreshed;
        } catch (error) {
          dialog.close();
          if (error instanceof ProxyAPIError && error.status === 401) throw error;
          context.announce(
            'Secret stored; route not saved. Refresh to reload secrets before saving the route.',
          );
          return;
        }
        const config = (snapshot: ProxyState) =>
          JSON.stringify({
            routes: snapshot.routes.map(({ serves: _serves, ...r }) => r),
            providers: snapshot.providers.map(({ status: _status, reason: _reason, ...p }) => p),
            defaults: snapshot.defaults,
          });
        const unchanged = config(latest) === config(state);
        state = {
          ...state,
          secrets: latest.secrets,
          revision: unchanged ? latest.revision : state.revision,
        };
        onStored(storedName);
        dialog.close();
        context.announce(
          unchanged
            ? 'API key stored and selected. Your route is not saved yet.'
            : 'API key stored. Routes changed elsewhere; refresh before saving this route.',
        );
      },
    });
    dialog.append(form, notice);
    dialog.addEventListener('cancel', (event) => {
      if (form.getAttribute('aria-busy') === 'true') event.preventDefault();
    });
    dialog.addEventListener('close', () => {
      form.reset();
      dialog.remove();
    });
    formRoot.append(dialog);
    dialog.showModal();
    form.querySelector<HTMLInputElement>('input[type=password]')?.focus();
  }
  function addHeader(key = '', config: ProxyRoute['set_headers'][string] = { secret: '' }) {
    const keyInput = input(key);
    keyInput.required = true;
    const kind = select(
      [
        ['secret', 'Stored secret'],
        ['value', 'Literal (not secret)'],
      ],
      config.value !== undefined ? 'value' : 'secret',
    );
    const literal = input(config.value);
    const secret = select([['', 'Choose secret']]);
    let selected = config.secret ?? '';
    const secretPrefix = input(config.prefix);
    const literalField = field(
      'Literal value',
      literal,
      'Public in state and YAML. Never enter credentials here.',
    );
    const secretField = field('Secret name', secret);
    const prefixField = field(
      'Secret prefix',
      secretPrefix,
      'For example "Bearer " (with a trailing space).',
    );
    const warning = el('p');
    warning.className = 'warning';
    const missing = el('div');
    const node = el(
      'fieldset',
      el('legend', 'Upstream header substitution'),
      field('Header name', keyInput),
      field('Source', kind),
      literalField,
      secretField,
      prefixField,
      warning,
      missing,
    );
    node.className = 'header-substitution';
    const primary = !!(
      preset &&
      !needsAccount &&
      key &&
      key.toLowerCase() === preset.auth.header.toLowerCase()
    );
    const credentialBlock = el('fieldset', el('legend', 'Provider API key'));
    if (primary) {
      credentialBlock.className = 'route-key';
      credentialBlock.append(secretField, missing);
      credentials.append(credentialBlock);
      node.append(el('small', 'Select the provider API key above.'));
    }
    const update = () => {
      const matching = state.secrets;
      selected = secret.value || selected;
      secret.replaceChildren(
        ...[
          ['', 'Choose secret'],
          ...matching.map((s) => [s.name, s.name]),
          ...(config.secret && !matching.some((s) => s.name === config.secret)
            ? [[config.secret, `${config.secret} (missing)`]]
            : []),
        ].map(([value, text]) => {
          const option = el('option', text);
          option.value = value;
          return option;
        }),
      );
      secret.value = selected;
      const useSecret = kind.value === 'secret';
      credentialBlock.hidden = !useSecret;
      literalField.hidden = useSecret;
      secretField.hidden = !useSecret;
      prefixField.hidden = !useSecret;
      secret.required = useSecret;
      warning.textContent =
        !useSecret && /authorization|key|token/i.test(keyInput.value)
          ? 'This looks like a credential header. Use a stored secret; literals are public.'
          : '';
      warning.hidden = !warning.textContent;
      missing.replaceChildren();
      if (useSecret)
        missing.append(
          button('Add API key', () =>
            openSecretDialog(`${(name.value || 'upstream').slice(0, 56)}-key`, (stored) => {
              selected = stored;
              secret.value = '';
              rows.forEach((row) => row.update());
            }),
          ),
        );
      previewRoute();
    };
    secret.addEventListener('change', () => {
      selected = secret.value;
    });
    kind.addEventListener('change', update);
    keyInput.addEventListener('input', update);
    const row: HeaderRow = {
      node,
      header: keyInput,
      kind,
      value: literal,
      secret,
      savedSecret: config.secret,
      prefix: secretPrefix,
      update,
    };
    rows.push(row);
    update();
    node.append(
      button('Remove substitution', () => {
        rows.splice(rows.indexOf(row), 1);
        node.remove();
        credentialBlock.remove();
        previewRoute();
      }),
    );
    substitutions.append(node);
  }
  form.addEventListener('input', previewRoute);
  if (rulesMode) {
    for (const row of ruleRows)
      row.upstream.addEventListener('input', () => {
        refreshSharedSecret();
        previewRoute();
      });
    refreshSharedSecret();
  } else {
    for (const [key, value] of Object.entries(initial.set_headers)) addHeader(key, value);
    upstream.addEventListener('input', () => {
      rows.forEach((r) => r.update());
      previewRoute();
    });
    if (!needsAccount || rows.length) {
      headerControls.append(
        el('h3', 'Upstream headers'),
        substitutions,
        button('Add substitution', () => addHeader()),
      );
      (preset || needsAccount ? advanced : form).append(headerControls);
    }
  }
  advanced.append(el('h3', 'Preview'), preview);
  form.append(advanced);
  let clientConfigYAML: HTMLTextAreaElement | undefined;
  if (!route && !provider && !rulesMode) {
    clientConfigYAML = el('textarea');
    clientConfigYAML.className = 'provider-config-yaml';
    clientConfigYAML.rows = 8;
    clientConfigYAML.spellcheck = false;
    clientConfigYAML.placeholder = [
      'type: vllm',
      'model: deepseek-ai/DeepSeek-V4-Flash',
      'context_window: 200000',
      'max_output_tokens: 50000',
      'vllm_thinking_param: thinking',
    ].join('\n');
    const importNotice = el('p');
    importNotice.className = 'provider-config-notice';
    importNotice.hidden = true;
    const importButton = button('Import pasted term-llm YAML', () => {
      void context.run(async () => {
        const raw = clientConfigYAML?.value ?? '';
        if (!raw.trim()) {
          throw new Error('Paste a term-llm provider YAML block into Client config first.');
        }
        const parsed = await api.parseProviderConfig(raw);
        if (parsed.name) {
          name.value = parsed.name;
          if (!prefixEdited) local.value = `/${parsed.name}/`;
        }
        if (parsed.upstream) {
          upstream.value = parsed.upstream;
          if (parsed.upstream.toLowerCase().startsWith('http:')) http.checked = false;
          rows.forEach((r) => r.update());
        }
        if (clientConfigYAML) clientConfigYAML.value = parsed.config_yaml;
        const stripped = parsed.stripped?.length
          ? ` Stripped local-only fields: ${parsed.stripped.join(', ')}.`
          : '';
        importNotice.textContent = `Imported${parsed.name ? ` ${parsed.name}` : ''}.${stripped}${parsed.upstream ? ' Configure or select the upstream API key above.' : ''}`;
        importNotice.hidden = false;
        previewRoute();
        context.announce(importNotice.textContent);
      }, importButton);
    });
    const clientConfigDisclosure = disclosure(
      'Client config (term-llm)',
      field(
        'Provider YAML (optional)',
        clientConfigYAML,
        'Discovered automatically by term-llm clients listing this provider under proxy.providers. Paste a full local term-llm provider block and click Import pasted term-llm YAML to pre-fill name, upstream URL and client config.',
      ),
      importNotice,
      actions(importButton),
    );
    clientConfigDisclosure.className = 'provider-config-create';
    form.append(clientConfigDisclosure);
  }
  if (route) {
    const affected = state.callers
      .filter((c) => !c.revoked && c.grants.includes(route.name))
      .map((c) => c.name);
    form.append(
      el(
        'p',
        `Upstream, credential or transport changes can remove client grants${affected.length ? ` for ${affected.join(', ')}` : ''}. In-flight requests may continue.`,
      ),
    );
  }
  const save = submit(
    form,
    context,
    async () => {
      if (!rulesMode && !isSRVUpstream(upstream.value)) {
        const url = new URL(upstream.value);
        if (
          !['https:', 'http:'].includes(url.protocol) ||
          url.username ||
          url.password ||
          url.hash ||
          url.search
        )
          throw new Error(
            'Use an HTTP(S) upstream URL without credentials, a query or a fragment.',
          );
        if (url.protocol === 'http:' && !http.checked)
          throw new Error('Explicitly allow insecure HTTP, or use HTTPS.');
      }
      if (
        !local.value.startsWith('/') ||
        !local.value.endsWith('/') ||
        local.value === '/' ||
        local.value.includes('//') ||
        /[%\\\s]/.test(local.value) ||
        local.value.split('/').some((s) => s === '.' || s === '..')
      )
        throw new Error(
          'Use a canonical non-root local path ending in /, without escapes or dot segments.',
        );
      if (
        ['/_proxy/', '/healthz/', '/assets/'].some(
          (p) => local.value.startsWith(p) || p.startsWith(local.value),
        )
      )
        throw new Error('That local prefix is reserved for proxy administration or health.');
      if (
        state.routes.some(
          (r) =>
            r !== route &&
            (r.local_prefix.startsWith(local.value) || local.value.startsWith(r.local_prefix)),
        )
      )
        throw new Error('Local prefixes cannot overlap another route. Choose a distinct path.');
      if (state.routes.some((r) => r !== route && r.name === name.value.trim()))
        throw new Error('Route names must be unique.');
      const setHeaders: ProxyRoute['set_headers'] = Object.create(null);
      const ruleHeaders = new Map<SubpathRow, ProxyRoute['set_headers']>();
      if (rulesMode) {
        const seen = new Set<string>();
        for (const row of ruleRows) {
          const srv = isSRVUpstream(row.upstream.value);
          const url = srv ? undefined : new URL(row.upstream.value);
          if (
            !srv &&
            (!['https:', 'http:'].includes(url!.protocol) ||
              url!.username ||
              url!.password ||
              url!.hash ||
              url!.search)
          )
            throw new Error(
              'Use an HTTP(S) upstream URL or valid SRV reference for every subpath rule, without credentials, a query or a fragment.',
            );
          if (url?.protocol === 'http:' && !http.checked)
            throw new Error('Explicitly allow insecure HTTP, or use HTTPS for every subpath rule.');
          const subpath = row.path.value;
          if (
            !subpath.startsWith('/') ||
            !subpath.endsWith('/') ||
            subpath.includes('//') ||
            /[%\\\s]/.test(subpath) ||
            subpath.split('/').some((s) => s === '.' || s === '..')
          )
            throw new Error(
              'Use a canonical subpath ending in /, without escapes or dot segments.',
            );
          if (seen.has(subpath)) throw new Error('Subpath rules must have unique paths.');
          seen.add(subpath);
          ruleHeaders.set(row, { ...row.source.set_headers });
        }
        if (keyRules.length && changedSharedSecret) {
          const secret = state.secrets.find((s) => s.name === sharedSecret);
          if (!secret)
            throw new Error('Select a stored secret for the subpath rules, or add one first.');
          for (const row of keyRules) {
            const headers = ruleHeaders.get(row)!;
            const header = ruleCredentialHeader(row.source) || row.source.caller_auth.header;
            if (!header)
              throw new Error('Every key-bearing subpath rule needs a credential header.');
            headers[header] = {
              ...(headers[header] ?? {}),
              secret: secret.name,
              prefix: headers[header]?.prefix ?? row.source.caller_auth.prefix,
            };
          }
        }
      } else
        for (const row of rows) {
          if (
            Object.keys(setHeaders).some(
              (key) => key.toLowerCase() === row.header.value.trim().toLowerCase(),
            )
          )
            throw new Error('Header substitutions must have unique names.');
          if (row.kind.value === 'secret') {
            const secret = state.secrets.find((s) => s.name === row.secret.value);
            if (!secret && (row.secret.value !== row.savedSecret || enabled.checked))
              throw new Error('Choose an existing secret.');
            setHeaders[row.header.value.trim()] = {
              secret: row.secret.value,
              prefix: row.prefix.value,
            };
          } else setHeaders[row.header.value.trim()] = { value: row.value.value };
        }
      // An untouched account keeps its saved value even when it is unavailable;
      // any new choice must be an existing account of the required kind.
      if (needsAccount && account.value !== route?.account) {
        const chosen = (state.accounts ?? []).find((a) => a.name === account.value);
        if (!chosen || chosen.kind !== accountKind)
          throw new Error(
            `Choose an existing ${accountKind ?? 'trusted'} account, or connect a new one first.`,
          );
      }
      const next: ProxyRoute = {
        // Preserve redirect policy and other configuration not exposed here.
        ...route,
        billing: billing.value as ProxyBilling,
        ...(needsAccount ? { account: account.value } : {}),
        // Untouched controls keep the saved representation exactly.
        websocket: websocket[1].checked || (route?.websocket === false ? false : undefined),
        usage: usage.value === 'auto' ? 'auto' : route?.usage === 'off' ? 'off' : undefined,
        name: name.value.trim(),
        provider: route?.provider ?? provider?.id ?? name.value.trim(),
        profile: route?.profile ?? defaults?.id,
        allow: rulesMode ? undefined : readAllow(allow),
        local_prefix: local.value,
        // Rules carry every destination; the backend rejects parent
        // upstream/caller_auth/set_headers/allow when rules exist.
        upstream: rulesMode ? '' : upstream.value,
        enabled: route ? enabled.checked : true,
        allow_insecure_http: http.checked,
        caller_auth: rulesMode
          ? { header: '', prefix: '' }
          : { header: header.value, prefix: prefix.value },
        set_headers: rulesMode ? {} : setHeaders,
        request_id_header: requestID.value || undefined,
        ...(rulesMode
          ? {
              rules: ruleRows.map((row) => ({
                ...row.source,
                path_prefix: row.path.value,
                upstream: row.upstream.value,
                allow: readAllow(row.allow),
                set_headers: ruleHeaders.get(row)!,
              })),
            }
          : {}),
      };
      if (!route && !provider) requireProviderName(next.provider, state);
      if (state.providers.some((item) => item.id === next.name && item.id !== next.provider))
        throw new Error('Route name belongs to another provider.');
      if (
        route &&
        authority(route) !== authority(next) &&
        !window.confirm(
          `Save authority changes to ${route.name}? Affected client grants will be dropped and must be explicitly granted again. Dispatched requests may continue.`,
        )
      )
        return;
      const rawClientYAML = clientConfigYAML?.value.trim();
      const nextRoutes = route
        ? state.routes.map((item) => (item === route ? next : item))
        : [...state.routes, next];
      if (!route && !provider)
        await api.config(
          state,
          nextRoutes,
          { [next.provider]: { label: next.provider } },
          rawClientYAML ? { [next.provider]: rawClientYAML } : undefined,
        );
      else await api.config(state, nextRoutes);
      await context.refresh(`route:${next.name}`);
      context.announce(`Saved route ${next.name}. No upstream request was made.`);
    },
    'Save route',
  );
  const footer = actions(save, button('Cancel', close));
  footer.classList.add('route-form-footer');
  form.append(footer);
  if (route) {
    const affected =
      state.callers
        .filter((c) => !c.revoked && c.grants.includes(route.name))
        .map((c) => c.name)
        .join(', ') || 'Any client with this grant';
    const remove = confirmAction(
      `Delete route ${route.name}`,
      async () => {
        await api.config(
          state,
          state.routes.filter((r) => r !== route),
        );
        await context.refresh();
        context.announce(
          `Deleted route ${route.name}. Its grants were removed; dispatched requests may continue.`,
        );
      },
      context,
      `Delete route ${route.name}? ${affected} will lose access. Re-adding this name will not restore grants. Disable instead to keep grants. Only new requests are blocked; dispatched requests may continue.`,
    );
    const danger = el(
      'div',
      el('p', 'Deletion removes client grants permanently.'),
      actions(remove),
    );
    danger.className = 'route-form-danger';
    form.append(danger);
  }
  previewRoute();
  return formRoot;
}

function allowInput(entries?: string[]): HTMLTextAreaElement {
  const input = el('textarea');
  input.value = entries?.join('\n') ?? '';
  input.rows = 3;
  input.spellcheck = false;
  return input;
}

function allowField(input: HTMLTextAreaElement): HTMLElement {
  return field(
    'Allowed requests',
    input,
    'One METHOD /prefix or METHOD /prefix/* per line. GET /* allows GET on any path. Leave empty for no allowlist.',
  );
}

function readAllow(input: HTMLTextAreaElement): string[] | undefined {
  const entries = input.value
    .split('\n')
    .map((entry) => entry.trim())
    .filter(Boolean);
  return entries.length ? entries : undefined;
}

function isSRVUpstream(value: string): boolean {
  return /^srv:\/\/_([a-z0-9-]+)\._tcp\.([a-z0-9-]+(?:\.[a-z0-9-]+)*\.?)(?:\/[^?#]*)?$/i.test(
    value,
  );
}
