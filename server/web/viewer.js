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
const linkEl = document.getElementById('link');
const diagEl = document.getElementById('diag');
const titleEl = document.getElementById('title');
const barEl = document.getElementById('bar');
const qualityEl = document.getElementById('quality');
const vwrap = document.querySelector('.viewer-wrap');
titleEl.textContent = hostName;
document.title = 'autormm — ' + hostName;

let ws;
let remoteW = canvas.width, remoteH = canvas.height;
let frames = 0;
let rxTotal = 0; // running total of bytes received, reported to the host

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
  // Advertise H.264 when this browser can decode it, so the hub starts the
  // session on it whenever the host has ffmpeg. JPEG-tile stays the fallback,
  // both in negotiation and if a decode ever fails mid-stream.
  const caps = ('VideoDecoder' in window) ? 'jpeg-tile,webcodecs-h264' : 'jpeg-tile';
  ws = new WebSocket(`${proto}://${location.host}/client/session?token=${encodeURIComponent(tokenParam)}&caps=${caps}`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => {
    reconnectAttempts = 0;
    rxTotal = 0; // a new session; the host's tally starts over too
    stateEl.textContent = 'live';
    stateEl.className = 'pill live';
    // A modifier left down before the session dropped would still be held on the
    // host, so start every session from a known-clean keyboard state.
    heldKeys.clear();
    if (altTabTimer !== null) { clearTimeout(altTabTimer); altTabTimer = null; }
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
    // 30, not 60. On a link that cannot carry both, frames and sharpness trade
    // directly against each other: 60fps halves the bits in every frame, which
    // reads as "smooth but heavily pixelated". 30 is indistinguishable for
    // desktop work and looks twice as good at the same bandwidth.
    body: JSON.stringify({ agent_id: agentId, fps: 30, quality: 90 }),
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
  // The hub negotiates the starting codec, so trust what it reports rather than
  // assuming the JPEG default — otherwise the toggle highlights the wrong one.
  if (m.active) currentCodec = m.active;
  // The decoder used to be built only by a button click. Now that the hub can
  // start a session on H.264 by itself, one has to exist before the first frame
  // arrives — otherwise decodeH264 drops everything and the screen stays black
  // until the operator toggles the codec by hand.
  if (currentCodec === 'webcodecs-h264' && !decoder) initDecoder();
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
        // Update the remote dimensions *before* laying out: layoutCanvas sizes
        // the element from them, so doing this the other way round scaled the
        // new picture to the old aspect ratio and left it visibly squashed
        // until the next window resize happened to fix it.
        remoteW = f.displayWidth; remoteH = f.displayHeight;
        layoutCanvas();
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
  // Safety net for ordering: if H.264 frames arrive before the caps message
  // that would have set the decoder up, build it now rather than dropping them.
  if (!decoder) {
    initDecoder();
    if (!decoder) return; // no WebCodecs here; fallbackH264 has taken over
  }
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
  rxTotal += (typeof ev.data === 'string') ? ev.data.length : ev.data.byteLength;
  if (typeof ev.data === 'string') {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.t === 'superseded') {
        // Another connection took this host over. Do NOT reconnect: the two
        // sessions would displace each other forever.
        userClosing = true;
        clearTimeout(reconnectTimer); reconnectTimer = null;
        stateEl.textContent = 'taken over';
        stateEl.className = 'pill dead';
        showNotice(msg.message || 'This session was taken over by a newer connection. Reload to take it back.');
      }
      else if (msg.t === 'error') { stateEl.textContent = msg.message; stateEl.className = 'pill dead'; }
      else if (msg.t === 'notice') showNotice(msg.message);
      else if (msg.t === 'cursor') updateCursor(msg);
      else if (msg.t === 'displays') renderDisplays(msg);
      else if (msg.t === 'caps') renderCodecs(msg);
      else if (msg.t === 'clip') setLocalClipboard(msg.d);
      else if (msg.t === 'link') showLinkRate(msg);
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
    layoutCanvas();
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

// The canvas has the host's pixel dimensions, which may be smaller OR larger
// than the viewport. Size the *element* to the largest box with the same aspect
// ratio that fits the wrapper, so a lower host resolution is scaled up to fill
// the window instead of sitting in a letterboxed island. Keeping the element box
// equal to the picture (rather than using object-fit) means
// getBoundingClientRect still maps pointer coordinates correctly.
function layoutCanvas() {
  if (!vwrap || !remoteW || !remoteH) return;
  const availW = vwrap.clientWidth, availH = vwrap.clientHeight;
  if (availW <= 0 || availH <= 0) return;
  const scale = Math.min(availW / remoteW, availH / remoteH);
  canvas.style.width = Math.max(1, Math.round(remoteW * scale)) + 'px';
  canvas.style.height = Math.max(1, Math.round(remoteH * scale)) + 'px';
}
window.addEventListener('resize', layoutCanvas);

// Give focus back to the page after any toolbar button is used.
//
// A clicked button keeps focus, and a focused button is activated by Space and
// Enter. So after touching Fit or ⌨ once, every later space in whatever the
// operator was typing on the host *also* re-ran that button — refitting the
// resolution or flipping the keyboard bar mid-sentence. It also left the button
// looking permanently pressed.
//
// The keyboard button hands focus to its own input, and that runs first (target
// phase, before this bubbles), so blurring the button here cannot steal it.
barEl.addEventListener('click', (e) => {
  const b = e.target.closest('button');
  if (b) b.blur();
});

// ---- session info & settings panel ----
// The live readings and the set-once controls both moved out of the bar. The
// readings because a figure that changes width shifts everything beside it,
// which on a phone walks the keyboard button out from under your thumb; the
// controls because a bar you have to swipe sideways is one you cannot use in a
// hurry.
const infoBtn = document.getElementById('infoBtn');
const infoPanel = document.getElementById('infoPanel');

// The quick-action tray. It exists only on a narrow screen — everywhere wider
// the same buttons are laid out in the bar itself and the toggle is not shown,
// so this only ever runs on a phone.
const qaToggle = document.getElementById('qaToggle');
const quickActions = document.getElementById('quickActions');

function toggleQuickActions(show) {
  if (show === undefined) show = !quickActions.classList.contains('open');
  quickActions.classList.toggle('open', show);
  qaToggle.classList.toggle('active', show);
  qaToggle.setAttribute('aria-expanded', show ? 'true' : 'false');
}
qaToggle.addEventListener('click', () => {
  if (!infoPanel.classList.contains('hidden')) toggleInfo(false); // one at a time
  toggleQuickActions();
});
// The tray stays open after a tap on purpose: Alt+Tab needs several taps in
// succession to cycle windows, and closing after the first would make that
// gesture impossible from a phone.
quickActions.addEventListener('click', (e) => {
  const b = e.target.closest('button');
  if (b) b.blur();
});

function toggleInfo(show) {
  if (show === undefined) show = infoPanel.classList.contains('hidden');
  infoPanel.classList.toggle('hidden', !show);
  infoBtn.classList.toggle('active', show);
  infoBtn.setAttribute('aria-expanded', show ? 'true' : 'false');
}
infoBtn.addEventListener('click', () => {
  if (quickActions.classList.contains('open')) toggleQuickActions(false);
  toggleInfo();
});
infoPanel.addEventListener('click', (e) => {
  // Same reason as the bar: a focused button is re-fired by Space and Enter
  // typed to the host.
  const b = e.target.closest('button');
  if (b) b.blur();
});
// Touching the remote screen puts the panel away — it overlaps the picture, and
// reaching back up to the ⓘ to dismiss it is a step nobody should have to take.
//
// Captured on the wrapper rather than the canvas so it runs before the canvas's
// own pointer handlers, and swallowed: the tap that dismisses the panel should
// not also click whatever is underneath it on the host.
vwrap.addEventListener('pointerdown', (e) => {
  const openPanel = !infoPanel.classList.contains('hidden');
  const openTray = quickActions.classList.contains('open');
  if (!openPanel && !openTray) return;
  if (openPanel) toggleInfo(false);
  if (openTray) toggleQuickActions(false);
  e.stopPropagation();
  e.preventDefault();
}, true);

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
window.addEventListener('blur', () => {
  releaseHeldButtons();
  cancelTouch(); // backgrounding mid-drag never delivers touchcancel
});
canvas.addEventListener('contextmenu', e => e.preventDefault());

// ---- touch gestures ----
// A touch device has no buttons and no wheel, so the whole mouse has to come
// from gestures. We take over single-finger touch entirely (preventDefault
// stops the browser synthesising its own mouse events, which would double up
// with ours) and send the clicks ourselves:
//
//   tap                 left click
//   double tap          double click
//   two-finger tap      right click
//   press and hold      grab, then drag until you lift
//   one-finger drag     move the pointer (no button held)
//   two-finger drag     scroll
//
// Right click used to be a double tap, and it could not work. A tap has to send
// its left click immediately — waiting out a double-tap window would put a
// quarter second of lag on every ordinary tap — so the first tap of the pair
// always landed as a real left click first. Select some text, double tap to
// bring up the menu, and the selection was gone before the right click arrived.
//
// A two-finger tap has no such problem: two fingers on the glass is
// unambiguous from the very first touch, so nothing needs delaying and no
// left click is ever sent. It also frees double tap to be a genuine double
// click, which touch could not do at all before.
const SCROLL_PX_PER_NOTCH = 40;  // finger travel for one wheel click
const LONG_PRESS_MS = 450;       // hold this long to grab
const TAP_MOVE_TOL = 12;         // px of drift still counted as a tap, not a drag
const TWO_FINGER_TAP_MS = 400;   // longer than this and it was a hold, not a tap
const TWO_FINGER_TAP_TOL = 16;   // px of drift before it is a scroll, not a tap

let twoFinger = null;
let scrollAccum = 0;
let touch = null;                // the in-flight single-finger gesture

function touchMid(t) {
  return { x: (t[0].clientX + t[1].clientX) / 2, y: (t[0].clientY + t[1].clientY) / 2 };
}

function moveTo(t) {
  const p = toRemote(t);
  predictCursor(p);
  send({ t: 'mmove', x: p.x, y: p.y });
  return p;
}

function cancelTouch() {
  if (touch) {
    clearTimeout(touch.timer);
    if (touch.dragging) send({ t: 'mup', x: lastPos.x, y: lastPos.y, button: 0 });
    touch = null;
  }
}

canvas.addEventListener('touchstart', e => {
  if (e.touches.length === 2) {
    e.preventDefault();      // stop the browser treating it as pan/zoom
    cancelTouch();           // a second finger ends any single-finger gesture
    const mid = touchMid(e.touches);
    // Every two-finger gesture starts as a possible right click and stops being
    // one the moment it moves or is held — that is what separates it from a
    // scroll without either gesture needing to wait for the other.
    twoFinger = { x: mid.x, y: mid.y, startX: mid.x, startY: mid.y, at: performance.now(), tap: true };
    scrollAccum = 0;
    return;
  }
  if (e.touches.length !== 1) return;
  e.preventDefault();
  const t = e.touches[0];
  moveTo(t);                 // put the pointer under the finger before anything else
  touch = {
    x: t.clientX, y: t.clientY,
    moved: false, dragging: false,
    timer: setTimeout(() => {
      // Held still long enough: press and keep holding, so the next movement
      // drags whatever is under the pointer.
      if (!touch || touch.moved) return;
      touch.dragging = true;
      send({ t: 'mdown', x: lastPos.x, y: lastPos.y, button: 0 });
      if (navigator.vibrate) navigator.vibrate(15); // confirm the grab
    }, LONG_PRESS_MS),
  };
}, { passive: false });

canvas.addEventListener('touchmove', e => {
  if (e.touches.length === 2 && twoFinger) {
    e.preventDefault();
    const mid = touchMid(e.touches);
    if (twoFinger.tap &&
        (Math.abs(mid.x - twoFinger.startX) > TWO_FINGER_TAP_TOL ||
         Math.abs(mid.y - twoFinger.startY) > TWO_FINGER_TAP_TOL)) {
      twoFinger.tap = false; // it is a scroll
    }
    // Dragging up scrolls down, as on a phone. Accumulate so slow drags still
    // move eventually instead of rounding away to nothing.
    scrollAccum += (twoFinger.y - mid.y) / SCROLL_PX_PER_NOTCH;
    twoFinger.x = mid.x; twoFinger.y = mid.y;
    const notches = Math.trunc(scrollAccum);
    if (notches !== 0) {
      scrollAccum -= notches;
      send({ t: 'scroll', dx: 0, dy: notches });
    }
    return;
  }
  if (e.touches.length !== 1 || !touch) return;
  e.preventDefault();
  const t = e.touches[0];
  if (!touch.moved &&
      (Math.abs(t.clientX - touch.x) > TAP_MOVE_TOL || Math.abs(t.clientY - touch.y) > TAP_MOVE_TOL)) {
    touch.moved = true;
    if (!touch.dragging) clearTimeout(touch.timer); // moved first: not a hold
  }
  moveTo(t);
}, { passive: false });

canvas.addEventListener('touchend', e => {
  // A two-finger tap fires as soon as the first finger leaves, and clears the
  // candidate so the second lift cannot repeat it.
  if (twoFinger && twoFinger.tap && e.touches.length < 2) {
    if (performance.now() - twoFinger.at <= TWO_FINGER_TAP_MS) {
      e.preventDefault();
      twoFinger.tap = false;
      // Put the pointer where the fingers were before clicking, exactly as a
      // mouse would arrive there — a context menu belongs under the tap.
      const p = moveTo({ clientX: twoFinger.x, clientY: twoFinger.y });
      send({ t: 'mdown', x: p.x, y: p.y, button: 2 });
      send({ t: 'mup', x: p.x, y: p.y, button: 2 });
      if (navigator.vibrate) navigator.vibrate(12);
    } else {
      twoFinger.tap = false; // held too long to be a tap
    }
  }
  if (!e.touches.length) { twoFinger = null; scrollAccum = 0; }
  if (!touch) return;
  e.preventDefault();
  clearTimeout(touch.timer);
  const g = touch;
  touch = null;

  if (g.dragging) {                       // release whatever was grabbed
    send({ t: 'mup', x: lastPos.x, y: lastPos.y, button: 0 });
    return;
  }
  if (g.moved) return;                    // that was a pointer move, not a click

  // Sent immediately, with no double-tap window to wait out. Two taps in quick
  // succession therefore reach the host as two ordinary clicks close together,
  // which is precisely what a double click is — the host decides, using its own
  // double-click interval, and touch gets a real double click for free.
  send({ t: 'mdown', x: lastPos.x, y: lastPos.y, button: 0 });
  send({ t: 'mup', x: lastPos.x, y: lastPos.y, button: 0 });
}, { passive: false });

canvas.addEventListener('touchcancel', () => {
  cancelTouch();
  twoFinger = null;
  scrollAccum = 0;
});
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

// The soft-keyboard box is kept seeded with a run of zero-width spaces.
//
// Backspace on this box is translated by watching what the browser deletes from
// it (below). But the box starts empty while the host field already has text —
// so a backspace against existing text, or against text pasted with 📋, found
// nothing in the box to delete, fired no input event, and never reached the
// host. New text worked because it was in the box; existing text did not. The
// guard gives every backspace something local to consume so the event always
// fires, and being zero-width it is invisible in the input bar.
const KBD_GUARD = '\u200b\u200b\u200b\u200b';
function primeKbd() {
  softkbd.value = KBD_GUARD;
  lastKbdVal = KBD_GUARD;
  try { softkbd.setSelectionRange(KBD_GUARD.length, KBD_GUARD.length); } catch (_) {}
}
function keyTap(code) { send({ t: 'kdown', code }); send({ t: 'kup', code }); }

// ---- sticky modifiers and the special-keys strip ----
// A phone keyboard cannot hold Ctrl/Alt/Win and press another key, so those
// modifiers latch on tap and apply to the *next* key or character, then clear —
// the "sticky keys" model. Latch several before the key for a combo. This is
// what lets Ctrl+C, Alt+F4, Win+E and the like happen at all from touch.
const stickyMods = new Set();

function clearMods() {
  stickyMods.clear();
  document.querySelectorAll('#kbdKeys .kk-mod').forEach(b => b.classList.remove('active'));
}

// emitKey presses code wrapped in whatever modifiers are latched, then releases
// them — the latch is one-shot, matching how a combo is a single action.
function emitKey(code) {
  const mods = [...stickyMods];
  for (const m of mods) send({ t: 'kdown', code: m });
  send({ t: 'kdown', code });
  send({ t: 'kup', code });
  for (const m of mods.reverse()) send({ t: 'kup', code: m });
  if (mods.length) clearMods();
}

// codeForChar maps a typed character to the physical key that produces it, so a
// latched modifier can combine with it — Unicode text injection (the normal
// typing path) carries no key code for Ctrl to attach to. Covers the letters
// and digits every common chord uses; anything else is typed plainly.
function codeForChar(ch) {
  if (/[a-zA-Z]/.test(ch)) return 'Key' + ch.toUpperCase();
  if (/[0-9]/.test(ch)) return 'Digit' + ch;
  return null;
}

// Wire the strip once. Plain keys respect any latched modifiers; the modifier
// buttons toggle their own latch.
document.querySelectorAll('#kbdKeys .kk').forEach(btn => {
  btn.addEventListener('click', (e) => {
    e.preventDefault();
    const mod = btn.dataset.mod;
    if (mod) {
      if (stickyMods.has(mod)) { stickyMods.delete(mod); btn.classList.remove('active'); }
      else { stickyMods.add(mod); btn.classList.add('active'); }
      softkbd.focus(); // keep the OS keyboard up so the next character can follow
      return;
    }
    emitKey(btn.dataset.key);
    softkbd.focus();
  });
});
function toggleKbd(show) {
  if (show === undefined) show = kbdbar.classList.contains('hidden');
  kbdbar.classList.toggle('hidden', !show);
  // A real on/off state. This one genuinely is a toggle, and without a state of
  // its own it was readable only by whether a stray :hover happened to be stuck
  // to it — which is not a state, and on a touchscreen was usually wrong.
  const kbdBtn = document.getElementById('kbd');
  if (kbdBtn) kbdBtn.classList.toggle('active', show);
  if (show) {
    softkbd.disabled = false; // must be enabled before it can take focus
    primeKbd();
    softkbd.focus();
  } else {
    softkbd.blur();
    clearMods();
    // Blur alone is not enough: a hidden-but-focusable input is still the page's
    // last editable element, so mobile browsers re-raise the on-screen keyboard
    // on the next tap anywhere on the screen. A disabled input cannot take focus
    // at all, so the keyboard stays down until ⌨ is pressed again.
    softkbd.disabled = true;
  }
  updateKbdLayout();
}
// The bar starts hidden, so the input must start unfocusable too.
softkbd.disabled = true;
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
  layoutCanvas(); // the wrapper just changed size
}
if (window.visualViewport) {
  window.visualViewport.addEventListener('resize', () => {
    // The OS keyboard can be dismissed by the system back/down gesture, which
    // never reaches our hide button. The viewport growing back to (nearly) full
    // height is the signal; close our bar so the input does not sit focused,
    // silently re-raising the keyboard on the next tap.
    const vv = window.visualViewport;
    const kbH = window.innerHeight - vv.height - vv.offsetTop;
    if (!kbdbar.classList.contains('hidden') && kbH < 100) { toggleKbd(false); return; }
    updateKbdLayout();
  });
  window.visualViewport.addEventListener('scroll', updateKbdLayout);
}
document.getElementById('kbd').addEventListener('click', () => toggleKbd());
document.getElementById('kbdHide').addEventListener('click', () => toggleKbd(false));
function sendEnter() { keyTap('Enter'); primeKbd(); softkbd.focus(); }
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
  for (let i = 0; i < lastKbdVal.length - common; i++) emitKey('Backspace');
  const added = v.slice(common);
  let chorded = false;
  for (const ch of added) {
    const code = stickyMods.size ? codeForChar(ch) : null;
    if (code) { emitKey(code); chorded = true; } // latched Ctrl + typed "c" -> Ctrl+C
    else { if (stickyMods.size) clearMods(); send({ t: 'type', text: ch }); }
  }
  lastKbdVal = v;
  // A chorded character went to the host as a key combo, not as the literal it
  // still is in the box — reset the box so that stale literal can't later emit a
  // spurious Backspace. Otherwise, refill the guard only once it has been eaten,
  // never mid-typing, so an IME composing text is left undisturbed.
  if (chorded || !v.startsWith(KBD_GUARD)) primeKbd();
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

// Tapping the remote screen dismisses the on-screen keyboard. Without this the
// input keeps focus and mobile browsers re-raise the OS keyboard on every
// subsequent tap — especially after the keyboard was dismissed with the system
// back/down gesture, which never runs our hide path.
canvas.addEventListener('pointerdown', () => {
  if (!kbdbar.classList.contains('hidden')) toggleKbd(false);
}, { passive: true });

function releaseHeldKeys() {
  for (const code of heldKeys) send({ t: 'kup', code });
  heldKeys.clear();
}

// Focus loss, tab switch, and page hide all end key delivery without a keyup.
window.addEventListener('blur', releaseHeldKeys);
window.addEventListener('pagehide', () => { releaseHeldKeys(); cancelTouch(); });
document.addEventListener('visibilitychange', () => {
  if (document.hidden) releaseHeldKeys();
});

// ---- fullscreen / installed-app mode ----
// Opened from the installed app, the viewer replaces the dashboard in the same
// window, so it offers a way back and takes the whole screen — the point of app
// mode being that there is no browser chrome eating space.
const appMode = params.get('app') === '1';
const fsBtn = document.getElementById('fsBtn');
const backBtn = document.getElementById('backBtn');

function toggleFullscreen() {
  if (document.fullscreenElement) {
    document.exitFullscreen().catch(() => {});
  } else {
    document.documentElement.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
  }
}
fsBtn.addEventListener('click', () => { toggleFullscreen(); fsBtn.blur(); });

if (appMode) {
  backBtn.classList.remove('hidden');
  backBtn.addEventListener('click', () => {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    location.href = '/';
  });
  // Fullscreen needs a user gesture, and the click that opened this page does
  // not carry over across navigation — so take the first interaction here.
  const grab = () => {
    if (!document.fullscreenElement) {
      document.documentElement.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
    }
    window.removeEventListener('pointerdown', grab);
    window.removeEventListener('keydown', grab);
  };
  window.addEventListener('pointerdown', grab);
  window.addEventListener('keydown', grab);
}

// Entering or leaving fullscreen changes the wrapper size.
document.addEventListener('fullscreenchange', layoutCanvas);

// ---- clipboard sync ----
let lastClip = null;
let pendingHostClip = null; // resolver for a Copy that is waiting on the host

// Host -> viewer: the host's clipboard changed.
//
// The obvious implementation — call navigator.clipboard.writeText here — cannot
// work, and quietly did nothing for a long time. writeText requires transient
// user activation, and this runs on a websocket message with no gesture in
// flight, so browsers reject it; the rejection was swallowed, so nothing ever
// appeared on the phone and there was no error anywhere to say why. Copying on
// the host and pasting on the device simply never worked.
//
// The write now happens inside the operator's own tap instead (copyFromHost).
// This function keeps an opportunistic attempt, which does succeed in a focused
// desktop tab, but nothing depends on it.
function setLocalClipboard(text) {
  if (text == null) return;
  const changed = text !== lastClip;
  lastClip = text;
  if (pendingHostClip) {           // someone tapped Copy and is waiting for this
    const resolve = pendingHostClip;
    pendingHostClip = null;
    resolve(text);
  }
  if (!changed) return;
  if (!navigator.clipboard || !navigator.clipboard.writeText) return;
  navigator.clipboard.writeText(text).catch(() => {
    // Refused for want of a gesture, which is the normal case on a phone. The
    // host has copied something and this device cannot be given it unasked — so
    // arm the Copy button and let one tap collect it. This is what makes
    // copying by any means on the host (Ctrl+C, the context menu, an app's own
    // button) reachable from the device at all.
    armCopy(text);
  });
}

// awaitHostClip resolves with the host's clipboard once it arrives.
//
// Falls back to the last known value on timeout rather than failing: the agent
// only reports the clipboard when it *changes*, so copying the same text twice
// produces no message at all, and treating that as an error would be wrong.
function awaitHostClip(ms) {
  return new Promise(resolve => {
    pendingHostClip = resolve;
    setTimeout(() => {
      if (pendingHostClip === resolve) {
        pendingHostClip = null;
        resolve(lastClip);
      }
    }, ms);
  });
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

// Paste this device's clipboard on the host. The Ctrl/Cmd+V path already does
// this, but on a phone there is no Ctrl to hold, so the whole flow needs a
// button: read the local clipboard, sync it to the host, then synthesize the
// paste chord there — including Control itself, which the keyboard path leaves
// out because the operator is physically holding it.
//
// navigator.clipboard.readText needs a secure context and a user gesture; a
// button tap is a gesture, and the viewer already requires https for H.264.
// Some browsers show a paste prompt on the first tap — that is the browser
// asking, not a bug.
// Copy on the host and put the result on this device.
//
// The hard part is not the copy, it is the hand-back. Writing another device's
// clipboard needs transient user activation, and the host's text does not
// arrive until a round trip later, by which time the tap that authorised it is
// long over. So the write is started *inside* the tap and handed a promise for
// the text: ClipboardItem accepts one, and the browser keeps the activation
// alive until it settles. This is exactly what that API is for.
//
// Where the promise form is unsupported, the text is held and the button arms
// itself — a second tap is a fresh gesture and places it. Two taps, but honest.
const copyBtn = document.getElementById('copyBtn');
let armedClip = null; // text waiting for a second tap to place it

function sendHostCopy() {
  send({ t: 'kdown', code: 'ControlLeft' });
  send({ t: 'kdown', code: 'KeyC' });
  send({ t: 'kup', code: 'KeyC' });
  send({ t: 'kup', code: 'ControlLeft' });
}

// legacyCopy places text using the pre-permissions mechanism: select it in a
// throwaway field and let the browser's own copy command take it.
//
// document.execCommand is deprecated and still the most widely allowed way to
// write a clipboard from a user gesture — it predates the Clipboard API's
// permission model, so the rules that refuse writeText do not apply. Kept as a
// last resort precisely because those rules vary by browser, by engine version
// and by whether the page is installed, and are not worth predicting.
//
// readonly is what stops mobile browsers raising the on-screen keyboard for a
// field the operator never sees, and the explicit selection range is what iOS
// needs in place of select() alone.
function legacyCopy(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;';
  document.body.appendChild(ta);
  const prev = document.activeElement;
  let ok = false;
  try {
    ta.focus();
    ta.select();
    if (ta.setSelectionRange) ta.setSelectionRange(0, text.length);
    ok = document.execCommand('copy');
  } catch (_) {
    ok = false;
  }
  ta.remove();
  if (prev && prev.focus) { try { prev.focus(); } catch (_) {} }
  return ok;
}

// placeOnDevice writes text to this device's clipboard, trying each mechanism
// the browser might allow. Must be called from inside a user gesture.
async function placeOnDevice(text) {
  let err = null;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return { ok: true };
    } catch (e) {
      err = e;
    }
  }
  if (legacyCopy(text)) return { ok: true };
  return { ok: false, err };
}

// clipboardBlockedMsg names the actual reason rather than "blocked", which is
// the same word for a dozen unrelated browser rules.
function clipboardBlockedMsg(err) {
  if (!window.isSecureContext) return 'needs https to copy';
  if (!navigator.clipboard) return 'no clipboard API here';
  const name = err && err.name ? err.name : 'unknown';
  if (name === 'NotAllowedError' && !document.hasFocus()) return 'blocked: page not focused';
  return 'blocked: ' + name;
}

function armCopy(text) {
  armedClip = text;
  copyBtn.classList.add('active');
  flashState('tap ⧉ again to place it here');
  setTimeout(() => {
    if (armedClip === text) { armedClip = null; copyBtn.classList.remove('active'); }
  }, 8000);
}

copyBtn.addEventListener('click', async (e) => {
  e.currentTarget.blur();

  // Second tap of the fallback path: a fresh gesture, so a plain write works.
  if (armedClip !== null) {
    const text = armedClip;
    armedClip = null;
    copyBtn.classList.remove('active');
    const res = await placeOnDevice(text);
    flashState(res.ok ? 'copied to this device' : clipboardBlockedMsg(res.err));
    return;
  }

  const arrival = awaitHostClip(2500);
  sendHostCopy();

  if (window.ClipboardItem && navigator.clipboard && navigator.clipboard.write) {
    try {
      await navigator.clipboard.write([new ClipboardItem({
        'text/plain': arrival.then(t => new Blob([t == null ? '' : t], { type: 'text/plain' })),
      })]);
      const got = await arrival;
      // Distinguish the two failures that look identical from the outside: the
      // host never reported a copy, versus the copy never reached this device.
      flashState(got == null ? 'host reported no copy' : 'copied to this device');
      return;
    } catch (_) {
      // Promise-backed writes unsupported or refused; fall through.
    }
  }

  const text = await arrival;
  if (text == null) { flashState('host reported no copy'); return; }
  armCopy(text);
});

// Context menu for whatever is selected on the host, without moving the
// pointer — which a right click cannot do, since arriving there can disturb the
// selection. Same key as the strip's Menu button, hoisted into the bar so it
// does not require opening the keyboard first.
document.getElementById('menuBtn').addEventListener('click', (e) => {
  keyTap('ContextMenu');
  e.currentTarget.blur();
});

document.getElementById('pasteBtn').addEventListener('click', async () => {
  let text = '';
  try {
    text = await navigator.clipboard.readText();
  } catch (_) {
    flashState('clipboard blocked — copy again, then retry');
    return;
  }
  if (!text) { flashState('local clipboard is empty'); return; }
  lastClip = text;
  // Same ordered socket, processed sequentially by the agent: the clipboard is
  // set before the keystroke lands, so no delay is needed between them.
  send({ t: 'clip', clip: text });
  send({ t: 'kdown', code: 'ControlLeft' });
  send({ t: 'kdown', code: 'KeyV' });
  send({ t: 'kup', code: 'KeyV' });
  send({ t: 'kup', code: 'ControlLeft' });
  flashState('pasted');
});

qualityEl.addEventListener('change', () => send({ t: 'params', quality: parseInt(qualityEl.value, 10) }));
// Task Manager (Ctrl+Shift+Esc). Ctrl+Alt+Del can't be synthesized on Windows
// (protected sequence) and its secure desktop isn't capturable anyway; Task
// Manager is what operators actually need and it works via normal injection.
// Alt+Tab, which cannot be caught from a physical keyboard: the OS switches the
// *local* machine's windows before the page ever sees the combo. So it is a
// button — and one that mirrors how alt-tab actually feels. The first tap holds
// Alt down and presses Tab (the host shows its switcher); each further tap
// within the window presses Tab again to cycle; a pause releases Alt, which
// commits the highlighted choice. Tap once and wait = switch to the previous
// window; tap-tap-tap = cycle several, then pause.
let altTabTimer = null;
document.getElementById('altTab').addEventListener('click', (e) => {
  if (altTabTimer === null) {
    send({ t: 'kdown', code: 'AltLeft' }); // begin: hold Alt across taps
  } else {
    clearTimeout(altTabTimer);
  }
  send({ t: 'kdown', code: 'Tab' });
  send({ t: 'kup', code: 'Tab' });
  altTabTimer = setTimeout(() => {
    send({ t: 'kup', code: 'AltLeft' }); // commit the selection
    altTabTimer = null;
  }, 1200);
  e.currentTarget.blur();
});

document.getElementById('taskMgr').addEventListener('click', (e) => {
  for (const c of ['ControlLeft', 'ShiftLeft', 'Escape']) send({ t: 'kdown', code: c });
  for (const c of ['Escape', 'ShiftLeft', 'ControlLeft']) send({ t: 'kup', code: c });
  // Belt-and-suspenders: make sure no modifier is left stuck on the host if it
  // dropped a key-up while Task Manager stole focus.
  for (const c of ['ControlLeft', 'ControlRight', 'ShiftLeft', 'ShiftRight', 'AltLeft', 'AltRight']) send({ t: 'kup', code: c });
  e.currentTarget.blur(); // return focus to the page so canvas input keeps flowing
});

// The top bar stays visible (keyboard / display / codec controls are always reachable).

// The measured link rate the encoder is aiming at. Worth showing: when motion
// looks blocky the useful question is whether the connection is the limit, and
// that used to be unanswerable without reading the host's log.
let hostFps = null;
function showLinkRate(m) {
  if (!linkEl) return;
  const rate = (k) => k >= 1000 ? (k / 1000).toFixed(1) + ' Mbps' : k + ' kbps';
  // Show what the host is actually putting on the wire, not the ceiling it is
  // aiming at. The ceiling is an internal control value and reading it as a
  // measurement is how a stuck estimate went unnoticed.
  if (typeof m.txkbps === 'number') linkEl.textContent = rate(m.txkbps);
  hostFps = typeof m.fps === 'number' ? m.fps : null;

  // Where the host's time actually goes. A low framerate has several unrelated
  // causes — capture, encoding, the socket, or nothing changing on screen — and
  // they are indistinguishable from here without this.
  //
  // Shown as text rather than a tooltip: these numbers exist to be read while
  // something is going wrong, and a title attribute is unreachable on a phone,
  // which is where this viewer is most used.
  if (diagEl) {
    const codec = currentCodec === 'webcodecs-h264' ? 'h264' : 'jpeg';
    diagEl.textContent =
      `${codec} · cap ${m.capms} enc ${m.encms} tx ${m.txms} ms` +
      ` · idle ${m.idle}/s · aim ${rate(m.kbps || 0)}`;
  }
}

// fps meter + keepalive
setInterval(() => {
  // "received / sent by the host" — if these differ the frames are queueing
  // somewhere in between, which is a completely different problem from the host
  // not producing them.
  fpsEl.textContent = hostFps === null ? frames + ' fps' : frames + '/' + hostFps + ' fps';
  frames = 0;
}, 1000);

// Report what we are actually receiving, once a second.
//
// The host sizes its stream to the connection, and it works that out from its
// own writes blocking. That only reaches it if every hop in between pushes
// back, which cannot be relied on: a reverse proxy or an overlay network in the
// path will happily buffer megabytes and the host then concludes the link is
// faster than it is. What arrives here is the one number no amount of buffering
// upstream can inflate.
//
// A running total, not a rate. Subtracting totals gives the host exactly what is
// still queued between us; comparing rates cannot, because a receiver always
// trails a sender and that gap is data in flight on a healthy link.
setInterval(() => {
  if (ws && ws.readyState === WebSocket.OPEN) send({ t: 'rx', bytes: rxTotal });
}, 1000);
setInterval(() => send({ t: 'ping' }), 20000);

connect();
