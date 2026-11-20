import {
  ProxyAPIError,
  type ProxyCaller,
  type ProxyClient,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { routeAllowsClientApproval, routeNativeProvider, routeSharedNativeAccount } from './native';
import { actions, button, dateTime, el, field, input, select, submit, type UIContext } from './dom';
import { authLabel, joinLocalPrefix, routeRules } from './route-rules';
import { providerApproval, type ProviderGroup } from './provider-groups';
import { callerCurl } from './security';

function termLLMConnection(origin: string, group: ProviderGroup): HTMLElement {
  const yaml = `proxy:\n  url: ${origin}\n  token: file:///run/secrets/proxy-client-token\n  providers: [${group.id}]`;
  const section = el(
    'div',
    el('h4', 'term-llm client configuration'),
    el(
      'p',
      `Add ${group.id} to proxy.providers in config.yaml to connect to this provider through the proxy. Other providers stay direct. Change the file reference to the private file where you saved this client token.`,
    ),
    el('pre', el('code', yaml)),
  );
  const visionRef = group.config?.vision_via?.trim();
  if (visionRef) {
    const visionProvider = visionRef.split(':')[0];
    section.append(
      el(
        'p',
        `This provider routes vision via ${visionRef}. Approve ${visionProvider} and include it in proxy.providers if clients use image inputs.`,
      ),
    );
  }
  return section;
}

// Keep endpoint instructions separate from approval: the token covers the
// provider, while individual integrations still need their own base URL.
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

/** Client approval is managed once for the whole configured provider. */
export function providerAccess(
  group: ProviderGroup,
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  let current = state;
  const focusKey = `route:${group.routes[0].name}`;
  const native = group.routes.map((route) => routeNativeProvider(route, state)).find(Boolean);
  const canApprove = () => group.routes.every((route) => routeAllowsClientApproval(route, current));
  let tokenShown = false;

  const panel = el('div');
  panel.className = 'route-access-panel';
  const view = el('div', el('h3', 'Client approval'), panel);
  view.className = 'route-access provider-section';

  function clients(): ProxyCaller[] {
    return current.callers.filter(
      (caller) => !caller.revoked && providerApproval(group, caller) !== 'none',
    );
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
          await context.refresh(focusKey);
        } catch {
          // The conflict message still tells the operator to refresh.
        }
      }
      throw error;
    }
  }

  function removeAccess(caller: ProxyCaller): HTMLButtonElement {
    const remove = button('Remove access', () => {
      if (tokenGuard('Save or dismiss the open one-time client token before removing access.'))
        return;
      const sharedNote = group.routes.some(
        (route) => caller.grants.includes(route.name) && routeSharedNativeAccount(route, current),
      )
        ? ` The shared ${native?.shortName ?? 'subscription'} sign-in and other clients’ access are unchanged.`
        : '';
      if (
        !window.confirm(
          `Remove ${caller.name}’s access to ${group.label}? Access to other providers and its token stay unchanged. New requests to ${group.label} will be blocked; dispatched requests may continue.${sharedNote}`,
        )
      )
        return;
      void context.run(async () => {
        await mutate(() =>
          api.updateProviderApprovals(
            caller.id,
            [{ id: group.id, approved: false }],
            current.revision,
          ),
        );
        await context.refresh(focusKey);
        context.announce(`Removed ${caller.name}’s access to ${group.label}.`);
      }, remove);
    });
    remove.className = 'danger';
    remove.setAttribute('aria-label', `Remove ${caller.name}’s access to ${group.label}`);
    return remove;
  }

  function currentAccess(): HTMLElement {
    const granted = clients();
    const section = el('div');
    section.className = 'route-access-current';
    if (!granted.length) {
      const empty = el('p', 'No clients approved yet.');
      empty.className = 'route-access-empty';
      section.append(empty);
      return section;
    }
    const list = el('ul');
    list.className = 'route-access-clients';
    for (const caller of granted) {
      const label = providerApproval(group, caller) === 'partial' ? 'Needs approval' : 'Approved';
      const buttons = actions(removeAccess(caller));
      const item = el(
        'li',
        el(
          'span',
          el('strong', caller.name),
          ' ',
          el('small', caller.last_used ? `${label} · used ${dateTime(caller.last_used)}` : label),
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
      (caller) => !caller.revoked && providerApproval(group, caller) !== 'approved',
    );
    if (!eligible.length) return undefined;
    const section = el('div', el('h4', 'Add existing client'));
    section.className = 'route-access-grant';
    const choice = select([
      ['', 'Choose a client'],
      ...eligible.map((caller): [string, string] => [
        caller.id,
        providerApproval(group, caller) === 'partial'
          ? `${caller.name} (needs approval)`
          : caller.name,
      ]),
    ]);
    const form = el('form', field('Client', choice));
    submit(
      form,
      context,
      async () => {
        if (
          tokenGuard(
            'Save or dismiss the open one-time client token before approving provider access.',
          )
        )
          return;
        const caller = eligible.find((value) => value.id === choice.value);
        if (!caller) throw new Error('Choose an existing client.');
        await mutate(() =>
          api.updateProviderApprovals(
            caller.id,
            [{ id: group.id, approved: true }],
            current.revision,
          ),
        );
        await context.refresh(focusKey);
        context.announce(
          `Approved ${caller.name} for ${group.label}. Its token and other approvals are unchanged.`,
        );
      },
      'Approve access',
    );
    section.append(form);
    return section;
  }

  function showToken(value: string, caller: ProxyCaller) {
    tokenShown = true;
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
        `Shown once. Save it securely before dismissing; it cannot be retrieved later. Access is limited to the current routes for ${group.label}.`,
      ),
      field('Client token', token),
      copy,
      termLLMConnection(location.origin, group),
      ...group.routes.map(connectionDetails),
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
          const result = await mutate(() =>
            api.createProviderCaller(requested, [group.id], current.revision),
          );
          issued = true;
          context.protectToken(true);
          showToken(result.token, result.caller);
          try {
            // Reconcile revision/CSRF without discarding the only copy of the
            // token: a normal refresh would replace this whole view.
            const latest = await context.refresh(undefined, true);
            if (latest) current = latest;
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

  function renderBody() {
    if (tokenShown) return;
    const parts: HTMLElement[] = [currentAccess()];
    if (current.mode === 'file') {
      const note = el(
        'p',
        'Routes are file-managed. Approvals are editable, but a configuration reload may drop incompatible grants.',
      );
      note.className = 'provider-note';
      parts.push(note);
    }
    if (canApprove()) {
      const forms: HTMLElement[] = [];
      const existing = grantExisting();
      if (existing) forms.push(existing);
      if (!native) forms.push(createClient());
      if (forms.length) {
        const row = el('div', ...forms);
        row.className = 'route-access-forms';
        parts.push(row);
      }
    } else {
      const note = el(
        'p',
        'Native routes must use an enabled, signed-in shared subscription before clients can be approved.',
      );
      note.className = 'provider-note';
      parts.push(note);
    }
    if (native) {
      const hasShared = group.routes.some((route) => routeSharedNativeAccount(route, current));
      if (hasShared) {
        const note = el(
          'p',
          `Workers use the shared ${native.shortName} sign-in. Removing a client’s access does not sign out ${native.shortName} or affect other clients.`,
        );
        note.className = 'provider-note';
        parts.push(note);
      }
      if (current.mode === 'ui' && canApprove()) {
        const note = el(
          'p',
          `${native.label} uses a device launcher instead of a generic API URL. Use Add device below to connect a new device.`,
        );
        note.className = 'provider-note';
        parts.push(note);
      }
    }
    panel.replaceChildren(...parts);
  }

  renderBody();
  return view;
}
