import { describe, expect, it } from 'vitest';
import {
  applyRuntimeToRequest,
  splitModelEffort,
  supportsFastMode,
  supportsReasoningMode,
} from './runtime';

it.each([
  ['model_XHIGH', '', 'model', 'xhigh'],
  ['model-high', ' MAX ', 'model', 'max'],
  ['model-high', 'custom', 'model-high', 'custom'],
  ['o3-mini-high', '', 'o3-mini-high', ''],
  ['-high', '', '-high', ''],
])('splits reasoning effort from %s with explicit effort %j', (input, explicit, model, effort) => {
  expect(splitModelEffort(input, explicit)).toEqual({ model, effort });
});

describe('runtime request selection', () => {
  it('omits reasoning.mode unless the selected model advertises support', () => {
    const body: Record<string, unknown> = {};

    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'chatgpt',
        model: 'gpt-5.6-sol',
        effort: 'high',
        reasoningMode: 'standard',
        fast: false,
      },
      {
        id: 'gpt-5.6-sol',
        name: 'gpt-5.6-sol',
        provider: 'chatgpt',
        efforts: ['low', 'medium', 'high'],
      },
    );

    expect(body).not.toHaveProperty('reasoning');
  });

  it('sends reasoning.mode when model metadata explicitly advertises it', () => {
    const model = {
      id: 'gpt-5.6-sol',
      name: 'gpt-5.6-sol',
      provider: 'openai',
      reasoning_modes: ['standard', 'pro'],
    };
    const body: Record<string, unknown> = {};

    expect(supportsReasoningMode(model, 'standard')).toBe(true);
    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'openai',
        model: 'gpt-5.6-sol',
        effort: 'high',
        reasoningMode: 'standard',
        fast: false,
      },
      model,
    );

    expect(body.reasoning).toEqual({ mode: 'standard' });
  });
  it('sends priority service tier only for models that advertise fast mode', () => {
    const model = {
      id: 'gpt-5.6-sol',
      name: 'gpt-5.6-sol',
      service_tiers: [{ id: 'priority', name: 'fast' }],
    };
    const body: Record<string, unknown> = {};

    expect(supportsFastMode(model)).toBe(true);
    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'chatgpt',
        model: model.id,
        effort: 'medium',
        reasoningMode: 'standard',
        fast: true,
      },
      model,
    );

    expect(body.service_tier).toBe('priority');
  });

  it('sends an explicit service-tier clear when fast mode is off', () => {
    const model = {
      id: 'gpt-5.6-sol',
      name: 'gpt-5.6-sol',
      additional_speed_tiers: ['fast'],
    };
    const body: Record<string, unknown> = {};

    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'chatgpt',
        model: model.id,
        effort: 'medium',
        reasoningMode: 'standard',
        fast: false,
      },
      model,
    );

    expect(body.service_tier).toBe('');
  });

  it('inherits provider-enforced fast mode without sending an override', () => {
    const model = {
      id: 'gpt-5.6-sol',
      name: 'gpt-5.6-sol',
      service_tiers: [{ id: 'priority' }],
    };
    const body: Record<string, unknown> = {};

    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'chatgpt',
        model: model.id,
        effort: 'medium',
        reasoningMode: 'standard',
        fast: false,
      },
      model,
      { id: 'chatgpt', name: 'chatgpt', service_tier: 'fast' },
    );

    expect(body).not.toHaveProperty('service_tier');
  });

  it('drops unsupported reasoning_effort when model metadata advertises its supported efforts', () => {
    const model = {
      id: 'claude-opus-5-5',
      name: 'claude-opus-5-5',
      provider: 'agy-bin',
      efforts: ['low', 'medium', 'high'],
    };
    const body: Record<string, unknown> = {};

    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'agy-bin',
        model: model.id,
        effort: 'xhigh',
        reasoningMode: 'standard',
        fast: false,
      },
      model,
    );

    expect(body.model).toBe('claude-opus-5-5');
    expect(body).not.toHaveProperty('reasoning_effort');
  });

  it('preserves supported reasoning_effort when model metadata advertises it', () => {
    const model = {
      id: 'gemini-4-neon',
      name: 'gemini-4-neon',
      provider: 'agy-bin',
      efforts: ['low', 'medium', 'high', 'xhigh'],
    };
    const body: Record<string, unknown> = {};

    applyRuntimeToRequest(
      body,
      {},
      {
        provider: 'agy-bin',
        model: model.id,
        effort: 'xhigh',
        reasoningMode: 'standard',
        fast: false,
      },
      model,
    );

    expect(body.model).toBe('gemini-4-neon');
    expect(body.reasoning_effort).toBe('xhigh');
  });
});
