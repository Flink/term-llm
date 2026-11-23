import type { ProxyClient, ProxyState } from '../src/api/proxy-client';
import { adminKeysView } from './admin-keys';
import { button, card, download, el, field, input, select, submit, type UIContext } from './dom';

export function adminView(state: ProxyState, api: ProxyClient, context: UIContext): HTMLElement {
  const readonly = state.mode === 'file';
  const section = el(
    'div',
    card(
      'Admin',
      el(
        'p',
        'This is not a full backup: route YAML excludes credentials, client tokens and passkeys.',
      ),
    ),
  );
  section.firstElementChild?.classList.add('page-intro');
  section.append(adminKeysView(state, api, context));
  const exportButton = button('Download routes.yaml', () => {
    void context.run(async () => {
      download('routes.yaml', await api.exportYAML(), 'text/yaml');
      context.toast('Route configuration downloaded. Secret values are not included.', 'success');
    }, exportButton);
  });
  section.append(
    card(
      'Download configuration',
      el('p', 'Export route definitions and secret references for --config.'),
      exportButton,
    ),
  );
  if (readonly) {
    section.append(el('p', 'Edit that file and use Reload file to apply --config changes.'));
    return section;
  }
  const file = input('', 'file');
  file.accept = '.yaml,.yml,application/yaml,text/yaml';
  const fileField = field('YAML file', file, 'Choose a configuration file, up to 1 MiB.');
  const yaml = el('textarea');
  yaml.rows = 6;
  yaml.spellcheck = false;
  const source = select(
    [
      ['file', 'File'],
      ['paste', 'Paste YAML'],
    ],
    'file',
  );
  const pasteField = field('Route configuration YAML', yaml);
  pasteField.hidden = true;
  source.addEventListener('change', () => {
    fileField.hidden = source.value !== 'file';
    pasteField.hidden = source.value !== 'paste';
  });
  const form = el('form', field('Import source', source), fileField, pasteField);
  const tokenGuard = () => {
    if (!context.hasProtectedToken?.()) return false;
    context.announce('Save or dismiss the one-time API key token before replacing routes.');
    return true;
  };
  const replace = submit(
    form,
    context,
    async () => {
      if (tokenGuard()) return;
      let contents: string;
      if (source.value === 'paste') {
        contents = yaml.value;
      } else {
        const selected = file.files?.[0];
        if (!selected) throw new Error('Choose a YAML file, or select Paste YAML.');
        if (selected.size > 1 << 20)
          throw new Error('Configuration files must be 1 MiB or smaller.');
        try {
          contents = await selected.text();
        } catch {
          throw new Error(
            'Could not read the configuration file. Choose it again or paste the YAML instead.',
          );
        }
      }
      if (!contents.trim())
        throw new Error(
          'The configuration is empty. Choose a file or paste YAML containing route definitions.',
        );
      if (new TextEncoder().encode(contents).length > 1 << 20)
        throw new Error('Configuration must be 1 MiB or smaller.');
      if (tokenGuard()) return;
      if (
        !window.confirm(
          'Replace routes? Removed or authority-changed routes lose client grants; dispatched requests may continue. Credentials, client tokens and passkeys are not restored.',
        )
      )
        return;
      await api.importYAML(state.revision, contents);
      await context.refresh();
      context.announce('Route configuration replaced. Review client grants.');
    },
    'Replace routes from YAML',
  );
  replace.className = 'danger';
  section.append(
    card(
      'Replace route configuration',
      el('p', 'Replaces routes only; removed or authority-changed routes lose client grants.'),
      form,
    ),
  );
  return section;
}
