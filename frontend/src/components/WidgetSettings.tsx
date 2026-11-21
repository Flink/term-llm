import { useStore } from '../app/context';
import type { Widget } from '../domain/types';
import { ToggleSwitch } from './ToggleSwitch';
import { widgetDetail, WidgetLoadErrors } from './widget-details';

/** On/off switches for this server's widgets; one turned off is stopped and left out of the launcher. */
export function WidgetSettings() {
  const store = useStore();
  const { sorted, loadErrors } = store.widgetStore;
  const pending = store.widgetStore.pending.value;
  const toggle = async (widget: Widget, on: boolean) => {
    if (!widget.mount) return;
    try {
      await store.widgetStore.setDisabled(widget.mount, !on);
    } catch (error) {
      store.toast(error, 'error');
    }
  };
  return (
    <div class="settings-panel-body">
      <WidgetLoadErrors errors={loadErrors.value} />
      {sorted.value.map((widget) => (
        <div class="settings-field" key={widget.id}>
          <label class="settings-toggle">
            <span>
              <span class="settings-label settings-label-inline">{widget.name}</span>{' '}
              <span class="settings-help">{widgetDetail(widget)}</span>
            </span>
            <ToggleSwitch
              checked={widget.state !== 'disabled'}
              disabled={Boolean(pending) || !widget.mount}
              onChange={(event) => {
                const on = event.currentTarget.checked;
                // Keep displaying server state while the write (and any reconciliation) runs.
                event.currentTarget.checked = widget.state !== 'disabled';
                void toggle(widget, on);
              }}
            />
          </label>
          {widget.state === 'disabled' && widget.error && (
            <p class="settings-help" role="status">
              Turned off, but the process did not stop: {widget.error}
            </p>
          )}
          {pending?.action === 'toggle' && pending.mount === widget.mount && (
            <span class="settings-help" role="status">
              Updating {widget.name}…
            </span>
          )}
        </div>
      ))}
    </div>
  );
}
