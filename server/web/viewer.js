// autormm remote-desktop viewer: decodes binary screen frames and forwards input.
const params = new URLSearchParams(location.search);
const tokenParam = params.get('token');
const hostName = params.get('host') || 'remote';

const canvas = document.getElementById('screen');
const ctx = canvas.getContext('2d', { alpha: false });
const stateEl = document.getElementById('state');
const resEl = document.getElementById('res');
const fpsEl = document.getElementById('fps');
const titleEl = document.getElementById('title');
const barEl = document.getElementById('bar');
const qualityEl = document.getElementById('quality');
titleEl.textContent = hostName;
document.title = 'autormm — ' + hostName;

let ws;
let remoteW = canvas.width, remoteH = canvas.height;
let frames = 0;

function connect() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/client/session?token=${encodeURIComponent(tokenParam)}`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => { stateEl.textContent = 'live'; stateEl.className = 'pill live'; };
  ws.onclose = () => { stateEl.textContent = 'disconnected'; stateEl.className = 'pill dead'; };
  ws.onerror = () => { stateEl.textContent = 'error'; stateEl.className = 'pill dead'; };
  ws.onmessage = onMessage;
}

function onMessage(ev) {
  if (typeof ev.data === 'string') {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.t === 'error') { stateEl.textContent = msg.message; stateEl.className = 'pill dead'; }
    } catch (_) {}
    return;
  }
  drawFrame(new DataView(ev.data));
  frames++;
}

function drawFrame(dv) {
  if (dv.byteLength < 10 || dv.getUint8(0) !== 0xAA) return;
  const kind = dv.getUint8(1);
  const w = dv.getUint16(2), h = dv.getUint16(4);
  const tile = dv.getUint16(6), count = dv.getUint16(8);
  if (w !== remoteW || h !== remoteH) {
    remoteW = w; remoteH = h;
    canvas.width = w; canvas.height = h;
    resEl.textContent = `${w}×${h}`;
  }
  let off = 10;
  const buf = dv.buffer;
  for (let i = 0; i < count; i++) {
    const tx = dv.getUint16(off), ty = dv.getUint16(off + 2);
    const len = dv.getUint32(off + 4);
    off += 8;
    const bytes = new Uint8Array(buf, off, len);
    off += len;
    const blob = new Blob([bytes], { type: 'image/jpeg' });
    const px = tx * tile, py = ty * tile;
    createImageBitmap(blob).then(bm => ctx.drawImage(bm, px, py)).catch(() => {});
  }
}

// ---- input ----
function send(obj) { if (ws && ws.readyState === 1) ws.send(JSON.stringify(obj)); }

function toRemote(e) {
  const r = canvas.getBoundingClientRect();
  const x = Math.round((e.clientX - r.left) * (canvas.width / r.width));
  const y = Math.round((e.clientY - r.top) * (canvas.height / r.height));
  return { x: Math.max(0, Math.min(remoteW - 1, x)), y: Math.max(0, Math.min(remoteH - 1, y)) };
}

let lastMove = 0;
canvas.addEventListener('mousemove', e => {
  const now = performance.now();
  if (now - lastMove < 16) return; // ~60 Hz cap
  lastMove = now;
  const p = toRemote(e);
  send({ t: 'mmove', x: p.x, y: p.y });
});
canvas.addEventListener('mousedown', e => {
  e.preventDefault();
  const p = toRemote(e);
  send({ t: 'mdown', x: p.x, y: p.y, button: e.button });
});
canvas.addEventListener('mouseup', e => {
  e.preventDefault();
  const p = toRemote(e);
  send({ t: 'mup', x: p.x, y: p.y, button: e.button });
});
canvas.addEventListener('contextmenu', e => e.preventDefault());
canvas.addEventListener('wheel', e => {
  e.preventDefault();
  const scale = e.deltaMode === 0 ? 1 / 100 : 1;
  send({ t: 'scroll', dx: Math.round(e.deltaX * scale), dy: Math.round(e.deltaY * scale) });
}, { passive: false });

window.addEventListener('keydown', e => {
  if (e.metaKey && e.key === 'v') return; // let paste-into-page shortcuts through if any
  e.preventDefault();
  send({ t: 'kdown', code: e.code });
});
window.addEventListener('keyup', e => {
  e.preventDefault();
  send({ t: 'kup', code: e.code });
});

qualityEl.addEventListener('change', () => send({ t: 'params', quality: parseInt(qualityEl.value, 10) }));
document.getElementById('ctrlAlt').addEventListener('click', () => {
  for (const c of ['ControlLeft', 'AltLeft', 'Delete']) send({ t: 'kdown', code: c });
  for (const c of ['Delete', 'AltLeft', 'ControlLeft']) send({ t: 'kup', code: c });
});

// auto-hide the top bar unless the pointer is near the top
document.addEventListener('mousemove', e => {
  barEl.classList.toggle('hide', e.clientY > 60);
});

// fps meter + keepalive
setInterval(() => { fpsEl.textContent = frames + ' fps'; frames = 0; }, 1000);
setInterval(() => send({ t: 'ping' }), 20000);

connect();
