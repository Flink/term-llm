import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, getByRole, queryByRole, waitFor } from '@testing-library/preact';
import type { UIContext } from './dom';
import type { ProviderGroup } from './provider-groups';
import { termLLMConfigSection, termLLMProxyYAML } from './client-config-snippet';

const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
  if (clipboardDescriptor) Object.defineProperty(navigator, 'clipboard', clipboardDescriptor);
  else Reflect.deleteProperty(navigator, 'clipboard');
});

function group(id: string, overrides: Partial<ProviderGroup> = {}): ProviderGroup {
  return {
    id,
    label: id,
    family: 'inference',
    status: 'green',
    reason: '',
    routes: [],
    ...overrides,
  };
}

function uiContext() {
  const announce = vi.fn();
  const context: UIContext = {
    run: async (action) => {
      await action();
      return true;
    },
    refresh: vi.fn(async () => undefined),
    announce,
    navigate: vi.fn(),
    protectToken: vi.fn(),
  };
  return { context, announce };
}

describe('termLLMProxyYAML', () => {
  it('formats a safe file-reference block by default and deduplicates provider IDs', () => {
    expect(termLLMProxyYAML('https://proxy.example.test', ['exa', 'chatgpt', 'exa'])).toBe(
      [
        'proxy:',
        '  url: https://proxy.example.test',
        '  token: file:///run/secrets/proxy-client-token',
        '  providers: [exa, chatgpt]',
      ].join('\n'),
    );
  });

  it('single-quotes literal tokens and unsafe YAML provider IDs safely', () => {
    expect(
      termLLMProxyYAML('https://proxy.example.test', ['cdck-frontier', 'team:api'], "sk'123"),
    ).toBe(
      [
        'proxy:',
        '  url: https://proxy.example.test',
        "  token: 'sk''123'",
        "  providers: [cdck-frontier, 'team:api']",
      ].join('\n'),
    );
  });
});

describe('termLLMConfigSection', () => {
  it('omits the section when no provider IDs are present', () => {
    const { context } = uiContext();
    expect(termLLMConfigSection('https://proxy.example.test', [], context)).toBeUndefined();
  });

  it('keeps the token out of DOM text, copies on demand, and refuses once cleared', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    const { context, announce } = uiContext();
    let liveToken = 'sk-tlp-live-secret';
    const fallback = vi.fn();
    const section = termLLMConfigSection('https://proxy.example.test', [group('exa')], context, {
      getToken: () => liveToken,
      onTokenFallback: fallback,
    })!;
    document.body.append(section);

    expect(section.textContent).not.toContain('sk-tlp-live-secret');
    expect(section).toHaveTextContent('~/.config/term-llm/config.yaml');
    expect(section.querySelector('pre code')?.textContent).toBe(
      [
        'proxy:',
        '  url: https://proxy.example.test',
        '  token: file:///run/secrets/proxy-client-token',
        '  providers: [exa]',
      ].join('\n'),
    );

    fireEvent.click(getByRole(section, 'button', { name: 'Copy with token' }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        [
          'proxy:',
          '  url: https://proxy.example.test',
          "  token: 'sk-tlp-live-secret'",
          '  providers: [exa]',
        ].join('\n'),
      ),
    );
    expect(fallback).not.toHaveBeenCalled();

    // Once the one-time token input is cleared on dismiss, Copy with token stays safe.
    liveToken = '';
    writeText.mockClear();
    fireEvent.click(getByRole(section, 'button', { name: 'Copy with token' }));
    await waitFor(() =>
      expect(announce).toHaveBeenLastCalledWith('Client token is no longer available.'),
    );
    expect(writeText).not.toHaveBeenCalled();
  });

  it('falls back cleanly when clipboard access is unavailable', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
    const { context, announce } = uiContext();
    const fallback = vi.fn();
    const section = termLLMConfigSection('https://proxy.example.test', [group('exa')], context, {
      getToken: () => 'sk-tlp-secret',
      onTokenFallback: fallback,
    })!;
    document.body.append(section);

    fireEvent.click(getByRole(section, 'button', { name: 'Copy YAML' }));
    await waitFor(() =>
      expect(announce).toHaveBeenLastCalledWith(
        'Clipboard unavailable. Copy the selected YAML manually.',
      ),
    );
    expect(window.getSelection()?.toString()).toContain('providers: [exa]');

    fireEvent.click(getByRole(section, 'button', { name: 'Copy with token' }));
    await waitFor(() =>
      expect(announce).toHaveBeenLastCalledWith(
        'Clipboard unavailable. Copy the YAML and client token manually.',
      ),
    );
    expect(fallback).toHaveBeenCalledOnce();
  });

  it('shows vision_via reminders only for vision providers not already approved', () => {
    const { context } = uiContext();
    const deepseek = group('cdck_deepseek', {
      label: 'DeepSeek',
      config: { vision_via: 'cdck_qwen:vl' },
    });
    const qwen = group('cdck_qwen', { label: 'Qwen VL' });

    const missingVision = termLLMConfigSection('https://proxy.example.test', [deepseek], context)!;
    expect(missingVision).toHaveTextContent(
      'This provider routes vision via cdck_qwen:vl. Approve cdck_qwen',
    );
    expect(queryByRole(missingVision, 'button', { name: 'Copy with token' })).toBeNull();

    const bothApproved = termLLMConfigSection(
      'https://proxy.example.test',
      [deepseek, qwen],
      context,
    )!;
    expect(bothApproved).not.toHaveTextContent('routes vision via');
  });
});
