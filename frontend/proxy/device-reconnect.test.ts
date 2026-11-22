import { fireEvent, getByRole } from '@testing-library/preact';
import { describe, expect, it, vi } from 'vitest';
import {
  ProxyClient,
  type ProxyAccount,
  type ProxyRoute,
  type ProxyState,
} from '../src/api/proxy-client';
import type { UIContext } from './dom';
import { providerRouteAction } from './provider-onboarding';

function action(
  mode: ProxyState['mode'],
  eligibility?: ProxyAccount['device_reconnect'],
  kind: ProxyAccount['kind'] = 'grok',
  accountState = 'needs_login',
) {
  const route = { account: 'subscription', name: 'work' } as ProxyRoute;
  const account: ProxyAccount = {
    name: 'subscription',
    kind,
    state: accountState,
    generation: 2,
    updated: 1,
    device_reconnect: eligibility,
  };
  const state = { mode, accounts: [account], routes: [route] } as ProxyState;
  const api = new ProxyClient(vi.fn());
  const start = vi.spyOn(api, 'startDeviceSetup').mockRejectedValue(new Error('offline'));
  const context = {
    run: async (fn: () => Promise<void>) => {
      await fn().catch(() => {});
      return false;
    },
  } as UIContext;
  return { view: providerRouteAction(route, state, api, context), start };
}

describe('existing account device reconnect', () => {
  it.each(['chatgpt', 'grok'] as const)(
    'file-managed %s submits only the attached account',
    (kind) => {
      const { view, start } = action('file', 'eligible', kind);
      document.body.append(view!);
      fireEvent.click(getByRole(view!, 'button', { name: /Reconnect/ }));
      expect(start).toHaveBeenCalledExactlyOnceWith(kind, { account: 'subscription' });
      view!.remove();
    },
  );
  it.each(['file', 'ui'] as const)(
    '%s offers a new provider and fresh approval for an unpinned identity',
    (mode) => {
      const { view } = action(mode, 'missing_identity');
      expect(view?.querySelector('button')).toBeNull();
      expect(view?.textContent).toMatch(
        /original account identity.*new provider.*approve clients/i,
      );
      expect(view?.textContent?.includes('operator')).toBe(mode === 'file');
    },
  );
  it.each(['ui', 'file'] as const)(
    '%s keeps custom accounts on their existing provider',
    (mode) => {
      expect(action(mode, 'custom_settings', 'grok', 'ready').view).toBeUndefined();
      const { view } = action(mode, 'custom_settings');
      expect(view?.querySelector('button')).toBeNull();
      expect(view?.textContent).toContain('Replace credentials');
      expect(view?.textContent).not.toMatch(/new provider|approve clients/i);
    },
  );
  it('does not guess how to reconnect a file-managed account with unknown eligibility', () => {
    const { view } = action('file');
    expect(view?.querySelector('button')).toBeNull();
    expect(view?.textContent).toContain('operator');
  });
  it('does not expose native launcher setup in file mode', () => {
    expect(action('file', undefined, 'claude-bin').view).toBeUndefined();
  });
});
