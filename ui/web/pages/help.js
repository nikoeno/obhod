// Help (docs/UI.md §5.5).
import { h, toast } from '../dom.js';
import { ROUTES, MAIN } from '../words.js';
import { logViewer } from '../shell.js';

const NOT_IN_MOCK = 'Not available in the mock';

export function create() {
  const logSlot = h('div', { hidden: true });
  const logBtn = h('button', {
    class: 'btn', type: 'button', text: 'Show engine log', 'aria-expanded': 'false',
    onclick: () => {
      const open = logSlot.hidden;
      logSlot.hidden = !open;
      logBtn.setAttribute('aria-expanded', String(open));
      logBtn.textContent = open ? 'Hide engine log' : 'Show engine log';
      if (open && !logSlot.firstChild) logSlot.append(logViewer());
    },
  });

  const el = h('section', { class: 'page help', 'aria-labelledby': 'help-h' },
    h('h1', { id: 'help-h', text: 'Help' }),

    h('h2', { text: 'How it works' }),
    h('p', { class: 'muted', text: 'Press Start once. The app checks each site and picks one of three paths for it by itself. You do not need to set anything up.' }),
    h('div', { class: 'cards' }, MAIN.map((r) => h('div', { class: 'card path static' },
      h('span', { class: 'card-head' }, h('span', { class: 'dot ' + ROUTES[r].dot, 'aria-hidden': 'true' }), h('span', { class: 'card-title', text: ROUTES[r].long })),
      h('span', { class: 'card-line muted', text: ROUTES[r].line })))),

    h('h2', { text: 'Logs and diagnostics' }),
    h('div', { class: 'row gap wrap' },
      h('button', { class: 'btn', type: 'button', text: 'Open logs folder', onclick: () => toast(NOT_IN_MOCK) }),
      logBtn,
      h('button', { class: 'btn', type: 'button', text: 'Export diagnostics…', onclick: () => toast(`${NOT_IN_MOCK}. The real app saves a zip on this computer; nothing is uploaded.`) })),
    logSlot,

    h('h2', { text: 'If the internet stops working' }),
    h('p', { class: 'muted', text: 'Keep these steps where you can read them offline. Use an administrator PowerShell; after each step, check whether the internet is back.' }),
    h('ol', { class: 'steps' },
      h('li', null, h('strong', { text: 'Stop everything. ' }), 'This fixes it in most cases.', h('pre', { class: 'code', text: 'Stop-Process -Name splitter, sing-box, winws2 -Force -ErrorAction SilentlyContinue' })),
      h('li', null, h('strong', { text: 'Turn off the leftover network adapter. ' }), 'It is named "splitter".', h('pre', { class: 'code', text: 'Disable-NetAdapter -Name "splitter" -Confirm:$false' })),
      h('li', null, h('strong', { text: 'Unload the packet driver ' }), 'only if packets still seem dropped. A reboot does this too.'),
      h('li', null, h('strong', { text: 'Reset DNS and the network stack. ' }), h('pre', { class: 'code', text: 'ipconfig /flushdns' })),
      h('li', null, 'The full steps are in docs/RECOVERY.md.')),
  );
  return { el, update() {} };
}
