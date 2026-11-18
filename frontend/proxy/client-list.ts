import type { ProxyCaller, ProxyRoute } from '../src/api/proxy-client';
import { badge, dateTime, el } from './dom';

const DAY = 24 * 60 * 60 * 1000;

/** Compact, scannable timestamp: "Today 2:28 PM", "Yesterday 9:40 AM", "Nov 16". */
export function shortDate(
  value: string | number | undefined,
  empty = 'Never',
  now = new Date(),
): string {
  if (!value) return empty;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return 'Unknown';
  const time = date.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  if (date.getTime() >= startOfToday && date.getTime() < startOfToday + DAY) return `Today ${time}`;
  if (date.getTime() >= startOfToday - DAY && date.getTime() < startOfToday)
    return `Yesterday ${time}`;
  return date.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    ...(date.getFullYear() === now.getFullYear() ? {} : { year: 'numeric' }),
  });
}

/**
 * Short ID suffixes for clients that share a name, so identical names stay
 * distinguishable. Derived from the public client ID, never the token.
 */
export function duplicateSuffixes(callers: ProxyCaller[]): Map<string, string> {
  const groups = new Map<string, ProxyCaller[]>();
  for (const caller of callers)
    groups.set(caller.name, [...(groups.get(caller.name) ?? []), caller]);
  const out = new Map<string, string>();
  for (const group of groups.values()) {
    if (group.length < 2) continue;
    const ids = group.map((caller) => caller.id.replace(/^c_/, ''));
    let length = 4;
    while (
      length < Math.max(...ids.map((id) => id.length)) &&
      new Set(ids.map((id) => id.slice(0, length))).size < ids.length
    )
      length++;
    group.forEach((caller, i) => out.set(caller.id, ids[i].slice(0, length)));
  }
  return out;
}

/** Most recently used first; never-used clients by newest creation. */
export function sortCallers(callers: ProxyCaller[]): ProxyCaller[] {
  const created = (caller: ProxyCaller) => new Date(caller.created).getTime() || 0;
  return [...callers].sort(
    (a, b) => (b.last_used ?? 0) - (a.last_used ?? 0) || created(b) - created(a),
  );
}

export function clientLabel(caller: ProxyCaller, suffixes: Map<string, string>): string {
  const suffix = suffixes.get(caller.id);
  return suffix ? `${caller.name} #${suffix}` : caller.name;
}

function nameNode(caller: ProxyCaller, suffixes: Map<string, string>): Node[] {
  const suffix = suffixes.get(caller.id);
  if (!suffix) return [document.createTextNode(caller.name)];
  const id = el('span', `#${suffix}`);
  id.className = 'client-id';
  return [document.createTextNode(`${caller.name} `), id];
}

function grantBadges(caller: ProxyCaller): HTMLElement {
  const grants = [...caller.grants].sort();
  const node = el(
    'div',
    ...(grants.length
      ? grants.map((grant) => badge(grant, caller.revoked ? '' : 'credential'))
      : [badge('No routes')]),
  );
  node.className = 'route-badges client-grants';
  return node;
}

function lastUsed(caller: ProxyCaller, now: Date): HTMLElement {
  const label = el('span', 'Last used ');
  label.className = 'visually-hidden';
  const node = el('span', label, shortDate(caller.last_used, 'Never', now));
  node.className = 'client-last';
  if (!caller.last_used) node.classList.add('never');
  else {
    node.title = dateTime(caller.last_used);
    if (!caller.revoked && now.getTime() - caller.last_used < DAY) node.classList.add('recent');
  }
  return node;
}

export function clientListHeader(): HTMLElement {
  const head = el('div', el('span', 'Client'), el('span', 'Routes'), el('span', 'Last used'));
  head.className = 'client-list-head';
  head.setAttribute('aria-hidden', 'true');
  return head;
}

/** A one-line client summary; active rows toggle the given detail panel. */
export function clientRow(
  caller: ProxyCaller,
  suffixes: Map<string, string>,
  keyNames: ReadonlyMap<string, string>,
  detail?: { panel: HTMLElement; toggle: (open: boolean) => void },
  now = new Date(),
): HTMLElement {
  const heading = el('h2');
  heading.tabIndex = -1;
  heading.className = 'client-name';
  const metadata = [];
  if (caller.created_by) {
    const keyName = keyNames.get(caller.created_by);
    metadata.push(keyName ? `Created by API key ‹${keyName}›` : 'Created by a revoked API key');
  }
  if (caller.external_id) metadata.push(`External ID: ${caller.external_id}`);
  const identity = metadata.length
    ? el('div', heading, ...metadata.map((text) => el('p', el('small', text))))
    : heading;
  const main = el('div', identity, grantBadges(caller), lastUsed(caller, now));
  main.className = 'client-row-main';
  const row = el('article', main);
  row.className = 'client-row';
  row.dataset.focus = `caller:${caller.id}`;
  if (!detail) {
    heading.append(...nameNode(caller, suffixes));
    return row;
  }
  const toggle = el('button', ...nameNode(caller, suffixes));
  toggle.type = 'button';
  toggle.className = 'client-toggle';
  toggle.setAttribute('aria-expanded', 'false');
  detail.panel.id ||= `client-detail-${caller.id}`;
  detail.panel.hidden = true;
  toggle.setAttribute('aria-controls', detail.panel.id);
  toggle.addEventListener('click', () => detail.toggle(!row.classList.contains('expanded')));
  heading.append(toggle);
  const chevron = el('span');
  chevron.className = 'client-chevron';
  chevron.setAttribute('aria-hidden', 'true');
  main.append(chevron);
  row.append(detail.panel);
  return row;
}

export function setRowExpanded(row: HTMLElement, open: boolean): void {
  const panel = row.querySelector<HTMLElement>('.client-detail');
  if (panel) panel.hidden = !open;
  row.classList.toggle('expanded', open);
  row.querySelector('.client-toggle')?.setAttribute('aria-expanded', String(open));
}

/** Pill-style grant checkboxes; disabled routes stay selectable but labelled. */
export function grantChips(
  routes: ProxyRoute[],
  selected: string[],
  legend: string,
): { fields: HTMLFieldSetElement; selected: () => string[]; reset: () => void } {
  const choices = routes.map((route) => {
    const control = el('input');
    control.type = 'checkbox';
    control.checked = selected.includes(route.name);
    const label = el('label', control, route.name + (route.enabled ? '' : ' (disabled)'));
    label.className = 'check grant-chip';
    return { route, control, label };
  });
  const chips = el('div', ...choices.map((choice) => choice.label));
  chips.className = 'grant-chips';
  const fields = el('fieldset', el('legend', legend), chips);
  fields.className = 'client-grant-fields';
  return {
    fields,
    selected: () => choices.filter((c) => c.control.checked).map((c) => c.route.name),
    reset: () => choices.forEach((c) => (c.control.checked = selected.includes(c.route.name))),
  };
}
