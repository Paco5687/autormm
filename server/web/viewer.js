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
  // Start on JPEG-tile (the safe default); H.264 is opt-in via the codec toggle.
  currentCodec = 'jpeg-tile';
  disposeDecoder();
  codecsEl.innerHTML = '';
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const caps = 'jpeg-tile';
  ws = new WebSocket(`${proto}://${location.host}/client/session?token=${encodeURIComponent(tokenParam)}&caps=${caps}`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => { stateEl.textContent = 'live'; stateEl.className = 'pill live'; };
  ws.onclose = () => { stateEl.textContent = 'disconnected'; stateEl.className = 'pill dead'; };
  ws.onerror = () => { stateEl.textContent = 'error'; stateEl.className = 'pill dead'; };
  ws.onmessage = onMessage;
}

const rcursor = document.getElementById('rcursor');
const displaysEl = document.getElementById('displays');
let selectedDisplay = -1;

function renderDisplays(m) {
  const list = m.list || [];
  if (list.length <= 1) { displaysEl.innerHTML = ''; return; } // single monitor: no picker
  selectedDisplay = typeof m.current === 'number' ? m.current : -1;
  const btn = (idx, label) => `<button data-d="${idx}" class="${idx === selectedDisplay ? 'active' : ''}">${label}</button>`;
  let html = btn(-1, 'All');
  for (const d of list) html += btn(d.index, `Display ${d.index + 1}${d.primary ? ' ★' : ''}`);
  displaysEl.innerHTML = html;
  displaysEl.querySelectorAll('button').forEach(b => b.onclick = () => selectDisplay(parseInt(b.dataset.d, 10)));
}

function selectDisplay(idx) {
  selectedDisplay = idx;
  displaysEl.querySelectorAll('button').forEach(b => b.classList.toggle('active', parseInt(b.dataset.d, 10) === idx));
  send({ t: 'display', display: idx });
}

// ---- codec picker + H.264 (WebCodecs) decode ----
const codecsEl = document.getElementById('codecs');
let currentCodec = 'jpeg-tile';
let decoder = null, decoderReady = false, h264ts = 0;

function renderCodecs(m) {
  const canH264 = (m.codecs || []).includes('webcodecs-h264') && ('VideoDecoder' in window);
  if (!canH264) { codecsEl.innerHTML = ''; return; }
  const btn = (c, label) => `<button data-c="${c}" class="${currentCodec === c ? 'active' : ''}">${label}</button>`;
  codecsEl.innerHTML = btn('jpeg-tile', 'JPEG-tile') + btn('webcodecs-h264', 'H.264');
  codecsEl.querySelectorAll('button').forEach(b => b.onclick = () => selectCodec(b.dataset.c));
}

function selectCodec(c) {
  currentCodec = c;
  codecsEl.querySelectorAll('button').forEach(b => b.classList.toggle('active', b.dataset.c === c));
  if (c === 'webcodecs-h264') initDecoder(); else disposeDecoder();
  send({ t: 'codec', codec: c });
}

function initDecoder() {
  disposeDecoder();
  if (!('VideoDecoder' in window)) { fallbackH264(); return; }
  decoderReady = false; h264ts = 0;
  decoder = new VideoDecoder({
    output: f => {
      if (f.displayWidth !== canvas.width || f.displayHeight !== canvas.height) {
        canvas.width = f.displayWidth; canvas.height = f.displayHeight;
        remoteW = f.displayWidth; remoteH = f.displayHeight;
        resEl.textContent = `${f.displayWidth}×${f.displayHeight}`;
      }
      ctx.drawImage(f, 0, 0); f.close();
    },
    error: () => fallbackH264(),
  });
}

function disposeDecoder() {
  if (decoder) { try { decoder.close(); } catch (_) {} decoder = null; }
  decoderReady = false;
}

function fallbackH264() {
  disposeDecoder();
  stateEl.textContent = 'H.264 unavailable — using JPEG-tile';
  if (currentCodec !== 'jpeg-tile') selectCodec('jpeg-tile');
}

function decodeH264(buf) {
  if (!decoder) return;
  const flags = new DataView(buf).getUint8(0);
  const au = new Uint8Array(buf, 1);
  const key = (flags & 1) === 1;
  if (!decoderReady) {
    if (!key) return; // wait for a keyframe to start decoding
    try { decoder.configure({ codec: codecStringFromAU(au), optimizeForLatency: true }); decoderReady = true; }
    catch (e) { fallbackH264(); return; }
  }
  try { decoder.decode(new EncodedVideoChunk({ type: key ? 'key' : 'delta', timestamp: (h264ts++) * 1000, data: au })); }
  catch (e) { fallbackH264(); }
}

// Build the exact avc1 codec string from the SPS NAL (type 7) in a keyframe.
function codecStringFromAU(au) {
  for (let i = 0; i + 6 < au.length; i++) {
    if (au[i] === 0 && au[i + 1] === 0 && au[i + 2] === 1 && (au[i + 3] & 0x1f) === 7) {
      const hex = x => x.toString(16).padStart(2, '0');
      return 'avc1.' + hex(au[i + 4]) + hex(au[i + 5]) + hex(au[i + 6]);
    }
  }
  return 'avc1.42E01E';
}

function updateCursor(m) {
  if (!m.vis) { rcursor.classList.add('hidden'); return; }
  const r = canvas.getBoundingClientRect();
  rcursor.style.left = (r.left + m.x * (r.width / canvas.width)) + 'px';
  rcursor.style.top = (r.top + m.y * (r.height / canvas.height)) + 'px';
  rcursor.classList.remove('hidden');
}

function onMessage(ev) {
  if (typeof ev.data === 'string') {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.t === 'error') { stateEl.textContent = msg.message; stateEl.className = 'pill dead'; }
      else if (msg.t === 'cursor') updateCursor(msg);
      else if (msg.t === 'displays') renderDisplays(msg);
      else if (msg.t === 'caps') renderCodecs(msg);
      else if (msg.t === 'clip') setLocalClipboard(msg.d);
    } catch (_) {}
    return;
  }
  // Each media message is prefixed with a 1-byte codec tag (0 = JPEG-tile, 1 = H.264).
  if (ev.data.byteLength < 1) return;
  const codec = new DataView(ev.data).getUint8(0);
  const payload = ev.data.slice(1);
  if (codec === 0) { drawFrame(new DataView(payload)); frames++; }
  else if (codec === 1) { decodeH264(payload); frames++; }
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
  // Let Ctrl/Cmd+V raise a browser 'paste' event (handled below) so we can push
  // the local clipboard to the host *before* it pastes. Don't forward the V key
  // here — the paste handler sends it once the clipboard is synced.
  if ((e.ctrlKey || e.metaKey) && e.code === 'KeyV') return;
  e.preventDefault();
  send({ t: 'kdown', code: e.code });
});
window.addEventListener('keyup', e => {
  e.preventDefault();
  send({ t: 'kup', code: e.code });
});

// ---- clipboard sync ----
let lastClip = null;

// Host -> viewer: write the host's clipboard locally (needs a secure context:
// https or localhost; on plain http the browser blocks clipboard writes).
function setLocalClipboard(text) {
  if (text == null || text === lastClip) return;
  lastClip = text;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).catch(() => {});
  }
}

// Viewer -> host: on paste (Ctrl/Cmd+V), set the host clipboard, then paste.
// getData in a paste handler works even on plain http.
window.addEventListener('paste', e => {
  const text = (e.clipboardData || window.clipboardData);
  const data = text ? text.getData('text') : '';
  if (data != null) {
    lastClip = data;
    send({ t: 'clip', clip: data });
    send({ t: 'kdown', code: 'KeyV' }); // Ctrl/Cmd is physically held
    send({ t: 'kup', code: 'KeyV' });
  }
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
