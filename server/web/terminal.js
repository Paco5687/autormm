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

term.onData(d => send({ t: 'in', d }));
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
