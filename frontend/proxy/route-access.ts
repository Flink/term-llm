import {
  ProxyAPIError,
  type ProxyCaller,
  type ProxyClient,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { isClaudeRoute } from './claude';
import { actions, button, dateTime, el, field, input, select, submit, type UIContext } from './dom';
import { authLabel, joinLocalPrefix, routeRules } from './route-rules';
import { callerCurl } from './security';

let panelSequence = 0;

function grantedClients(state: ProxyState, route: ProxyRoute): ProxyCaller[] {
  return state.callers.filter((caller) => !caller.revoked && caller.grants.includes(route.name));
}

// Sibling routes (for example chatgpt-backend) that clients reach by fixed name
// beside this preset route. Only routes that exist are returned.
function companionRoutes(state: ProxyState, route: ProxyRoute): string[] {
  const preset = state.presets?.find((value) => value.id === route.name);
  return (preset?.companions ?? []).filter((name) => state.routes.some((r) => r.name === name));
}

// Use the built-in CLI name only when the route matches its preset. A custom
// route's name alone doesn't tell us how term-llm is configured on the device.
function termLLMConnection(route: ProxyRoute, origin: string): HTMLElement | undefined {
  if (
    route.local_prefix !== `/${route.name}/` ||
    route.caller_auth.header.toLowerCase() !== 'authorization' ||
    route.caller_auth.prefix !== 'Bearer '
  )
    return;

  const upstream = route.upstream.replace(/\/$/, '');
  const isChatGPT =
    route.name === 'chatgpt' && upstream === 'https://chatgpt.com/backend-api/codex';
  const isVenice = route.name === 'venice' && upstream === 'https://api.venice.ai/api/v1';
  if (!isChatGPT && !isVenice) return;

  const yaml = `proxy:\n  url: ${origin}\n  token: file:///run/secrets/proxy-client-token\n  providers: [${route.name}]`;
  const section = el(
    'div',
    el('h4', 'term-llm client configuration'),
    el(
      'p',
      `Use this top-level block in config.yaml to proxy the built-in ${isChatGPT ? 'ChatGPT' : 'Venice'} provider and its related features. Other providers stay direct. Change the file reference to the private file where you saved this client token.`,
    ),
    el('pre', el('code', yaml)),
  );
  if (isChatGPT)
    section.append(
      el(
        'p',
        'Chat is covered by this route. If you use features on chatgpt-backend or chatgpt-openai, grant those routes to the same client too.',
      ),
    );
  return section;
}

// Keep the same generic snippets as the Clients view so a route-scoped token
// can be used without redirecting unrelated providers.
function connectionDetails(route: ProxyRoute): HTMLElement {
  const rules = routeRules(route);
  if (!rules.length) {
    const details = el(
      'div',
      el(
        'p',
        `Base URL: ${location.origin}${route.local_prefix} · Header: ${route.caller_auth.header}: ${JSON.stringify(route.caller_auth.prefix)} + <token>`,
      ),
      el('pre', callerCurl(route, location.origin)),
    );
    const termLLM = termLLMConnection(route, location.origin);
    if (termLLM) details.append(termLLM);
    return details;
  }
  // A unified route has no parent client auth; each subpath declares which
  // header carries the same client token.
  const base = rules.find((rule) => rule.path_prefix === '/') ?? rules[0];
  return el(
    'div',
    el(
      'p',
      `Base URL: ${location.origin}${route.local_prefix} · one client token and one grant cover every subpath.`,
    ),
    el(
      'ul',
      ...rules.map((rule) =>
        el(
          'li',
          `${joinLocalPrefix(route.local_prefix, rule.path_prefix)} · client ${authLabel(rule.caller_auth)}`,
        ),
      ),
    ),
    el(
      'pre',
      callerCurl(
        {
          ...route,
          caller_auth: base.caller_auth,
          local_prefix: joinLocalPrefix(route.local_prefix, base.path_prefix),
        },
        location.origin,
      ),
    ),
  );
}

/**
 * Inline per-route grant management for one Routes row: list the clients that
 * can use this route, grant an existing client or create a route-scoped client,
 * and remove this route's grant without touching the client's other grants.
 * Claude Code routes delegate new devices to the provider launcher instead of
 * handing out a base URL that Claude Code cannot use.
 */
export function routeAccess(
  route: ProxyRoute,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  let current = state;
  const native = isClaudeRoute(route, current);
  const panelId = `route-access-${++panelSequence}`;
  let expanded = false;
  let tokenShown = false;

  const status = el('p');
  status.className = 'route-access-status';
  const toggle = button('Grant access', () => {
    if (expanded) collapse();
    else expand();
  });
  toggle.className = 'route-access-toggle';
  toggle.setAttribute('aria-controls', panelId);
  const panel = el('div');
  panel.id = panelId;
  panel.className = 'route-access-panel';
  panel.hidden = true;
  const view = el('div', el('h3', 'Client access'), status, toggle, panel);
  view.className = 'route-access';

  function clients(): ProxyCaller[] {
    return grantedClients(current, route);
  }

  function renderStatus() {
    const granted = clients();
    const label = granted.length ? 'Manage access' : 'Grant access';
    // Keep the row-distinguishing name in the accessible label while the
    // visible label stays short.
    toggle.textContent = label;
    toggle.setAttribute('aria-label', `${label} for route ${route.name}`);
    status.textContent = granted.length
      ? granted.map((caller) => caller.name).join(', ')
      : 'No clients yet.';
    toggle.setAttribute('aria-expanded', expanded ? 'true' : 'false');
  }

  // A refresh would drop another row's show-once token, so refuse to start.
  function tokenGuard(message: string): boolean {
    if (!context.hasProtectedToken?.()) return false;
    context.announce(message);
    return true;
  }

  async function mutate<T>(operation: () => Promise<T>): Promise<T> {
    try {
      return await operation();
    } catch (error) {
      if (error instanceof ProxyAPIError && error.status === 409) {
        // Never replay a stale write. Reload the authoritative revision so the
        // reopened panel shows the grants that actually exist now.
        try {
          await context.refresh(`route:${route.name}`);
        } catch {
          // The conflict message still tells the operator to refresh.
        }
      }
      throw error;
    }
  }

  function removeAccess(caller: ProxyCaller): HTMLButtonElement {
    const others = caller.grants.filter((grant) => grant !== route.name);
    const nativeNote = native
      ? ' Its Claude sign-in through this route ends; other routes and your normal Claude setup are unchanged.'
      : '';
    const consequence = others.length
      ? `Remove ${caller.name}’s access to ${route.name}? Its token and its grants for ${others.join(', ')} stay valid. New requests to ${route.name} are blocked; dispatched requests may continue.${nativeNote}`
      : `Remove ${caller.name}’s access to ${route.name}? This is its only route grant. Its token stays valid but can make no new requests; dispatched requests may continue.${nativeNote}`;
    const remove = button('Remove', () => {
      if (tokenGuard('Save or dismiss the open one-time client token before removing access.'))
        return;
      if (!window.confirm(consequence)) return;
      void context.run(async () => {
        await mutate(() => api.updateCaller(caller.id, others, current.revision));
        await context.refresh(`route:${route.name}`);
        context.announce(
          `Removed ${caller.name}’s access to ${route.name}. ${
            others.length ? 'Its token and other grants are unchanged.' : 'Its token is unchanged.'
          }`,
        );
      }, remove);
    });
    remove.className = 'danger';
    remove.setAttribute('aria-label', `Remove ${caller.name}’s access to ${route.name}`);
    return remove;
  }

  // One click grants the sibling routes this client is missing.
  function addCompanions(caller: ProxyCaller, missing: string[]): HTMLButtonElement {
    const add = button(`Add ${missing.join(', ')}`, () => {
      if (
        tokenGuard('Save or dismiss the open one-time client token before granting route access.')
      )
        return;
      void context.run(async () => {
        await mutate(() =>
          api.updateCaller(caller.id, [...caller.grants, ...missing], current.revision),
        );
        await context.refresh(`route:${route.name}`);
        context.announce(`Granted ${caller.name} access to ${missing.join(', ')}.`);
      }, add);
    });
    add.setAttribute('aria-label', `Grant ${caller.name} access to ${missing.join(', ')}`);
    return add;
  }

  function currentAccess(): HTMLElement {
    const granted = clients();
    const section = el('div', el('h4', 'Clients'));
    section.className = 'route-access-current';
    if (!granted.length) {
      section.append(el('p', 'None.'));
      return section;
    }
    const companions = companionRoutes(current, route);
    const list = el('ul');
    list.className = 'route-access-clients';
    for (const caller of granted) {
      const missing = companions.filter((name) => !caller.grants.includes(name));
      const buttons = actions(
        ...(missing.length ? [addCompanions(caller, missing)] : []),
        removeAccess(caller),
      );
      const item = el(
        'li',
        el(
          'span',
          el('strong', caller.name),
          ' ',
          el('small', caller.last_used ? `used ${dateTime(caller.last_used)}` : 'never used'),
        ),
        buttons,
      );
      item.className = 'route-access-client';
      list.append(item);
    }
    section.append(list);
    return section;
  }

  function grantExisting(): HTMLElement | undefined {
    const eligible = current.callers.filter(
      (caller) => !caller.revoked && !caller.grants.includes(route.name),
    );
    if (!eligible.length) return undefined;
    const section = el('div', el('h4', 'Add existing client'));
    section.className = 'route-access-grant';
    const choice = select([
      ['', 'Choose a client'],
      ...eligible.map(
        (caller) =>
          [
            caller.id,
            `${caller.name}${
              caller.grants.length ? ` — ${caller.grants.join(', ')}` : ' — no other routes'
            }`,
          ] as [string, string],
      ),
    ]);
    const form = el('form', field('Client', choice));
    submit(
      form,
      context,
      async () => {
        if (
          tokenGuard('Save or dismiss the open one-time client token before granting route access.')
        )
          return;
        const caller = eligible.find((value) => value.id === choice.value);
        if (!caller) throw new Error('Choose an existing client.');
        await mutate(() =>
          api.updateCaller(caller.id, [...caller.grants, route.name], current.revision),
        );
        await context.refresh(`route:${route.name}`);
        context.announce(
          `Granted ${caller.name} access to ${route.name}. Its token and other grants are unchanged.`,
        );
      },
      'Grant access',
    );
    section.append(form);
    return section;
  }

  function showToken(value: string, caller: ProxyCaller) {
    tokenShown = true;
    toggle.disabled = true;
    toggle.title = 'Save or dismiss the one-time client token first.';
    const token = input(value);
    token.readOnly = true;
    token.className = 'caller-token';
    token.spellcheck = false;
    const copy = button('Copy token', () => {
      void context.run(async () => {
        if (!navigator.clipboard) {
          token.focus();
          token.select();
          context.announce('Clipboard unavailable. Copy the selected token manually.');
          return;
        }
        try {
          await navigator.clipboard.writeText(token.value);
          copy.textContent = 'Copied ✓';
          setTimeout(() => {
            if (copy.isConnected) copy.textContent = 'Copy token';
          }, 2000);
          context.announce('Client token copied. Store it safely before dismissing.');
        } catch {
          token.focus();
          token.select();
          context.announce('Copy unavailable. Copy the selected token manually.');
        }
      }, copy);
    });
    copy.setAttribute('aria-label', 'Copy token');
    const dismiss = button('I saved it — dismiss', () => {
      void context.run(async () => {
        // The token is unrecoverable after this point: clear every copy in the
        // DOM before attempting the authoritative read.
        token.value = '';
        panel.querySelectorAll('textarea').forEach((text) => {
          text.value = '';
        });
        context.protectToken(false);
        tokenShown = false;
        toggle.disabled = false;
        toggle.removeAttribute('title');
        renderBody();
        try {
          await context.refresh(`caller:${caller.id}`);
          context.announce(`Client ${caller.name} created. The one-time token has been dismissed.`);
        } catch (error) {
          if (error instanceof ProxyAPIError && error.status === 401) throw error;
          context.announce(
            'Client created. The one-time token was cleared; refresh state before changing access.',
          );
        }
      }, dismiss);
    });
    const heading = el('h4', 'Copy this token now');
    heading.tabIndex = -1;
    panel.replaceChildren(
      heading,
      el(
        'p',
        `Shown once. Save it securely before dismissing; it cannot be retrieved later. Access is limited to ${route.name} until you grant more.`,
      ),
      field('Client token', token),
      copy,
      connectionDetails(route),
      el(
        'p',
        'For curl examples, set TOKEN in your shell to this client token, not the administrator token.',
      ),
      el(
        'p',
        'For apps configured manually, point only this provider’s base URL at the proxy. Do not set a global HTTP_PROXY or HTTPS_PROXY; a global proxy variable would reroute other providers.',
      ),
      dismiss,
    );
    token.focus();
    token.select();
    context.announce(`Created client ${caller.name}. Copy the token now; it is shown once.`);
  }

  function createClient(): HTMLElement {
    const section = el('div', el('h4', 'New client'));
    section.className = 'route-access-create';
    const name = input();
    name.required = true;
    name.maxLength = 80;
    const form = el('form', field('Client name', name));
    submit(
      form,
      context,
      async () => {
        if (
          tokenGuard(
            'Save or dismiss the open one-time client token before creating another client.',
          )
        )
          return;
        const requested = name.value.trim();
        if (!requested) throw new Error('Enter a client name.');
        context.protectToken(true, true);
        let issued = false;
        try {
          context.announce('Creating client… wait for the one-time token.');
          const result = await api.createCaller(requested, [route.name], current.revision);
          issued = true;
          context.protectToken(true);
          showToken(result.token, result.caller);
          try {
            // Reconcile revision/CSRF without discarding the only copy of the
            // token: a normal refresh would replace this whole view.
            const latest = await context.refresh(undefined, true);
            if (latest) {
              current = latest;
              renderStatus();
            }
            context.announce(
              `Created client ${result.caller.name}. Copy the token now; it is shown once.`,
            );
          } catch (error) {
            if (error instanceof ProxyAPIError && error.status === 401) throw error;
            context.announce(
              'Client created. Save or dismiss the token now; state refresh is unavailable.',
            );
          }
        } finally {
          if (!issued) context.protectToken(false);
        }
      },
      'Create client',
    );
    section.append(form);
    return section;
  }

  function nativeGuidance(): HTMLElement {
    const section = el('div', el('h4', 'Connect a Claude Code device'));
    section.className = 'route-access-native';
    const guidance = !route.enabled
      ? 'This route is disabled, so its device launcher is unavailable. Enable it to issue a one-time launcher command from Provider setup.'
      : current.mode === 'file'
        ? 'This route and its Claude account are file-managed. New devices use the launcher from your deployment’s Claude setup.'
        : 'Claude Code connects through the device launcher, not a generic API base URL. Use Add device in Provider setup to issue a one-time launcher command for this route.';
    section.append(el('p', guidance));
    if (route.enabled && current.mode !== 'file')
      section.append(
        el(
          'small',
          'The launcher keeps your normal Claude setup unchanged. Access above controls which devices may use this route.',
        ),
      );
    return section;
  }

  function renderBody() {
    if (tokenShown) return;
    const parts: HTMLElement[] = [];
    if (native)
      parts.push(
        el(
          'p',
          'Claude Code signs in on each device; removing access ends that device’s sign-in for this route only.',
        ),
      );
    if (!route.enabled)
      parts.push(
        el(
          'p',
          'This route is disabled. Grants are kept; new requests are blocked until you enable it.',
        ),
      );
    if (current.mode === 'file')
      parts.push(
        el(
          'p',
          'Routes are file-managed here. Grants stay editable, but a configuration reload may drop grants that no longer match.',
        ),
      );
    parts.push(currentAccess());
    if (native) parts.push(nativeGuidance());
    else {
      const existing = grantExisting();
      if (existing) parts.push(existing);
      parts.push(createClient());
    }
    parts.push(
      actions(
        button('Close', () => {
          toggle.focus();
          collapse();
        }),
      ),
    );
    panel.replaceChildren(...parts);
  }

  function expand() {
    if (expanded) return;
    expanded = true;
    renderBody();
    panel.hidden = false;
    renderStatus();
    const heading = panel.querySelector<HTMLElement>('h4');
    if (heading) {
      heading.tabIndex = -1;
      heading.focus();
    }
  }

  function collapse() {
    if (!expanded || tokenShown) return;
    expanded = false;
    panel.hidden = true;
    renderStatus();
  }

  renderStatus();
  return view;
}
