import type { ProxyNativeSignInClient, ProxyRoute, ProxyState } from '../src/api/proxy-client';
import { clientLabel, duplicateSuffixes, sortCallers } from './client-list';
import { actions, button, el, field, input, select, submit, type UIContext } from './dom';
import type { NativeProvider } from './native';

/** The client a shared sign-in arms, with the name shown in its instructions. */
export interface NativeSignInChoice {
  client: ProxyNativeSignInClient;
  label: string;
}

const newClient = 'new';

/**
 * Shared sign-in runs from a real client: an existing one keeps its token and
 * other approvals, so no separate launcher client is left behind.
 */
export function nativeSignInClientForm(
  provider: NativeProvider,
  state: ProxyState,
  context: UIContext,
  route: ProxyRoute | undefined,
  choose: (choice: NativeSignInChoice) => Promise<void>,
  cancel: () => void,
): HTMLFormElement {
  const active = sortCallers(state.callers.filter((caller) => !caller.revoked));
  const suffixes = duplicateSuffixes(state.callers);
  const options: [string, string][] = active.map((caller) => [
    caller.id,
    clientLabel(caller, suffixes),
  ]);
  const client = select(
    [['', 'Choose a client…'], ...options, [newClient, 'New client…']],
    active.length ? '' : newClient,
  );
  const name = input();
  name.maxLength = 80;
  name.autocomplete = 'off';
  const nameField = field('New client name', name, 'For example, the device you sign in from.');
  const sync = () => (nameField.hidden = client.value !== newClient);
  client.addEventListener('change', sync);
  sync();
  const form = el(
    'form',
    el(
      'p',
      `Choose the client to sign in from, usually your own device. It keeps its token and other approvals and ${route ? 'keeps or gains' : 'gains'} access to this ${provider.shortName} provider. Don’t choose a worker’s client.`,
    ),
    field('Sign in from client', client),
    nameField,
  );
  const save = submit(
    form,
    context,
    async () => {
      if (client.value === newClient) {
        const value = name.value.trim();
        if (!value) throw new Error('Enter a name for the new client.');
        await choose({ client: { name: value }, label: value });
        return;
      }
      const label = options.find(([id]) => id === client.value)?.[1];
      if (!label) throw new Error('Choose the client to sign in from.');
      await choose({ client: { caller: client.value }, label });
    },
    'Get sign-in command',
  );
  // Add provider already has its own Cancel; Manage needs a way back.
  form.append(actions(save, ...(route ? [button('Cancel', cancel)] : [])));
  return form;
}
