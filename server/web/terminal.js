// autormm browser terminal: bridges an agent PTY to xterm.js.
const params = new URLSearchParams(location.search);
const token = params.get('token');
const hostName = params.get('host') || 'terminal';
document.getElementById('title').textContent = hostName;
document.title = 'autormm — ' + hostName;
const stateEl = document.getElementById('state');

const term = new Terminal({
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
  fontSize: 13,
  cursorBlink: true,
  theme: { background: '#000000', foreground: '#e6edf3' },
});
const fit = new FitAddon.FitAddon();
term.loadAddon(fit);
term.open(document.getElementById('term'));
fit.fit();

const proto = location.protocol === 'https:' ? 'wss' : 'ws';
const ws = new WebSocket(`${proto}://${location.host}/client/session?token=${encodeURIComponent(token)}`);
ws.binaryType = 'arraybuffer';
const dec = new TextDecoder();

function send(obj) { if (ws.readyState === 1) ws.send(JSON.stringify(obj)); }
function sendResize() { send({ t: 'resize', cols: term.cols, rows: term.rows }); }

// Keepalive. The hub's relay gives up on a socket that has sent nothing for 90
// seconds, and an idle shell sends nothing at all — so without this a terminal
// left alone dies on its own. The agent ignores message types it does not know,
// so this never reaches the shell.
setInterval(() => send({ t: 'ping' }), 20000);

ws.onopen = () => {
  stateEl.textContent = 'connected';
  stateEl.className = 'pill live';
  sendResize();
  term.focus();
};
ws.onclose = () => {
  stateEl.textContent = 'disconnected';
  stateEl.className = 'pill dead';
  term.write('\r\n\x1b[90m[session closed]\x1b[0m\r\n');
};
ws.onmessage = (ev) => {
  if (typeof ev.data === 'string') {
    try { const m = JSON.parse(ev.data); if (m.t === 'error') { stateEl.textContent = m.message; stateEl.className = 'pill dead'; } } catch (_) {}
    return;
  }
  term.write(new Uint8Array(ev.data));
};

// ---- on-screen keys ----
//
// A phone keyboard has no Escape, no Tab, no arrows and no Ctrl, which between
// them are most of what a shell is driven with. The modifiers are one-shots
// rather than held: tap Ctrl, then type c, and the next character is sent as
// the control code — a touch screen cannot hold a key down while pressing
// another.
let armed = { ctrl: false, alt: false };

// The sequences live here rather than in the markup, because an escape written
// into an HTML attribute is four literal characters: data-seq="\x1b[A" sends a
// backslash, an x and a one and a b, which is a fine way to fill a shell with
// nonsense.
const KEYSEQ = {
  esc: '\x1b', tab: '\t', bksp: '\x7f', del: '\x1b[3~',
  up: '\x1b[A', down: '\x1b[B', right: '\x1b[C', left: '\x1b[D',
  home: '\x1b[H', end: '\x1b[F', pgup: '\x1b[5~', pgdn: '\x1b[6~',
  'c-c': '\x03', 'c-d': '\x04', 'c-z': '\x1a', 'c-l': '\x0c',
  pipe: '|', tilde: '~', slash: '/', dash: '-',
};

function drawMods() {
  for (const b of document.querySelectorAll('.kk-mod')) {
    b.classList.toggle('active', !!armed[b.dataset.mod]);
  }
}

function applyMods(d) {
  if (d.length !== 1) return d;
  if (armed.ctrl) {
    const c = d.toUpperCase().charCodeAt(0);
    // Ctrl maps @ through _ onto 0..31; anything else is passed through rather
    // than mangled into a control code nobody asked for.
    if (c >= 64 && c <= 95) d = String.fromCharCode(c - 64);
    armed.ctrl = false;
  }
  if (armed.alt) {
    d = '\x1b' + d; // Alt is Escape then the key, as every terminal expects
    armed.alt = false;
  }
  drawMods();
  return d;
}

term.onData(d => send({ t: 'in', d: applyMods(d) }));

document.getElementById('kbdbar').addEventListener('click', (e) => {
  const b = e.target.closest('.kk');
  if (!b) return;
  e.preventDefault();
  if (b.dataset.mod) {
    armed[b.dataset.mod] = !armed[b.dataset.mod];
    drawMods();
  } else if (KEYSEQ[b.dataset.key] !== undefined) {
    // Straight through: a sequence from this bar is already what the terminal
    // should receive, and running it past the one-shots would let a stray armed
    // Ctrl rewrite an arrow key.
    send({ t: 'in', d: KEYSEQ[b.dataset.key] });
  }
  term.focus();
});

const keysBtn = document.getElementById('keysBtn');
function showKeys(on) {
  document.getElementById('kbdbar').classList.toggle('hidden', !on);
  document.body.classList.toggle('keys', on);
  keysBtn.classList.toggle('active', on);
  fit.fit();
  sendResize();
}
keysBtn.addEventListener('click', () => {
  showKeys(document.getElementById('kbdbar').classList.contains('hidden'));
  term.focus();
});
// Up by default where there is no real keyboard to have these keys on.
if (window.matchMedia && window.matchMedia('(pointer: coarse)').matches) showKeys(true);

// Closing. The terminal opens as a popup, so close() is permitted; when it is
// not — a tab restored by the browser, say — going back to the dashboard is
// what the person meant by closing it.
document.getElementById('closeBtn').addEventListener('click', () => {
  window.close();
  setTimeout(() => { if (!window.closed) location.href = '/'; }, 150);
});

window.addEventListener('resize', () => { fit.fit(); sendResize(); });

// Copy the selection with Ctrl+Shift+C (Ctrl+C stays SIGINT). Paste is handled
// natively by xterm on Ctrl+V / right-click, which works even over plain http.
term.attachCustomKeyEventHandler((e) => {
  if (e.type === 'keydown' && e.ctrlKey && e.shiftKey && e.code === 'KeyC') {
    const sel = term.getSelection();
    if (sel) { copyText(sel); return false; }
  }
  return true;
});

function copyText(text) {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).catch(() => fallbackCopy(text));
  } else {
    fallbackCopy(text);
  }
}

// Works on plain http (navigator.clipboard needs a secure context).
function fallbackCopy(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try { document.execCommand('copy'); } catch (_) {}
  ta.remove();
  term.focus();
}
