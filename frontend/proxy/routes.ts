import type { ProxyAccount, ProxyClient, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { browserPasskeyPlatform, type PasskeyPlatform } from '../src/hub/platform/passkeys';
import { accountControls, connectAccount, unusedAccountsView } from './accounts';
import { isClaudeRoute } from './claude';
import { actions, button, card, disclosure, el, type UIContext } from './dom';
import { clearClaudeSetup, providerOnboarding, providerRouteAction } from './provider-onboarding';
import { routeAccess } from './route-access';
import { routeForm } from './route-form';
import { matchRoutePreset, presetLabel, routePresetPicker } from './route-presets';
import { routeSummary } from './route-summary';
import { routeNeedsSecret } from './route-rules';

const PAGE_SIZE = 10;
const routePage = new WeakMap<UIContext, number>();

function pageNumbers(current: number, total: number): (number | 'ellipsis')[] {
  if (total <= 7) return Array.from({ length: total }, (_, i) => i);
  const pages: (number | 'ellipsis')[] = [0];
  const start = Math.max(1, current - 1);
  const end = Math.min(total - 2, current + 1);
  if (start > 1) pages.push('ellipsis');
  for (let i = start; i <= end; i++) pages.push(i);
  if (end < total - 2) pages.push('ellipsis');
  pages.push(total - 1);
  return pages;
}

export function routesView(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  platform: PasskeyPlatform = browserPasskeyPlatform(),
): HTMLElement {
  const readonly = state.mode === 'file';
  const view = el('div');
  const intro = card(
    'Routes',
    el('p', 'Connect a provider, then give clients access to its route.'),
  );
  intro.classList.add('page-intro');
  view.append(intro);

  // The app guards navigation. Within Routes, an operation must not replace a
  // component that is issuing or displaying a show-once client token.
  let tokenOwner: symbol | undefined;
  const operations = new Map<symbol, number>();
  const blocked = (owner?: symbol) => {
    if ((tokenOwner || context.hasProtectedToken?.()) && (!owner || tokenOwner !== owner)) {
      context.announce('Finish saving or dismissing the one-time connection details first.');
      return true;
    }
    if ([...operations.keys()].some((active) => active !== owner)) {
      context.announce('Wait for the current route action to finish.');
      return true;
    }
    return false;
  };
  const componentContext = (): UIContext => {
    const owner = Symbol();
    return {
      ...context,
      async run(action, source) {
        if (blocked(owner)) return false;
        operations.set(owner, (operations.get(owner) ?? 0) + 1);
        try {
          return await context.run(action, source);
        } finally {
          const remaining = (operations.get(owner) ?? 1) - 1;
          if (remaining) operations.set(owner, remaining);
          else operations.delete(owner);
        }
      },
      hasProtectedToken: () => !!tokenOwner || !!context.hasProtectedToken?.(),
      protectToken(shown, pending) {
        if (shown) tokenOwner = owner;
        else if (tokenOwner === owner) tokenOwner = undefined;
        else return;
        context.protectToken(shown, pending);
      },
    };
  };

  function focusWithin(node: HTMLElement) {
    node.querySelector<HTMLElement>('h2, button, input')?.focus();
  }

  const composer = el('div');
  let editor: { row: HTMLElement; form: HTMLElement } | undefined;
  function closeEditor() {
    editor?.form.remove();
    editor?.row.classList.remove('expanded');
    editor = undefined;
  }
  if (readonly) {
    view.append(
      el(
        'p',
        'Routes are file-managed and read-only. Reloading drops incompatible client grants fail-closed.',
      ),
    );
  } else {
    const toolbar = el('div');
    toolbar.className = 'route-cmd-bar';
    const addButton = button('Add route', () => {
      if (blocked()) return;
      if (!composer.childElementCount) {
        closeEditor();
        composer.append(
          routePresetPicker(
            state.presets ?? [],
            (preset) => {
              if (blocked()) return;
              const formContext = componentContext();
              const manualForm = () =>
                routeForm(state, api, formContext, close, undefined, preset, platform);
              const guided =
                preset && providerOnboarding(preset, state, api, formContext, platform, close);
              const panel =
                guided && preset ? card(`Set up ${presetLabel(preset)}`, guided) : manualForm();
              if (guided && preset?.account_kind === 'claude-code')
                panel.append(actions(button('Cancel', close)));
              // Guided sign-in is the default. Existing operators can still
              // attach a manually imported ChatGPT account to a new route.
              if (guided && preset?.account_kind === 'chatgpt') {
                panel.append(
                  disclosure(
                    'Advanced',
                    button('Use an existing account manually', () => {
                      if (blocked()) return;
                      composer.replaceChildren(manualForm());
                      composer.querySelector('input')?.focus();
                    }),
                  ),
                );
              }
              composer.replaceChildren(panel);
              if (guided) focusWithin(composer);
              else composer.querySelector('input')?.focus();
            },
            close,
          ),
        );
        addButton.setAttribute('aria-expanded', 'true');
      }
      focusWithin(composer);
    });
    function close() {
      if (tokenOwner || context.hasProtectedToken?.()) {
        context.announce('Finish saving or dismissing the one-time connection details first.');
        return;
      }
      composer.replaceChildren();
      addButton.setAttribute('aria-expanded', 'false');
      addButton.focus();
    }
    addButton.className = 'primary';
    addButton.setAttribute('aria-expanded', 'false');
    toolbar.append(addButton);
    view.append(toolbar, composer);
  }

  let currentPage = Math.min(
    routePage.get(context) ?? 0,
    Math.max(0, Math.ceil(state.routes.length / PAGE_SIZE) - 1),
  );
  const routeList = el('div');
  routeList.className = 'route-list';
  const pagination = el('nav');
  pagination.className = 'route-pagination';
  pagination.setAttribute('aria-label', 'Route pagination');

  const accountDeps = () => ({ state, api, context: componentContext(), platform });
  // Credentials are maintained beside the routes that use them. Claude
  // sign-ins live on each device and are managed by Claude Code settings.
  function routeAccountTools(route: ProxyRoute, kind?: ProxyAccount['kind']) {
    if (!route.account || state.accounts === undefined || isClaudeRoute(route, state)) return;
    const account = state.accounts.find((a) => a.name === route.account);
    if (!account) return connectAccount(accountDeps(), route.account, kind);
    if (account.kind === 'claude-code') return;
    const usedBy = state.routes.filter((r) => r.account === account.name).map((r) => r.name);
    return accountControls(accountDeps(), account, usedBy, `route:${route.name}`);
  }
  function claudeSettings() {
    const settings = disclosure(
      'Claude Code settings',
      el('p', 'Removes every Claude route, device connection and saved proxy sign-in.'),
      clearClaudeSetup(state, api, componentContext(), platform),
    );
    settings.className = 'claude-settings';
    return settings;
  }

  function renderRow(route: ProxyRoute): HTMLElement {
    const preset = matchRoutePreset(route, state);
    const display = routeSummary(route, state, preset);
    const head = el('div', display.summary);
    head.className = 'route-row-head';
    const provider = el('div', el('h3', 'Provider setup'), display.provider);
    const providerAction = providerRouteAction(route, state, api, componentContext(), platform);
    if (providerAction) provider.append(providerAction);
    const accountTools = routeAccountTools(route, preset?.account_kind);
    if (accountTools) provider.append(accountTools);
    if (!readonly && isClaudeRoute(route, state)) provider.append(claudeSettings());
    const sides = el('div', provider, routeAccess(route, state, api, componentContext(), platform));
    sides.className = 'route-connections';
    const row = el('article', head, sides, display.advanced);
    row.dataset.focus = `route:${route.name}`;
    row.className = `route-card route-accordion-item${route.enabled ? '' : ' disabled-route'}`;
    if (readonly) return row;

    const edit = (showAccount = false) => {
      if (blocked()) return;
      if (editor?.row === row) {
        focusWithin(editor.form);
        return;
      }
      closeEditor();
      const form = routeForm(
        state,
        api,
        componentContext(),
        () => {
          if (blocked()) return;
          closeEditor();
          focusWithin(row);
        },
        route,
        preset,
        platform,
      );
      row.classList.add('expanded');
      row.append(form);
      editor = { row, form };
      if (showAccount) {
        const technical = form.querySelector<HTMLDetailsElement>('details');
        if (technical) technical.open = true;
      }
      form.querySelector('input')?.focus();
    };
    const editButton = button('Edit', () => edit());
    if (!providerAction && (route.account || routeNeedsSecret(route))) {
      const configure = button(route.account ? 'Change linked account' : 'Configure API key', () =>
        edit(!!route.account),
      );
      provider.append(actions(configure));
    }
    editButton.setAttribute('aria-label', `Edit ${route.name}`);
    const toggle = button(route.enabled ? 'Disable' : 'Enable', () => {
      if (blocked()) return;
      if (
        route.enabled &&
        !window.confirm(
          `Disable route ${route.name}? New requests will be blocked. Client grants are kept; dispatched requests may continue until finish or deadline.`,
        )
      )
        return;
      void rowContext.run(async () => {
        await api.config(
          state.revision,
          state.routes.map((r) => (r === route ? { ...r, enabled: !r.enabled } : r)),
        );
        await context.refresh(`route:${route.name}`);
        context.announce(
          `${route.enabled ? 'Disabled' : 'Enabled'} route ${route.name}.${route.enabled ? ' New requests are blocked; dispatched requests may continue.' : ''}`,
        );
      }, toggle);
    });
    const rowContext = componentContext();
    toggle.setAttribute('aria-label', `${route.enabled ? 'Disable' : 'Enable'} ${route.name}`);
    head.append(actions(editButton, toggle));
    return row;
  }

  function renderPage() {
    const start = currentPage * PAGE_SIZE;
    const end = Math.min(start + PAGE_SIZE, state.routes.length);
    routeList.replaceChildren(...state.routes.slice(start, end).map(renderRow));
    const totalPages = Math.ceil(state.routes.length / PAGE_SIZE);
    if (totalPages <= 1) return;
    const goTo = (page: number) => {
      if (page === currentPage || blocked()) return;
      closeEditor();
      currentPage = page;
      routePage.set(context, page);
      renderPage();
      focusWithin(routeList);
    };
    const controls = el('div');
    controls.className = 'route-pagination-controls';
    const prev = button('Previous', () => goTo(currentPage - 1));
    prev.disabled = currentPage === 0;
    controls.append(prev);
    for (const item of pageNumbers(currentPage, totalPages)) {
      if (item === 'ellipsis') {
        const dots = el('span', '…');
        dots.className = 'route-pagination-ellipsis';
        dots.setAttribute('aria-hidden', 'true');
        controls.append(dots);
      } else {
        const control = button(String(item + 1), () => goTo(item));
        control.setAttribute('aria-label', `Page ${item + 1}`);
        if (item === currentPage) control.setAttribute('aria-current', 'page');
        controls.append(control);
      }
    }
    const next = button('Next', () => goTo(currentPage + 1));
    next.disabled = currentPage >= totalPages - 1;
    controls.append(next);
    pagination.replaceChildren(
      el('small', `Showing ${start + 1}–${end} of ${state.routes.length} routes`),
      controls,
    );
    if (!pagination.parentElement) view.append(pagination);
  }

  if (state.routes.length) {
    view.append(routeList);
    renderPage();
  } else {
    view.append(
      card(
        'Start with one route',
        el(
          'p',
          readonly
            ? 'No routes are defined in the proxy configuration file.'
            : 'Choose a provider preset or add a custom endpoint. You can set up client access after saving the route.',
        ),
      ),
    );
  }
  // Claude sign-ins can outlive their routes; keep the reset reachable.
  const orphanClaude =
    !readonly &&
    !state.routes.some((route) => isClaudeRoute(route, state)) &&
    state.accounts?.some((account) => account.kind === 'claude-code');
  if (orphanClaude) {
    const claude = card('Claude Code', claudeSettings());
    claude.classList.add('claude-leftovers');
    view.append(claude);
  }
  if (state.accounts !== undefined) {
    const unused = unusedAccountsView(accountDeps());
    if (unused) view.append(unused);
  }
  return view;
}
