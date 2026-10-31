// Logic of the Add server dialog (docs/UI.md 5.4), separate from the component so it can be tested.
// The pasted link or JSON lives only in this model's `input` while the dialog is open: it is sent
// once, never stored by the UI, and cleared on success and on close.

import { reactive } from 'vue';
import { pages } from '../../design/copy';
import type { Server } from '../backend/types';
import type { Store } from '../store';
import { serverErrorText, serverText, testResultText } from './serverText';

const add = pages.settings.detour.add;

export type MsgKind = '' | 'info' | 'ok' | 'err';

export interface AddServerModel {
  input: string;
  name: string;
  busy: boolean;
  msg: { text: string; kind: MsgKind };
  /** Offline check; never closes the dialog. */
  test(): Promise<void>;
  /** Adds the server. Resolves with it on success (the dialog then closes), `null` otherwise (errors stay inline). */
  add(): Promise<Server | null>;
  /** Forget the pasted text (dialog closed or cancelled). */
  clear(): void;
  /** Hide an old error once the user edits the field. */
  edited(): void;
}

export function createAddServerModel(store: Pick<Store, 'addServer' | 'testServer'>, toast: (t: string) => void): AddServerModel {
  const m = reactive<AddServerModel>({
    input: '',
    name: '',
    busy: false,
    msg: { text: '', kind: '' },
    async test() {
      if (m.busy) return;
      if (!m.input.trim()) return say(add.pasteFirst, 'err');
      m.busy = true;
      say(add.testing, 'info');
      try {
        const r = await store.testServer({ input: m.input });
        say(testResultText(r), r.ok ? 'ok' : 'err');
      } catch (e) {
        say(serverErrorText(e as { code?: string; message?: string }, 'add'), 'err');
      } finally {
        m.busy = false;
      }
    },
    async add() {
      if (m.busy) return null;
      if (!m.input.trim()) {
        say(add.pasteFirst, 'err');
        return null;
      }
      m.busy = true;
      say(add.adding, 'info');
      try {
        const server = await store.addServer(m.input, m.name.trim());
        m.input = '';
        m.name = '';
        say('', '');
        toast(serverText.added(server.name));
        return server;
      } catch (e) {
        say(serverErrorText(e as { code?: string; message?: string }, 'add'), 'err');
        return null;
      } finally {
        m.busy = false;
      }
    },
    clear() {
      m.input = '';
      m.name = '';
      say('', '');
    },
    edited() {
      if (m.msg.kind === 'err') say('', '');
    },
  });

  function say(text: string, kind: MsgKind): void {
    m.msg = { text, kind };
  }
  return m;
}
