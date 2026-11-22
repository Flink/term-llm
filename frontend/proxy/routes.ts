import {
  ProxyAPIError,
  type ProxyAccount,
  type ProxyClient,
  type ProxyPreset,
  type ProxyProviderFamily,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import { accountControls, connectAccount, unusedAccountsView } from './accounts';
import {
  nativeProvider,
  nativeProviders,
  routeNativeProvider,
  type NativeProvider,
} from './native';
import { actions, button, card, disclosure, el, field, input, submit, type UIContext } from './dom';
import {
  approvalSummary,
  providerGroups,
  providerRouteLine,
  providerFamilies,
  type ProviderGroup,
} from './provider-groups';
import { clearNativeSetup, providerOnboarding, providerRouteAction } from './provider-onboarding';
import { providerAccess } from './provider-access';
import { providerConfigSection } from './provider-config';
import { routeForm } from './route-form';
import { missingCompanions } from './route-companions';
import { providerForm } from './provider-form';
import { clientDefaults } from './provider-defaults';
import { matchRoutePreset, presetLabel, routePresetPicker } from './route-presets';
import { routeSummary } from './route-summary';
import { routeNeedsSecret, routeRules } from './route-rules';

const PAGE_SIZE = 10;
const providerPages = new WeakMap<UIContext, Map<ProxyProviderFamily, number>>();
const openManagers = new WeakMap<UIContext, Set<string>>();

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

function routeUsesSecret(route: ProxyRoute): boolean {
  const rules = routeRules(route);
  const destinations = rules.length ? rules : [route];
  return destinations.some((dest) =>
    Object.values(dest.set_headers).some((header) => header.secret !== undefined),
  );
}

function deleteProviderConsequence(group: ProviderGroup, state: ProxyState): string {
  const routeNames = new Set(group.routes.map((route) => route.name));
  const affected = state.callers
    .filter((caller) => !caller.revoked && caller.grants.some((grant) => routeNames.has(grant)))
    .map((caller) => caller.name)
    .join(', ');
  const routesPart =
    group.routes.length === 1
      ? `Removes route ${group.routes[0].name}`
      : `Removes ${group.routes.length} routes (${group.routes.map((route) => route.name).join(', ')})`;
  const notes: string[] = [];
  if (group.routes.some((route) => route.account)) {
    notes.push('Connected accounts stay until removed separately.');
  }
  if (group.routes.some(routeUsesSecret)) {
    notes.push('Keys no other route uses are removed from Secrets.');
  }
  const extra = notes.length ? ` ${notes.join(' ')}` : '';
  return `Delete provider ${group.id}? ${routesPart} and permanently revokes client access${affected ? ` for ${affected}` : ''}.${extra} Re-adding this provider will not restore grants. Only new requests are blocked; dispatched requests may continue.`;
}

export function routesView(state: ProxyState, api: ProxyClient, context: UIContext): HTMLElement {
  const readonly = state.mode === 'file';
  const view = el('div');
  const groups = providerGroups(state);
  const open = openManagers.get(context) ?? new Set<string>();
  openManagers.set(context, open);
  const intro = card('Providers');
  intro.classList.add('page-intro', 'providers-intro');
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
    node.querySelector<HTMLElement>('h2, [data-focus], button, input')?.focus();
  }

  const composer = el('div');
  let editor: { row: HTMLElement; form: HTMLElement } | undefined;
  function closeEditor() {
    editor?.form.remove();
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
    const addButton = button('Add provider', () => {
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
                preset
                  ? card(
                      `Set up ${presetLabel(preset)}`,
                      providerForm(preset, state, api, formContext, close),
                    )
                  : routeForm(state, api, formContext, close);
              const guided = preset && providerOnboarding(preset, state, api, formContext, close);
              const panel =
                guided && preset ? card(`Set up ${presetLabel(preset)}`, guided) : manualForm();
              if (guided && nativeProvider(preset?.account_kind))
                panel.append(actions(button('Cancel', close)));
              const kind = preset?.account_kind;
              const manualAction =
                kind === 'chatgpt'
                  ? 'Import credentials or use a saved account'
                  : kind === 'grok' && state.accounts?.some((account) => account.kind === kind)
                    ? 'Use a saved account'
                    : undefined;
              if (guided && manualAction) {
                panel.append(
                  disclosure(
                    'Advanced',
                    button(manualAction, () => {
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
    intro.append(addButton);
    view.append(composer);
  }

  const pages = providerPages.get(context) ?? new Map<ProxyProviderFamily, number>();
  providerPages.set(context, pages);
  view.append(clientDefaults(state, api, componentContext()));

  const accountDeps = () => ({ state, api, context: componentContext() });
  // Credentials are maintained beside the routes that use them. Native
  // sign-ins are held by the proxy and managed in the provider section.
  function routeAccountTools(route: ProxyRoute, kind?: ProxyAccount['kind']) {
    if (
      !route.account ||
      state.accounts === undefined ||
      routeNativeProvider(route, state) ||
      nativeProvider(kind)
    )
      return;
    const account = state.accounts.find((a) => a.name === route.account);
    if (!account) return connectAccount(accountDeps(), route.account, kind);
    const usedBy = state.routes.filter((r) => r.account === account.name).map((r) => r.name);
    return accountControls(accountDeps(), account, usedBy, `route:${route.name}`);
  }

  function deleteProviderButton(group: ProviderGroup): HTMLButtonElement {
    const removeContext = componentContext();
    const remove = button('Delete provider', () => {
      if (blocked()) return;
      const consequence = deleteProviderConsequence(group, state);
      if (!window.confirm(consequence)) return;
      void removeContext.run(async () => {
        try {
          await api.deleteProvider(state.revision, group.id);
        } catch (error) {
          if (error instanceof ProxyAPIError && error.status === 409) {
            try {
              await context.refresh();
            } catch {
              // Preserve the conflict message.
            }
          }
          throw error;
        }
        open.delete(group.id);
        await context.refresh();
        context.announce(
          `Deleted provider ${group.id}. Its client grants were removed; dispatched requests may continue.`,
        );
      }, remove);
    });
    remove.className = 'danger';
    remove.setAttribute('aria-label', `Delete provider ${group.label}`);
    return remove;
  }

  function routeAdminActions(
    route: ProxyRoute,
    panel: HTMLElement,
    preset: ProxyPreset | undefined,
  ) {
    const rowContext = componentContext();
    const edit = (focusAccount = false) => {
      if (blocked()) return;
      if (editor?.row === panel) {
        focusWithin(editor.form);
        return;
      }
      closeEditor();
      const form = routeForm(
        state,
        api,
        rowContext,
        () => {
          if (blocked()) return;
          closeEditor();
          editButton.focus();
        },
        route,
        preset,
      );
      panel.append(form);
      editor = { row: panel, form };
      if (focusAccount) {
        const accountSelect = form.querySelector<HTMLSelectElement>('select');
        if (accountSelect) accountSelect.focus();
        else form.querySelector('input')?.focus();
      } else {
        form.querySelector('input')?.focus();
      }
    };
    const editButton = button('Edit', () => edit());
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
          state,
          state.routes.map((r) => (r === route ? { ...r, enabled: !r.enabled } : r)),
        );
        await context.refresh(`route:${route.name}`);
        context.announce(
          `${route.enabled ? 'Disabled' : 'Enabled'} route ${route.name}.${route.enabled ? ' New requests are blocked; dispatched requests may continue.' : ''}`,
        );
      }, toggle);
    });
    toggle.setAttribute('aria-label', `${route.enabled ? 'Disable' : 'Enable'} ${route.name}`);
    const controls = actions(editButton, toggle);
    if ((route.account && !routeNativeProvider(route, state)) || routeNeedsSecret(route)) {
      controls.append(
        button(route.account ? 'Change linked account' : 'Configure API key', () =>
          edit(!!route.account),
        ),
      );
    }
    return controls;
  }

  function renderProvider(group: ReturnType<typeof providerGroups>[number]): HTMLElement {
    const routes = group.routes.map((route) => ({
      route,
      preset: matchRoutePreset(route, state),
      display: routeSummary(route, state),
    }));
    const dot = el('span');
    dot.className = `provider-dot provider-dot-${group.status}`;
    dot.setAttribute('role', 'img');
    dot.setAttribute(
      'aria-label',
      `Status: ${group.status}${group.reason ? `, ${group.reason}` : ''}`,
    );
    const heading = el('h3', group.id);
    const statusText = el('span', group.reason);
    statusText.className = 'provider-status';
    const title = el('div', dot, heading);
    if (group.label !== group.id) title.append(el('span', group.label));
    title.append(statusText);
    title.className = 'provider-title';
    const list = el('ul');
    list.className = 'provider-routes';
    list.setAttribute('aria-label', `${group.label} routes`);
    const manage = disclosure('Manage');
    const details = el('div');
    details.className = 'provider-management-content';
    manage.append(details);
    manage.className = 'provider-manage';
    manage.open = open.has(group.id);
    manage.addEventListener('toggle', () => {
      if (manage.open) open.add(group.id);
      else open.delete(group.id);
    });

    details.append(providerAccess(group, state, api, componentContext()));

    const seenAccounts = new Set<string>();
    const seenNative = new Set<NativeProvider>();
    for (const { route, preset } of routes) {
      const native = routeNativeProvider(route, state);
      list.append(providerRouteLine(route));
      if (route.account && !seenAccounts.has(route.account) && !native) {
        seenAccounts.add(route.account);
        // Legacy Grok CLI routes no longer have a picker preset.
        const kind = preset?.account_kind ?? (route.profile === 'grok-bin' ? 'grok' : undefined);
        const account = routeAccountTools(route, kind);
        const signIn = providerRouteAction(route, state, api, componentContext());
        if (signIn || account) {
          const settings = el('div', el('h4', 'Account'));
          settings.className = 'provider-section provider-account-section';
          if (signIn) settings.append(signIn);
          if (account) settings.append(account);
          // Account controls belong to the provider, not to each child route.
          details.append(settings);
        }
      }
      if (!readonly && native && !seenNative.has(native)) {
        const setup = providerRouteAction(route, state, api, componentContext());
        if (setup) {
          seenNative.add(native);
          const section = el('div', el('h4', 'Shared subscription'), setup);
          section.className = 'provider-section provider-account-section';
          details.append(section);
        }
      }
    }

    const routesSection = el('div', el('h4', routes.length === 1 ? 'Route' : 'Routes'));
    routesSection.className = 'provider-section provider-routes-section';
    for (const { route, preset, display } of routes) {
      const panel = el('div');
      panel.className = 'provider-route-management';
      panel.dataset.route = route.name;
      const routeHeader = el('div', el('code', route.local_prefix));
      routeHeader.className = 'provider-route-header';
      if (!readonly) routeHeader.append(routeAdminActions(route, panel, preset));
      panel.append(routeHeader, display.advanced);
      routesSection.append(panel);
    }
    details.append(routesSection);

    if (group.family === 'inference')
      details.append(providerConfigSection(group, state, api, componentContext(), readonly));

    if (!readonly) {
      // Renaming is rare: keep the editor closed until asked for.
      const label = input(group.label);
      const labelForm = el('form', field('Provider label', label));
      submit(
        labelForm,
        componentContext(),
        async () => {
          await api.renameProviderLabel(state.revision, group.id, label.value.trim());
          await context.refresh(`route:${group.routes[0].name}`);
          context.announce(`Saved label for ${group.id}.`);
        },
        'Save label',
      );
      const rename = button('Rename', () => {
        if (blocked()) return;
        setRenaming(true);
        label.focus();
        label.select();
      });
      rename.setAttribute('aria-label', `Rename ${group.label}`);
      labelForm.append(
        button('Cancel', () => {
          setRenaming(false);
          rename.focus();
        }),
      );
      labelForm.className = 'provider-section provider-label-form';
      const setRenaming = (renaming: boolean) => {
        if (!renaming) label.value = group.label;
        labelForm.hidden = !renaming;
        rename.setAttribute('aria-expanded', String(renaming));
      };
      setRenaming(false);
      details.append(labelForm);
      const additions = missingCompanions(group, state).map((preset) => {
        const addRoute = button(`Add ${presetLabel(preset)} route`, () => {
          if (blocked()) return;
          closeEditor();
          const form = routeForm(
            state,
            api,
            componentContext(),
            () => {
              if (blocked()) return;
              closeEditor();
              addRoute.focus();
            },
            undefined,
            preset,
            group,
          );
          routesSection.append(form);
          editor = { row: details, form };
          form.querySelector('input')?.focus();
        });
        return addRoute;
      });
      const footer = el('div', actions(rename, ...additions), actions(deleteProviderButton(group)));
      footer.className = 'provider-manage-footer';
      details.append(footer);
    }

    const row = el('article', title, approvalSummary(group, state), list, manage);
    row.className = 'route-card route-accordion-item provider-card';
    row.setAttribute('aria-label', group.label);
    return row;
  }

  // Keep every disclosure containing a show-once token reachable until the
  // operator has copied or dismissed it (including native device launchers).
  view.addEventListener('click', (event) => {
    if (!(event.target instanceof Element)) return;
    const details = event.target.closest('summary')?.parentElement;
    if (
      details instanceof HTMLDetailsElement &&
      details.open &&
      (tokenOwner || context.hasProtectedToken?.())
    ) {
      event.preventDefault();
      context.announce('Finish saving or dismissing the one-time connection details first.');
    }
  });

  function familySection(family: ProxyProviderFamily, label: string, groups: ProviderGroup[]) {
    const section = el('section', el('h2', label));
    section.className = 'provider-family';
    section.setAttribute('aria-label', label);
    let currentPage = Math.min(pages.get(family) ?? 0, Math.ceil(groups.length / PAGE_SIZE) - 1);
    const routeList = el('div');
    routeList.className = 'route-list';
    const pagination = el('nav');
    pagination.className = 'route-pagination';
    pagination.setAttribute('aria-label', `${label} provider pagination`);
    section.append(routeList);
    function renderPage() {
      const start = currentPage * PAGE_SIZE;
      const end = Math.min(start + PAGE_SIZE, groups.length);
      routeList.replaceChildren(...groups.slice(start, end).map(renderProvider));
      const totalPages = Math.ceil(groups.length / PAGE_SIZE);
      if (totalPages <= 1) return;
      const goTo = (page: number) => {
        if (page === currentPage || blocked()) return;
        closeEditor();
        currentPage = page;
        pages.set(family, page);
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
        el('small', `Showing ${start + 1}–${end} of ${groups.length} providers`),
        controls,
      );
      if (!pagination.parentElement) section.append(pagination);
    }

    renderPage();
    return section;
  }

  if (groups.length) {
    for (const [family, label] of providerFamilies) {
      const entries = groups.filter((group) => group.family === family);
      if (entries.length) view.append(familySection(family, label, entries));
    }
  } else {
    view.append(
      card(
        'Add your first provider',
        el(
          'p',
          readonly
            ? 'No routes are defined in the proxy configuration file.'
            : 'Choose a provider preset or add a custom endpoint. You can set up client access after saving the route.',
        ),
      ),
    );
  }
  // Native sign-ins can outlive their routes; keep each reset reachable.
  if (!readonly) {
    for (const native of nativeProviders) {
      if (!state.accounts?.some((account) => account.kind === native.kind)) continue;
      if (state.routes.some((route) => routeNativeProvider(route, state) === native)) continue;
      const leftovers = card(
        native.label,
        el(
          'p',
          `Removes every ${native.shortName} route, device connection and saved proxy sign-in.`,
        ),
        actions(clearNativeSetup(native, state, api, componentContext())),
      );
      leftovers.classList.add(`${native.launcher}-leftovers`);
      view.append(leftovers);
    }
  }
  if (state.accounts !== undefined) {
    const unused = unusedAccountsView(accountDeps());
    if (unused) view.append(unused);
  }
  return view;
}
