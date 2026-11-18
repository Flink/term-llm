import type {
  ProxyAPIEquivalent,
  ProxyClient,
  ProxyMoney,
  ProxyState,
  MoneyPeriod,
} from '../src/api/proxy-client';
import { moneySettingsView } from './money-settings';
import { button, card, disclosure, el, type UIContext } from './dom';
import { timelineView } from './timeline';

// Format only. Classification, source selection, aggregation and budgets belong
// to the backend; a missing amount must never be formatted as a zero.
export function moneyAmount(micros: number, currency: string): string {
  if (!Number.isSafeInteger(micros) || !/^[A-Z]{3}$/.test(currency)) return 'Amount unavailable';
  const value = micros / 1_000_000;
  const prefixes: Record<string, string> = {
    USD: 'US$',
    AUD: 'A$',
    CAD: 'CA$',
    NZD: 'NZ$',
    EUR: '€',
    GBP: '£',
  };
  return `${prefixes[currency] ?? currency + ' '}${value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: value !== 0 && Math.abs(value) < 0.01 ? 6 : 2,
  })}`;
}
export function moneyLabel(money?: ProxyMoney): string {
  if (!money) return 'Not measured';
  if (money.kind === 'no_charge_expected') return 'Blocked before dispatch · no charge expected';
  if (money.billing === 'plan')
    return money.kind === 'included'
      ? 'Measured plan activity · extra charges not reported'
      : 'Plan activity not fully measured · extra charges not reported';
  if (money.billing === 'local') return 'No provider charge';
  if (money.billing === 'unspecified') return 'Billing not specified';
  if (['charged', 'estimated', 'estimated_floor'].includes(money.kind)) {
    if (money.amount_micros === undefined || !money.currency) return 'Amount unavailable';
    const amount = moneyAmount(money.amount_micros, money.currency);
    if (money.kind === 'estimated') return `About ${amount}`;
    if (money.kind === 'estimated_floor') return `At least ${amount}`;
    return `${amount} · charged`;
  }
  const labels: Partial<Record<ProxyMoney['kind'], string>> = {
    unpriced: 'No price yet',
    unmeasured: "Couldn't measure",
    no_charge_expected: 'No charge expected',
  };
  return labels[money.kind] ?? 'Not measured';
}
export const notAvailable = 'Not available';
export const apiEquivalentNote = 'API estimate, not spend or savings.';
export function apiEquivalentLabel(estimate?: ProxyAPIEquivalent): string {
  if (!estimate || !['estimated', 'estimated_floor'].includes(estimate.kind)) return notAvailable;
  return moneyLabel({ ...estimate, billing: 'pay_as_you_go' });
}
export function providerLabel(name: string, state?: ProxyState): string {
  const route = state?.routes.find((route) => route.name === name);
  const kind = route?.account && state?.accounts?.find((a) => a.name === route.account)?.kind;
  const labels: Record<string, string> = {
    chatgpt: 'ChatGPT',
    'claude-code': 'Claude',
    'claude-bin': 'Claude',
    anthropic: 'Claude',
    'grok-bin': 'Grok',
    grok: 'Grok',
    xai: 'Grok',
    openai: 'OpenAI',
    copilot: 'GitHub Copilot',
    ollama: 'Ollama',
    unknown: 'Not identified',
  };
  const preset = state?.presets?.find((preset) => preset.upstream === route?.upstream);
  return (kind && labels[kind]) || labels[name] || (preset && labels[preset.id]) || name;
}
function planPeriod(label: string, period: MoneyPeriod): HTMLElement {
  const totals = period.api_equivalent_totals ?? [];
  const coverage = [
    period.api_equivalent_covered ? `${period.api_equivalent_covered.toLocaleString()} priced` : '',
    period.api_equivalent_unpriced
      ? `${period.api_equivalent_unpriced.toLocaleString()} unpriced`
      : '',
  ].filter(Boolean);
  return el(
    'p',
    el('strong', label),
    ': ',
    totals.length
      ? totals
          .map(
            (total) =>
              `${total.qualifier === 'at_least' ? '≥' : '≈'} ${moneyAmount(total.total_micros, total.currency)}`,
          )
          .join(' · ')
      : 'No estimate',
    ...(coverage.length ? [el('small', ` · ${coverage.join(' · ')}`)] : []),
  );
}
function planSources(label: string, period: MoneyPeriod): HTMLElement[] {
  return (period.api_equivalent_totals ?? []).flatMap((total) =>
    (total.sources ?? []).flatMap(({ source, asof }) =>
      source || asof
        ? [el('p', `${label} · ${total.currency}: ${[source, asof].filter(Boolean).join(' · ')}`)]
        : [],
    ),
  );
}
function planEstimatesView(api: ProxyClient, context: UIContext): HTMLDetailsElement {
  const body = el('div');
  const view = disclosure('Plan estimates at API rates', body);
  let loaded = false;
  let loading = false;
  async function load() {
    if (loading) return;
    loading = true;
    body.replaceChildren(el('p', 'Loading…'));
    const ok = await context.run(async () => {
      const summary = await api.summary();
      if (!view.isConnected) return;
      const sources = [
        ...planSources(summary.month_label, summary.month),
        ...planSources('Today', summary.today),
      ];
      body.replaceChildren(
        planPeriod(summary.month_label, summary.month),
        planPeriod('Today', summary.today),
        ...(sources.length ? [disclosure('Price details', ...sources)] : []),
      );
      loaded = true;
    }, body);
    loading = false;
    if (!ok && view.isConnected)
      body.replaceChildren(
        el('p', 'Plan estimates unavailable.'),
        button('Retry plan estimates', () => void load()),
      );
  }
  view.addEventListener('toggle', () => {
    if (view.open && !loaded) void load();
  });
  return view;
}
export function homeView(state: ProxyState, api: ProxyClient, context: UIContext): HTMLElement {
  const view = el('div', el('p', 'Loading chart…'));
  async function load() {
    view.setAttribute('aria-busy', 'true');
    const ok = await context.run(async () => {
      const browserTimezone = (() => {
        try {
          return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
        } catch {
          return undefined;
        }
      })();
      const timeline = await api.timeline('30d', 'total', browserTimezone);
      if (!view.isConnected) return;
      const details = el(
        'div',
        planEstimatesView(api, context),
        moneySettingsView(state, api, context),
      );
      details.className = 'home-details';
      view.replaceChildren(
        timelineView(timeline, (range, group) => api.timeline(range, group, browserTimezone)),
        details,
      );
    });
    if (!view.isConnected) return;
    view.setAttribute('aria-busy', 'false');
    if (!ok)
      view.replaceChildren(
        card(
          'Chart unavailable',
          button('Retry', () => {
            void load();
          }),
        ),
      );
  }
  queueMicrotask(() => {
    if (view.isConnected) void load();
  });
  return view;
}
