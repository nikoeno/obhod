// Detour-server dialogs shared by Settings and the first-run card on Home (docs/UI.md §5.4, §6).
// The pasted link or JSON lives only in the textarea of the open dialog: it is sent once, never stored
// by the UI, and disappears with the dialog.
import { h, openDialog, toast } from './dom.js';
import { S, addServer, removeServer, testServer } from './store.js';
import { SERVER_TEXT, serverErrorText, testResultText } from './words.js';

/**
 * Add-server dialog: paste field, optional name, Test (offline) and Add. Errors are shown inline.
 * onAdded(server) runs after the dialog has closed.
 */
export function showAddServerDialog(onAdded) {
  let busy = false;
  const input = h('textarea', {
    id: 'srv-input', class: 'field mono', rows: 5, autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false',
    'aria-describedby': 'srv-help srv-msg', 'aria-required': 'true',
  });
  const name = h('input', {
    type: 'text', id: 'srv-name', class: 'field', maxlength: '60', autocomplete: 'off', 'aria-describedby': 'srv-name-help',
  });
  const msg = h('p', { id: 'srv-msg', class: 'form-msg', role: 'status', 'aria-live': 'polite' });

  function say(text, kind) {
    msg.textContent = text || '';
    msg.className = 'form-msg' + (kind ? ' ' + kind : '');
    if (kind === 'err') input.setAttribute('aria-invalid', 'true'); else input.removeAttribute('aria-invalid');
  }
  input.addEventListener('input', () => { if (msg.classList.contains('err')) say(''); });

  async function test() {
    if (busy) return true;
    if (!input.value.trim()) { say(SERVER_TEXT.pasteFirst, 'err'); input.focus(); return true; }
    busy = true;
    say(SERVER_TEXT.testing, 'info');
    try {
      const r = await testServer({ input: input.value });
      say(testResultText(r), r.ok ? 'ok' : 'err');
    } catch (e) {
      say(serverErrorText(e, 'add'), 'err');
    } finally { busy = false; }
    return true; // keep the dialog open
  }

  async function add() {
    if (busy) return true;
    if (!input.value.trim()) { say(SERVER_TEXT.pasteFirst, 'err'); input.focus(); return true; }
    busy = true;
    say(SERVER_TEXT.adding, 'info');
    try {
      const server = await addServer(input.value, name.value.trim());
      input.value = '';
      busy = false;
      toast(SERVER_TEXT.added(server.name));
      if (onAdded) requestAnimationFrame(() => onAdded(server)); // after the dialog has closed and returned focus
      return false; // close
    } catch (e) {
      say(serverErrorText(e, 'add'), 'err');
      busy = false;
      return true;
    }
  }

  openDialog({
    title: SERVER_TEXT.addTitle,
    body: [
      h('div', { class: 'form-row' },
        h('label', { htmlFor: 'srv-input', text: SERVER_TEXT.inputLabel }),
        input,
        h('p', { id: 'srv-help', class: 'muted small', text: SERVER_TEXT.inputHelp })),
      h('div', { class: 'form-row' },
        h('label', { htmlFor: 'srv-name', text: SERVER_TEXT.nameLabel }),
        name,
        h('p', { id: 'srv-name-help', class: 'muted small', text: SERVER_TEXT.nameHelp })),
      msg,
    ],
    actions: [
      { label: SERVER_TEXT.test, onClick: test },
      { label: SERVER_TEXT.add, primary: true, onClick: add },
      { label: SERVER_TEXT.cancel },
    ],
  });
}

/** Remove with a confirmation. The active server cannot be removed while connected: the dialog says so. */
export function confirmRemoveServer(server, onDone) {
  if (server.active && S.state === 'connected') {
    openDialog({
      title: SERVER_TEXT.removeActiveTitle,
      body: h('p', { text: SERVER_TEXT.removeActiveBody }),
      actions: [{ label: 'Close', primary: true }],
    });
    return;
  }
  openDialog({
    title: SERVER_TEXT.removeTitle(server.name),
    body: h('p', { text: SERVER_TEXT.removeBody }),
    actions: [
      { label: SERVER_TEXT.cancel },
      {
        label: SERVER_TEXT.remove,
        danger: true,
        onClick: async () => {
          try {
            await removeServer(server.id);
            toast(SERVER_TEXT.removed(server.name));
            if (onDone) requestAnimationFrame(onDone);
          } catch (e) {
            toast(serverErrorText(e, 'remove'));
          }
          return false;
        },
      },
    ],
  });
}
