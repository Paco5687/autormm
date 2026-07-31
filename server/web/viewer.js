// autormm remote-desktop viewer: decodes binary screen frames and forwards input.
const params = new URLSearchParams(location.search);
let tokenParam = params.get('token'); // refreshed on reconnect
const hostName = params.get('host') || 'remote';
const agentId = params.get('agent') || hostName;
const autofitKey = 'autofit:' + agentId;
const ADMIN_TOKEN_KEY = 'autormm_token'; // same key the dashboard uses (same origin)

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

// The screen source can change under us — most notably a Windows host handing
// off between the user-session agent and the SYSTEM console worker as it locks
// or someone signs in. That drops the media socket. Rather than make the
// operator reopen the viewer, mint a fresh session and reconnect in place, so it
// feels like one continuous session across lock / sign-in transitions.
let userClosing = false;
let reconnectTimer = null;
let reconnectAttempts = 0;
window.addEventListener('beforeunload', () => { userClosing = true; });

function connect() {
  // Start on JPEG-tile (the safe default); H.264 is opt-in via the codec toggle.
  currentCodec = 'jpeg-tile';
  disposeDecoder();
  codecsEl.innerHTML = '';
  autoFitDone = false; // auto-fit resolution once per session
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const caps = 'jpeg-tile';
  ws = new WebSocket(`${proto}://${location.host}/client/session?token=${encodeURIComponent(tokenParam)}&caps=${caps}`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => {
    reconnectAttempts = 0;
    stateEl.textContent = 'live';
    stateEl.className = 'pill live';
    // A modifier left down before the session dropped would still be held on the
    // host, so start every session from a known-clean keyboard state.
    heldKeys.clear();
    for (const code of MODIFIER_CODES) send({ t: 'kup', code });
  };
  ws.onclose = () => scheduleReconnect();
  ws.onerror = () => {}; // onclose always follows; reconnect is handled there
  ws.onmessage = onMessage;
}

// mintSession asks the hub for a fresh screen-session ticket for this host,
// using the dashboard's stored admin token (same-origin localStorage).
async function mintSession() {
  const adminTok = localStorage.getItem(ADMIN_TOKEN_KEY) || '';
  const res = await fetch('/api/session', {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + adminTok, 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_id: agentId, fps: 12, quality: 60 }),
  });
  if (!res.ok) throw new Error('session mint failed (' + res.status + ')');
  return (await res.json()).token;
}

function scheduleReconnect() {
  if (userClosing || reconnectTimer) return;
  reconnectAttempts++;
  // Fast for the first tries (a lock/sign-in handoff is only a couple of
  // seconds), then back off so an actually-offline host isn't hammered.
  const delay = Math.min(3000, 250 * reconnectAttempts);
  stateEl.textContent = 'reconnecting…';
  stateEl.className = 'pill';
  showNotice('');
  reconnectTimer = setTimeout(async () => {
    reconnectTimer = null;
    try {
      tokenParam = await mintSession();
      connect();
    } catch (e) {
      scheduleReconnect(); // keep trying; the host may just be mid-handoff
    }
  }, delay);
}

const rcursor = document.getElementById('rcursor');
const displaysEl = document.getElementById('displays');
const resPick = document.getElementById('resPick');
let selectedDisplay = -1;
let userDisplayChoice = null; // the operator's explicit display pick; survives reconnects
let displaysList = [];

function renderDisplays(m) {
  const list = m.list || [];
  displaysList = list;
  selectedDisplay = typeof m.current === 'number' ? m.current : -1;
  // A fresh session (including an auto-reconnect after a lock/sign-in handoff)
  // starts on the host's primary display. Restore the operator's pick so a
  // reconnect doesn't jump them back to another monitor mid-task.
  if (userDisplayChoice !== null && userDisplayChoice !== selectedDisplay &&
      list.some(d => d.index === userDisplayChoice)) {
    selectedDisplay = userDisplayChoice;
    send({ t: 'display', display: userDisplayChoice });
  }
  if (list.length <= 1) {
    displaysEl.innerHTML = ''; // single monitor: no display picker
  } else {
    // One display at a time — the union of several monitors is far too large a
    // frame to stream smoothly, so this is a switcher rather than a toggle.
    const btn = (idx, label) => `<button data-d="${idx}" class="${idx === selectedDisplay ? 'active' : ''}">${label}</button>`;
    let html = '';
    for (const d of list) html += btn(d.index, `Display ${d.index + 1}${d.primary ? ' ★' : ''}`);
    displaysEl.innerHTML = html;
    displaysEl.querySelectorAll('button').forEach(b => b.onclick = () => selectDisplay(parseInt(b.dataset.d, 10)));
  }
  renderRes();
}

function selectDisplay(idx) {
  userDisplayChoice = idx; // remembered so reconnects don't snap back to "All"
  selectedDisplay = idx;
  displaysEl.querySelectorAll('button').forEach(b => b.classList.toggle('active', parseInt(b.dataset.d, 10) === idx));
  send({ t: 'display', display: idx });
  renderRes();
}

// The display whose resolution the dropdown controls. Exactly one display is
// captured at a time, so this is simply the selected one (falling back to the
// only display before the first 'displays' message arrives).
function activeResDisplay() {
  if (selectedDisplay >= 0) return displaysList.find(d => d.index === selectedDisplay) || null;
  return displaysList.length === 1 ? displaysList[0] : null;
}

// aspectLabel returns a friendly aspect ratio (16:9, 16:10, 4:3, …) for w×h,
// snapping to a common ratio when close, else the gcd-reduced fraction.
function aspectLabel(w, h) {
  if (!w || !h) return '';
  const common = [[16, 9], [16, 10], [4, 3], [21, 9], [5, 4], [3, 2], [32, 9], [1, 1]];
  const r = w / h;
  for (const [a, b] of common) if (Math.abs(r - a / b) < 0.02) return `${a}:${b}`;
  const gcd = (x, y) => (y ? gcd(y, x % y) : x);
  const g = gcd(w, h) || 1;
  return `${Math.round(w / g)}:${Math.round(h / g)}`;
}

const fitBtn = document.getElementById('fitBtn');
const autofitEl = document.getElementById('autofit');
const autofitLabel = document.getElementById('autofitLabel');
let autoFitDone = false;
// Per-host preference (remembered in this browser): auto-fit resolution on connect.
autofitEl.checked = localStorage.getItem(autofitKey) === '1';
autofitEl.addEventListener('change', () => {
  localStorage.setItem(autofitKey, autofitEl.checked ? '1' : '0');
  if (autofitEl.checked) fitToWindow();
});

// With auto on, follow the window: resizing the browser re-fits the host, which
// is what makes it feel automatic rather than a one-shot at connect. Debounced
// so dragging a window edge doesn't fire a mode change per pixel.
let refitTimer = null;
window.addEventListener('resize', () => {
  if (!autofitEl.checked) return;
  clearTimeout(refitTimer);
  refitTimer = setTimeout(fitToWindow, 600);
});

// bestMode picks the host mode that best fits this window: the largest one whose
// pixels fit the viewer's device-pixel area (crisp, no upscaling), preferring a
// matching aspect ratio; if none fit, the smallest available.
function bestMode(d) {
  if (!d || !d.modes || !d.modes.length) return null;
  const dpr = window.devicePixelRatio || 1;
  const availW = Math.round(window.innerWidth * dpr);
  // Measure the bar rather than assuming its height: it has grown controls and
  // can wrap, and guessing low made Fit pick a mode taller than the viewport.
  const barH = (barEl && barEl.offsetHeight) || 34;
  const availH = Math.round((window.innerHeight - barH) * dpr);
  const targetAR = availW / Math.max(1, availH);
  const fitting = d.modes.filter(m => m.w <= availW && m.h <= availH);
  const pool = (fitting.length ? fitting : d.modes).slice();
  pool.sort((a, b) => {
    const areaA = a.w * a.h, areaB = b.w * b.h;
    if (areaA !== areaB) return fitting.length ? areaB - areaA : areaA - areaB; // largest that fits, else smallest
    return Math.abs(a.w / a.h - targetAR) - Math.abs(b.w / b.h - targetAR);
  });
  return pool[0];
}

function fitToWindow() {
  const d = activeResDisplay();
  const m = bestMode(d);
  if (!m) { flashState('no modes reported'); return; }
  // Compare against the size we are actually receiving, not the figures from
  // session start — those go stale the moment the resolution changes.
  const curW = remoteW || d.w, curH = remoteH || d.h;
  if (m.w === curW && m.h === curH) {
    flashState(`already ${m.w}×${m.h}`);
    return;
  }
  send({ t: 'setres', display: d.index, w: m.w, h: m.h });
  flashState(`fitting to ${m.w}×${m.h}…`);
}
fitBtn.addEventListener('click', fitToWindow);

// flashState shows a short message in the status pill, then restores it, so
// buttons that legitimately do nothing don't look broken.
let flashTimer = null;
function flashState(msg) {
  const prev = stateEl.textContent, prevClass = stateEl.className;
  stateEl.textContent = msg;
  clearTimeout(flashTimer);
  flashTimer = setTimeout(() => {
    stateEl.textContent = prev;
    stateEl.className = prevClass;
  }, 1800);
}

function renderRes() {
  const d = activeResDisplay();
  if (!d || !d.modes || !d.modes.length) {
    resPick.classList.add('hidden'); fitBtn.classList.add('hidden'); autofitLabel.classList.add('hidden'); resPick.innerHTML = '';
    return;
  }
  const cur = `${d.w}x${d.h}`;
  const label = (w, h) => `${w}×${h} (${aspectLabel(w, h)})`;
  let html = '';
  if (!d.modes.some(m => `${m.w}x${m.h}` === cur)) html += `<option value="${cur}" selected>${label(d.w, d.h)}</option>`;
  for (const m of d.modes) {
    const v = `${m.w}x${m.h}`;
    html += `<option value="${v}" ${v === cur ? 'selected' : ''}>${label(m.w, m.h)}</option>`;
  }
  resPick.innerHTML = html;
  resPick.classList.remove('hidden');
  fitBtn.classList.remove('hidden');
  autofitLabel.classList.remove('hidden');
  // Auto-fit once per session, only if enabled for this host.
  if (!autoFitDone) { autoFitDone = true; if (autofitEl.checked) fitToWindow(); }
}

resPick.addEventListener('change', () => {
  const d = activeResDisplay();
  if (!d) return;
  const [w, h] = resPick.value.split('x').map(n => parseInt(n, 10));
  if (w > 0 && h > 0) send({ t: 'setres', display: d.index, w, h });
});

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

// A notice explains a stream that is running but showing nothing (the Windows
// lock screen). An empty message means the condition cleared.
function showNotice(text) {
  const el = document.getElementById('notice');
  if (!el) return;
  el.textContent = text || '';
  el.classList.toggle('hidden', !text);
}

// The pointer is drawn as an overlay, and the host echoes its real position back
// — which costs a full round trip plus the agent's poll interval. Waiting for
// that echo is what makes the mouse feel laggy even when the video is smooth, so
// while the operator is actively moving we draw at the local position
// immediately and let the host's reports take over once they stop.
let localCursorUntil = 0;

function placeCursor(x, y) {
  const r = canvas.getBoundingClientRect();
  rcursor.style.left = (r.left + x * (r.width / canvas.width)) + 'px';
  rcursor.style.top = (r.top + y * (r.height / canvas.height)) + 'px';
  rcursor.classList.remove('hidden');
}

// predictCursor draws the pointer where the operator just moved it, without
// waiting for the host to confirm.
function predictCursor(p) {
  localCursorUntil = performance.now() + 250;
  placeCursor(p.x, p.y);
}

function updateCursor(m) {
  if (!m.vis) { rcursor.classList.add('hidden'); return; }
  // Ignore stale echoes of our own movement; they lag behind the local position
  // and would drag the pointer backwards.
  if (performance.now() < localCursorUntil) return;
  placeCursor(m.x, m.y);
}

function onMessage(ev) {
  if (typeof ev.data === 'string') {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.t === 'error') { stateEl.textContent = msg.message; stateEl.className = 'pill dead'; }
      else if (msg.t === 'notice') showNotice(msg.message);
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

let lastPos = { x: 0, y: 0 };
function toRemote(e) {
  const r = canvas.getBoundingClientRect();
  const x = Math.round((e.clientX - r.left) * (canvas.width / r.width));
  const y = Math.round((e.clientY - r.top) * (canvas.height / r.height));
  lastPos = { x: Math.max(0, Math.min(remoteW - 1, x)), y: Math.max(0, Math.min(remoteH - 1, y)) };
  return lastPos;
}

function overCanvas(e) {
  const r = canvas.getBoundingClientRect();
  return e.clientX >= r.left && e.clientX < r.right && e.clientY >= r.top && e.clientY < r.bottom;
}

// Buttons currently held on the host. Move and release are tracked on the
// window, not the canvas: pressing inside the view and releasing over the top
// bar (always visible) or outside the browser never reaches a canvas-only
// 'mouseup', which would leave the host button stuck down — from then on every
// move is a drag, and dragging into the top edge snaps windows. Window-level
// tracking also keeps a drag alive while the pointer is briefly outside.
const heldButtons = new Set();

let lastMove = 0;
window.addEventListener('mousemove', e => {
  // Off-canvas moves only matter mid-drag; otherwise reaching for the top bar
  // would slide the host cursor to a clamped edge.
  if (!heldButtons.size && !overCanvas(e)) return;
  const now = performance.now();
  if (now - lastMove < 16) return; // ~60 Hz cap
  lastMove = now;
  const p = toRemote(e);
  predictCursor(p); // draw immediately; don't wait for the host to echo back
  send({ t: 'mmove', x: p.x, y: p.y });
});
canvas.addEventListener('mousedown', e => {
  e.preventDefault();
  heldButtons.add(e.button);
  const p = toRemote(e);
  predictCursor(p);
  send({ t: 'mdown', x: p.x, y: p.y, button: e.button });
});
window.addEventListener('mouseup', e => {
  if (!heldButtons.delete(e.button)) return; // not a press that started in the view
  const p = toRemote(e);
  send({ t: 'mup', x: p.x, y: p.y, button: e.button });
});
// A drag that ends while the tab is hidden/unfocused never delivers 'mouseup'
// (e.g. alt-tab, or the release lands on another window), so release manually.
function releaseHeldButtons() {
  for (const b of heldButtons) send({ t: 'mup', x: lastPos.x, y: lastPos.y, button: b });
  heldButtons.clear();
}
window.addEventListener('blur', releaseHeldButtons);
canvas.addEventListener('contextmenu', e => e.preventDefault());
canvas.addEventListener('wheel', e => {
  e.preventDefault();
  const scale = e.deltaMode === 0 ? 1 / 100 : 1;
  send({ t: 'scroll', dx: Math.round(e.deltaX * scale), dy: Math.round(e.deltaY * scale) });
}, { passive: false });

// ---- on-screen keyboard (touch devices) ----
// The remote screen is a <canvas>, so tapping it never raises a mobile keyboard.
// The ⌨ button focuses a hidden input; typing into it is forwarded as text, and
// its special keys (Enter/Backspace/arrows) as key events.
const softkbd = document.getElementById('softkbd');
const kbdbar = document.getElementById('kbdbar');
let lastKbdVal = '';
function keyTap(code) { send({ t: 'kdown', code }); send({ t: 'kup', code }); }
const vwrap = document.querySelector('.viewer-wrap');
function toggleKbd(show) {
  if (show === undefined) show = kbdbar.classList.contains('hidden');
  kbdbar.classList.toggle('hidden', !show);
  if (show) { softkbd.value = ''; lastKbdVal = ''; softkbd.focus(); } else { softkbd.blur(); }
  updateKbdLayout();
}
// When the on-screen keyboard is up, shrink the remote view to fit the space
// above it (like Parsec) so the whole screen stays visible, and float the input
// bar just above the OS keyboard. Uses visualViewport to know the visible area.
function updateKbdLayout() {
  const vv = window.visualViewport;
  const open = !kbdbar.classList.contains('hidden');
  if (open && vv) {
    const barH = kbdbar.offsetHeight || 46;
    const kbH = Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop));
    kbdbar.style.bottom = kbH + 'px';
    vwrap.style.bottom = (kbH + barH) + 'px';
  } else {
    kbdbar.style.bottom = '';
    vwrap.style.bottom = '';
  }
}
if (window.visualViewport) {
  window.visualViewport.addEventListener('resize', updateKbdLayout);
  window.visualViewport.addEventListener('scroll', updateKbdLayout);
}
document.getElementById('kbd').addEventListener('click', () => toggleKbd());
document.getElementById('kbdHide').addEventListener('click', () => toggleKbd(false));
function sendEnter() { keyTap('Enter'); softkbd.value = ''; lastKbdVal = ''; softkbd.focus(); }
document.getElementById('kbdEnter').addEventListener('click', sendEnter);

// Diff the box value on every change. This is the only reliable way to capture
// Android/Gboard input, which doesn't fire usable keydown/inputType events.
// Compare against the last value: backspace what changed after the common
// prefix, then type the new tail.
softkbd.addEventListener('input', () => {
  const v = softkbd.value;
  if (v === lastKbdVal) return;
  let common = 0;
  const max = Math.min(v.length, lastKbdVal.length);
  while (common < max && v[common] === lastKbdVal[common]) common++;
  for (let i = 0; i < lastKbdVal.length - common; i++) keyTap('Backspace');
  if (v.length > common) send({ t: 'type', text: v.slice(common) });
  lastKbdVal = v;
});
softkbd.addEventListener('keydown', e => {
  if (e.key === 'Enter' || e.code === 'Enter') { e.preventDefault(); sendEnter(); return; }
  const special = ['Tab', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Escape', 'Home', 'End'];
  if (special.includes(e.code)) { e.preventDefault(); keyTap(e.code); }
});

// Keys currently held on the host. The browser stops delivering keyup the
// moment the window loses focus — alt-tab while holding Shift and the host
// keeps it down forever, which is the classic "everything is in CAPS" stuck
// modifier. Track what is down so it can be released explicitly.
const heldKeys = new Set();
const MODIFIER_CODES = [
  'ShiftLeft', 'ShiftRight', 'ControlLeft', 'ControlRight',
  'AltLeft', 'AltRight', 'MetaLeft', 'MetaRight',
];

window.addEventListener('keydown', e => {
  if (document.activeElement === softkbd) return; // soft keyboard handles its own input
  // Let Ctrl/Cmd+V raise a browser 'paste' event (handled below) so we can push
  // the local clipboard to the host *before* it pastes. Don't forward the V key
  // here — the paste handler sends it once the clipboard is synced.
  if ((e.ctrlKey || e.metaKey) && e.code === 'KeyV') return;
  e.preventDefault();
  heldKeys.add(e.code);
  send({ t: 'kdown', code: e.code });
});
window.addEventListener('keyup', e => {
  // Always release a key we pressed, even if focus moved into the soft-keyboard
  // box in between — otherwise it stays held on the host. Keys we never
  // forwarded (the soft keyboard's own) are left alone so we don't inject a
  // release for something that was never pressed.
  const wasHeld = heldKeys.delete(e.code);
  if (!wasHeld && document.activeElement === softkbd) return;
  e.preventDefault();
  send({ t: 'kup', code: e.code });
});

function releaseHeldKeys() {
  for (const code of heldKeys) send({ t: 'kup', code });
  heldKeys.clear();
}

// Focus loss, tab switch, and page hide all end key delivery without a keyup.
window.addEventListener('blur', releaseHeldKeys);
window.addEventListener('pagehide', releaseHeldKeys);
document.addEventListener('visibilitychange', () => {
  if (document.hidden) releaseHeldKeys();
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
// Task Manager (Ctrl+Shift+Esc). Ctrl+Alt+Del can't be synthesized on Windows
// (protected sequence) and its secure desktop isn't capturable anyway; Task
// Manager is what operators actually need and it works via normal injection.
document.getElementById('taskMgr').addEventListener('click', (e) => {
  for (const c of ['ControlLeft', 'ShiftLeft', 'Escape']) send({ t: 'kdown', code: c });
  for (const c of ['Escape', 'ShiftLeft', 'ControlLeft']) send({ t: 'kup', code: c });
  // Belt-and-suspenders: make sure no modifier is left stuck on the host if it
  // dropped a key-up while Task Manager stole focus.
  for (const c of ['ControlLeft', 'ControlRight', 'ShiftLeft', 'ShiftRight', 'AltLeft', 'AltRight']) send({ t: 'kup', code: c });
  e.currentTarget.blur(); // return focus to the page so canvas input keeps flowing
});

// The top bar stays visible (keyboard / display / codec controls are always reachable).

// fps meter + keepalive
setInterval(() => { fpsEl.textContent = frames + ' fps'; frames = 0; }, 1000);
setInterval(() => send({ t: 'ping' }), 20000);

connect();
