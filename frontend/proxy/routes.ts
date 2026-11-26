import { type ProxyClient, type ProxyState } from '../src/api/proxy-client';
import { unusedAccountsView } from './accounts';
import { nativeProvider, nativeProviders, routeNativeProvider } from './native';
import { actions, button, card, disclosure, el, type UIContext } from './dom';
import { providerFamilies, providerGroups, type ProviderGroup } from './provider-groups';
import { clearNativeSetup, providerOnboarding } from './provider-onboarding';
import { providerDetail, type RouteEditorSlot } from './provider-detail';
import { FILTER_THRESHOLD, matchesFilter, providerList } from './provider-list';
import { routeForm } from './route-form';
import { providerForm } from './provider-form';
import { clientDefaults } from './provider-defaults';
import { routePresetPicker } from './route-presets';

/** Selection survives refreshes, which rebuild the whole view. */
interface ProviderPreferences {
  selected?: string;
  tab: string;
  filter: string;
}
const preferences = new WeakMap<UIContext, ProviderPreferences>();
const DETAIL_ID = 'provider-detail';

/**
 * The provider a refresh's focus key points at: `route:<name>` after a save,
 * `native:<launcher>` after shared native setup. Other keys select nothing.
 */
function focusedProvider(groups: ProviderGroup[], state: ProxyState, focusKey?: string) {
  const [kind, name] = focusKey?.split(/:(.*)/s) ?? [];
  if (kind === 'route') {
    return groups.find((group) => group.routes.some((route) => route.name === name));
  }
  if (kind === 'native') {
    return groups.find((group) =>
      group.routes.some((route) => routeNativeProvider(route, state)?.launcher === name),
    );
  }
  return undefined;
}

export function routesView(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
  focusKey?: string,
): HTMLElement {
  const readonly = state.mode === 'file';
  const view = el('div');
  const groups = providerGroups(state);
  const prefs = preferences.get(context) ?? { tab: 'access', filter: '' };
  preferences.set(context, prefs);
  const previous = prefs.selected;
  // Only the refresh that renders this view can move the selection, so a plain
  // Refresh (no key) or an unrelated save never jumps to another provider.
  const target = focusedProvider(groups, state, focusKey);
  if (target && target.id !== previous) {
    prefs.selected = target.id;
    // Another provider's route was saved: that is setup, which continues on Access.
    prefs.tab = 'access';
  }
  // Default to the first row the list shows, which is grouped by family.
  const ordered = providerFamilies.flatMap(([family]) =>
    groups.filter((group) => group.family === family),
  );
  const selectedGroup =
    groups.find((group) => group.id === prefs.selected) ?? ordered[0] ?? groups[0];
  prefs.selected = selectedGroup?.id;
  // Never keep a filter that hides the provider just selected for the operator.
  if (
    groups.length < FILTER_THRESHOLD ||
    (selectedGroup && previous !== selectedGroup.id && !matchesFilter(selectedGroup, prefs.filter))
  )
    prefs.filter = '';

  const heading = el('h2', 'Providers');
  heading.tabIndex = -1;
  const attention = groups.filter((group) => group.status === 'red' || group.status === 'orange');
  const summary = el(
    'p',
    `${groups.length} ${groups.length === 1 ? 'provider' : 'providers'}${attention.length ? ` · ${attention.length} ${attention.length === 1 ? 'needs' : 'need'} attention` : ''}`,
  );
  const introActions = el('div');
  introActions.className = 'providers-intro-actions';
  const intro = el('section', el('div', heading, groups.length ? summary : ''), introActions);
  intro.className = 'page-intro providers-intro';
  view.append(intro);

  // The app guards navigation. Within Providers, an operation must not replace
  // a component that is issuing or displaying a show-once client token.
  let tokenOwner: symbol | undefined;
  const operations = new Map<symbol, number>();
  const tokenShown = (owner?: symbol) => {
    if ((tokenOwner || context.hasProtectedToken?.()) && (!owner || tokenOwner !== owner)) {
      context.announce('Finish saving or dismissing the one-time connection details first.');
      return true;
    }
    return false;
  };
  const blocked = (owner?: symbol) => {
    if (tokenShown(owner)) return true;
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
  const editor: RouteEditorSlot = {
    close() {
      this.current?.form.remove();
      this.current = undefined;
    },
  };
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
        editor.close();
        composer.append(
          routePresetPicker(
            state.presets ?? [],
            (preset) => {
              if (blocked()) return;
              const formContext = componentContext();
              const manualForm = () =>
                preset
                  ? card(
                      `Set up ${preset.label}`,
                      providerForm(preset, state, api, formContext, close),
                    )
                  : routeForm(state, api, formContext, close);
              const guided = preset && providerOnboarding(preset, state, api, formContext, close);
              const panel =
                guided && preset ? card(`Set up ${preset.label}`, guided) : manualForm();
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
    introActions.append(addButton);
    view.append(composer);
  }

  view.append(clientDefaults(state, api, componentContext()));

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

  if (groups.length) {
    const detailSlot = el('div');
    detailSlot.className = 'provider-detail-slot';
    const renderDetail = () => {
      const group = groups.find((entry) => entry.id === prefs.selected);
      if (!group) return;
      const detail = providerDetail(group, {
        state,
        api,
        context,
        readonly,
        componentContext,
        blocked: () => blocked(),
        editor,
        tab: prefs.tab,
        onTab(id) {
          // Hiding a show-once token behind another tab invites losing it.
          if (tokenShown()) return false;
          prefs.tab = id;
          return true;
        },
        onDeleted(id) {
          if (prefs.selected === id) prefs.selected = undefined;
        },
      });
      detail.id = DETAIL_ID;
      detailSlot.replaceChildren(detail);
    };
    const list = providerList({
      groups,
      state,
      selected: prefs.selected,
      filter: prefs.filter,
      detailID: DETAIL_ID,
      onSelect(id) {
        if (blocked()) return false;
        editor.close();
        prefs.selected = id;
        for (const item of list.querySelectorAll<HTMLElement>('[data-provider]')) {
          if (item.dataset.provider === id) item.setAttribute('aria-current', 'true');
          else item.removeAttribute('aria-current');
        }
        renderDetail();
        // Stacked on narrow screens, the detail sits below the list: move there.
        if (detailSlot.getBoundingClientRect().top >= list.getBoundingClientRect().bottom) {
          detailSlot.scrollIntoView?.({ block: 'start' });
          detailSlot.querySelector<HTMLElement>('h2')?.focus({ preventScroll: true });
        }
        return true;
      },
      onFilter(value) {
        prefs.filter = value;
      },
    });
    renderDetail();
    const workspace = el('div', list, detailSlot);
    workspace.className = 'provider-workspace';
    view.append(workspace);
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
    const unused = unusedAccountsView({ state, api, context: componentContext() });
    if (unused) view.append(unused);
  }
  return view;
}
