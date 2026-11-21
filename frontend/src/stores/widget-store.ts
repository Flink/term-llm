import { batch, computed, signal } from '@preact/signals';
import type { WidgetMutationResponse } from '../api/endpoints';
import type { Widget } from '../domain/types';
import { array, recordValue } from './store-utils';
import type { AppStoreServices } from './app-store-services';

/** Owns optional widget discovery state. */
export class WidgetStore {
  readonly widgets = signal<Widget[]>([]);
  readonly loadErrors = signal<string[]>([]);
  readonly pending = signal<{ mount: string; action: 'stop' | 'toggle' } | null>(null);
  readonly sorted = computed(() =>
    [...this.widgets.value].sort((left, right) =>
      left.name.localeCompare(right.name, undefined, { sensitivity: 'base' }),
    ),
  );
  private revision = 0;
  private applied = 0;

  /** The widgets the launcher offers: all but those turned off in Settings. */
  readonly enabled = computed(() =>
    this.sorted.value.filter((widget) => widget.state !== 'disabled'),
  );

  constructor(private readonly services: AppStoreServices) {}

  /** Capture before a read, including reads that sideload widget status. */
  capture(): number {
    return ++this.revision;
  }

  applyRead(value: unknown, revision: number): void {
    if (revision >= this.applied && !this.pending.peek() && this.applySnapshot(value)) {
      this.applied = revision;
    }
  }

  private applySnapshot(value: unknown): boolean {
    const snapshot = recordValue(value);
    const source = Array.isArray(value) ? value : snapshot?.widgets;
    if (!Array.isArray(source)) return false;
    const widgets = array(source)
      .map((entry) => {
        const mount = String(entry.mount || entry.id || '').replace(/^\/+|\/+$/g, '');
        return {
          id: String(entry.id || mount),
          name: String(entry.title || entry.name || mount || 'Widget'),
          mount,
          description: String(entry.description || ''),
          state: String(entry.state || 'stopped'),
          error: String(entry.error || ''),
          url: mount
            ? `${this.services.config.prefix}/widgets/${encodeURIComponent(mount)}/`
            : String(entry.url || ''),
        };
      })
      .filter((entry) => entry.url);
    const errors = snapshot?.load_errors;
    batch(() => {
      this.widgets.value = widgets;
      this.loadErrors.value = Array.isArray(errors)
        ? errors.filter((error): error is string => typeof error === 'string' && !!error.trim())
        : [];
    });
    return true;
  }

  async load(): Promise<void> {
    if (this.pending.peek()) return;
    const revision = this.capture();
    try {
      this.applyRead(await this.services.endpoints.widgetStatus(), revision);
    } catch {
      /* Widgets are optional. */
    }
  }

  stop(mount: string): Promise<void> {
    return this.mutate(mount, 'stop', () => this.services.endpoints.stopWidget(mount));
  }

  /** Turns a widget off (the server stops it and won't start it) or back on. */
  setDisabled(mount: string, disabled: boolean): Promise<void> {
    return this.mutate(mount, 'toggle', () =>
      this.services.endpoints.setWidgetDisabled(mount, disabled),
    );
  }

  private async mutate(
    mount: string,
    action: 'stop' | 'toggle',
    request: () => Promise<WidgetMutationResponse>,
  ): Promise<void> {
    if (!mount || this.pending.peek()) return;
    this.pending.value = { mount, action };
    try {
      let result: WidgetMutationResponse;
      try {
        result = await request();
        if (result?.ok !== true) throw new Error('Widget change was not confirmed.');
      } catch (error) {
        // The server may have changed state despite the failure. Preserve the original error.
        await this.refreshAfterMutation().catch(() => undefined);
        throw error;
      }
      if (result.warning) this.services.toast(result.warning);
      if (!this.applySnapshot(result)) await this.refreshAfterMutation();
    } finally {
      this.applied = ++this.revision;
      this.pending.value = null;
    }
  }

  private async refreshAfterMutation(): Promise<void> {
    if (!this.applySnapshot(await this.services.endpoints.widgetStatus())) {
      throw new Error('Widget status was not confirmed.');
    }
  }
}
