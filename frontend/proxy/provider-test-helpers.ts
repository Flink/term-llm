// Test-only helpers for the Providers master–detail view.
export type ProviderTabID = 'access' | 'connection' | 'config' | 'settings';

/** The list row for the provider that owns `routeName`. */
export function providerItem(root: ParentNode, routeName: string): HTMLButtonElement {
  const item = [...root.querySelectorAll<HTMLButtonElement>('.provider-list-item')].find((node) =>
    node.dataset.routes?.split(' ').includes(routeName),
  );
  if (!item) throw new Error(`Missing provider for route: ${routeName}`);
  return item;
}

/** Selects the provider that owns `routeName`, shows `tab` and returns the detail pane. */
export function openProvider(
  root: ParentNode,
  routeName: string,
  tab: ProviderTabID = 'connection',
): HTMLElement {
  const item = providerItem(root, routeName);
  if (item.getAttribute('aria-current') !== 'true') item.click();
  const detail = root.querySelector<HTMLElement>('.provider-detail');
  if (!detail || detail.dataset.provider !== item.dataset.provider)
    throw new Error(`Provider ${item.dataset.provider} did not open`);
  const control = detail.querySelector<HTMLButtonElement>(`[role="tab"][data-tab="${tab}"]`);
  if (!control) throw new Error(`Missing ${tab} tab for ${item.dataset.provider}`);
  control.click();
  return detail;
}
