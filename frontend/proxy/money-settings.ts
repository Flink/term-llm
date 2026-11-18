import type {
  ProxyClient,
  ProxyMoneyRate,
  ProxyMoneySettings,
  ProxyState,
} from '../src/api/proxy-client';
import { button, disclosure, el, field, input, submit, type UIContext } from './dom';

export function budgetMicros(value: string): number | undefined {
  const text = value.trim();
  if (!text) return undefined;
  if (!/^\d+(?:\.\d{1,6})?$/.test(text))
    throw new Error('Use a non-negative amount with up to six decimals.');
  const [whole, fraction = ''] = text.split('.');
  const amount = BigInt(whole!) * 1000000n + BigInt(fraction.padEnd(6, '0'));
  if (amount > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('Budget is too large.');
  return Number(amount);
}
function budgetDecimal(micros: number): string {
  const n = BigInt(micros);
  return `${n / 1000000n}.${String(n % 1000000n).padStart(6, '0')}`;
}
export function parseMoneyRates(raw: string): ProxyMoneyRate[] {
  let rows: unknown;
  try {
    rows = JSON.parse(raw);
  } catch {
    throw new Error('Price cards must be a valid JSON array.');
  }
  if (!Array.isArray(rows) || rows.length > 256)
    throw new Error('Use an array of at most 256 cards.');
  const textFields = ['provider', 'model', 'tier', 'currency', 'source', 'asof'];
  const prices = [
    'input_micros_per_million',
    'output_micros_per_million',
    'cache_read_micros_per_million',
    'cache_write_micros_per_million',
  ];
  const seen = new Set<string>();
  for (const row of rows) {
    if (!row || typeof row !== 'object' || Array.isArray(row))
      throw new Error('Each card must be an object.');
    const r = row as Record<string, unknown>;
    for (const [key, value] of Object.entries(r)) {
      if (textFields.includes(key)) {
        if (typeof value !== 'string' || value.length > 512)
          throw new Error('Card metadata must be text.');
      } else if (!prices.includes(key) || !Number.isSafeInteger(value) || (value as number) < 0)
        throw new Error(
          'Prices must be non-negative integer micros; unknown fields are not accepted.',
        );
    }
    if (
      textFields.some((key) => typeof r[key] !== 'string') ||
      !r.provider ||
      !r.model ||
      !r.source ||
      !/^[A-Z]{3}$/.test(String(r.currency)) ||
      !/^\d{4}-\d{2}-\d{2}$/.test(String(r.asof)) ||
      !prices.slice(0, 2).every((key) => Number.isSafeInteger(r[key]))
    )
      throw new Error(
        'Include exact provider/model/tier, ISO currency, source, date (YYYY-MM-DD) and input/output prices.',
      );
    const date = new Date(`${r.asof}T00:00:00Z`);
    if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 10) !== r.asof)
      throw new Error('Use a real price-card date.');
    const key = JSON.stringify([r.provider, r.model, r.tier]);
    if (seen.has(key)) throw new Error('Duplicate provider/model/tier price card.');
    seen.add(key);
  }
  return rows as ProxyMoneyRate[];
}

// Settings load only when opened; an unavailable editor never hides the chart.
export function moneySettingsView(
  state: ProxyState,
  api: ProxyClient,
  context: UIContext,
): HTMLElement {
  const body = el('div');
  const view = disclosure('Money settings', body);
  view.className = 'money-settings';
  let loaded = false;
  let loading = false;
  async function load() {
    if (loading) return;
    loading = true;
    body.setAttribute('aria-busy', 'true');
    body.replaceChildren(el('p', 'Loading money settings…'));
    const ok = await context.run(async () => {
      const settings = await api.moneySettings();
      if (!view.isConnected) return;
      render(settings);
      loaded = true;
    });
    loading = false;
    body.setAttribute('aria-busy', 'false');
    if (!ok && view.isConnected)
      body.replaceChildren(
        el('p', 'Settings unavailable.'),
        button('Retry money settings', () => {
          void load();
        }),
      );
  }
  function render(settings: ProxyMoneySettings) {
    const timezone = input(settings.timezone || 'UTC');
    timezone.placeholder = 'Australia/Sydney';
    const currency = input(settings.budget?.currency);
    currency.placeholder = 'USD';
    currency.maxLength = 3;
    const amount = input(settings.budget ? budgetDecimal(settings.budget.limit_micros) : '');
    amount.inputMode = 'decimal';
    const rates = el('textarea');
    rates.rows = 9;
    rates.maxLength = 262144;
    rates.spellcheck = false;
    rates.value = JSON.stringify(settings.rates ?? [], null, 2);
    const rateDetails = disclosure(
      'Price overrides',
      el('p', 'Future requests only. Rates are currency micros per million tokens.'),
      field('Price cards JSON', rates),
    );
    const basicFields = el(
      'div',
      field('Billing timezone', timezone, 'Blank uses UTC.'),
      field('Budget currency', currency),
      field('Monthly alert', amount, 'Warning only. Leave blank to turn off.'),
    );
    basicFields.className = 'money-settings-fields';
    const form = el('form', basicFields, rateDetails);
    form.addEventListener('submit', (event) => event.preventDefault());
    if (state.mode === 'file') {
      form
        .querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input,textarea')
        .forEach((control) => {
          control.readOnly = true;
        });
      form.prepend(el('p', 'Edit the config file, then reload.'));
    } else
      submit(
        form,
        context,
        async () => {
          const limit = budgetMicros(amount.value);
          const code = currency.value.trim().toUpperCase();
          if (limit !== undefined && !/^[A-Z]{3}$/.test(code))
            throw new Error('Enter the budget’s three-letter ISO currency code.');
          const next: ProxyMoneySettings = {
            ...settings,
            timezone: timezone.value.trim(),
            rates: parseMoneyRates(rates.value),
            ...(limit === undefined
              ? { budget: undefined }
              : { budget: { currency: code, limit_micros: limit } }),
          };
          await api.money(next);
          await context.refresh();
          context.announce('Money settings saved.');
        },
        'Save',
      );
    body.replaceChildren(form);
  }
  view.addEventListener('toggle', () => {
    if (view.open && !loaded) void load();
  });
  return view;
}
