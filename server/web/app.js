// autormm dashboard: polls /api/hosts and renders host cards.
const grid = document.getElementById('grid');
const emptyEl = document.getElementById('empty');
const summaryEl = document.getElementById('summary');
const tokenBtn = document.getElementById('tokenBtn');

const TOKEN_KEY = 'autormm_token';

// ---- installed-app (PWA) support ----
// Registering a service worker is what makes the dashboard installable. It
// caches nothing; see sw.js. Requires a secure context (https or localhost),
// so over a plain-http LAN address this simply no-ops.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {});
  });
}

// standalone means we are running as an installed app rather than a browser
// tab, so there is no browser chrome to spend screen space on.
function isStandalone() {
  return window.matchMedia('(display-mode: standalone)').matches ||
    window.matchMedia('(display-mode: fullscreen)').matches ||
    navigator.standalone === true;
}
function token() { return localStorage.getItem(TOKEN_KEY) || ''; }

// ---- login ----
const loginModal = document.getElementById('loginModal');
async function showLogin() {
  const wasHidden = loginModal.classList.contains('hidden');
  if (!wasHidden) return; // already open — don't re-fetch or steal focus (poll runs on a timer)
  document.getElementById('loginErr').textContent = '';
  document.getElementById('loginForgotBox').classList.add('hidden');
  loginModal.classList.remove('hidden');
  try {
    const info = await (await fetch('/api/authinfo')).json();
    const setup = !!info.needs_setup;
    // First run: show the account-creation form and hide the sign-in bits.
    document.getElementById('loginTitle').textContent = setup ? 'Set up autormm' : 'Sign in to autormm';
    document.getElementById('loginSetup').classList.toggle('hidden', !setup);
    document.getElementById('loginPw').classList.toggle('hidden', setup || !info.password_login);
    document.getElementById('loginLinks').classList.toggle('hidden', setup);
    document.getElementById('loginTokenBox').classList.toggle('hidden', setup || info.password_login);
    if (setup) { document.getElementById('setupUser').focus(); }
    else { (info.password_login ? document.getElementById('loginUser') : document.getElementById('loginToken')).focus(); }
  } catch (_) {}
}

async function doSetup() {
  const username = document.getElementById('setupUser').value.trim();
  const password = document.getElementById('setupPass').value;
  const err = document.getElementById('loginErr');
  err.textContent = '';
  try {
    const res = await fetch('/api/setup', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    if (!res.ok) { err.textContent = 'Setup failed: ' + await res.text(); return; }
    const d = await res.json();
    localStorage.setItem(TOKEN_KEY, d.token);
    hideLogin(); poll();
  } catch (e) { err.textContent = 'Setup error: ' + e; }
}
document.getElementById('setupBtn').addEventListener('click', doSetup);
document.getElementById('setupPass').addEventListener('keydown', e => { if (e.key === 'Enter') doSetup(); });
document.getElementById('loginForgot').addEventListener('click', e => { e.preventDefault(); document.getElementById('loginForgotBox').classList.toggle('hidden'); });
function hideLogin() { loginModal.classList.add('hidden'); }

async function doLogin() {
  const username = document.getElementById('loginUser').value.trim();
  const password = document.getElementById('loginPass').value;
  const err = document.getElementById('loginErr');
  err.textContent = '';
  try {
    const res = await fetch('/api/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    if (!res.ok) { err.textContent = res.status === 401 ? 'Invalid username or password' : ('Login failed: ' + await res.text()); return; }
    const d = await res.json();
    localStorage.setItem(TOKEN_KEY, d.token);
    document.getElementById('loginPass').value = '';
    hideLogin(); poll();
  } catch (e) { err.textContent = 'Login error: ' + e; }
}
document.getElementById('loginBtn').addEventListener('click', doLogin);
document.getElementById('loginPass').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
document.getElementById('loginTokenToggle').addEventListener('click', e => { e.preventDefault(); document.getElementById('loginTokenBox').classList.toggle('hidden'); });
document.getElementById('loginTokenBtn').addEventListener('click', () => {
  const t = document.getElementById('loginToken').value.trim();
  if (t) { localStorage.setItem(TOKEN_KEY, t); hideLogin(); poll(); }
});
// ---- updates ----
const updateModal = document.getElementById('updateModal');
function authFetch(path, method, body) {
  const opts = { method: method || 'GET', headers: { Authorization: 'Bearer ' + token() } };
  if (body !== undefined) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
  return fetch(path, opts);
}

// authJSON is authFetch plus the two steps every caller needs and one of them
// will eventually forget: decoding the body, and noticing that the hub said no.
//
// authFetch hands back a Response, whose .ok means "HTTP 200" — not "the host
// did the thing". Reading r.ok straight off it therefore reports success for
// every reply the hub managed to send, including one whose body says the host
// refused. That mistake shipped in the reboot and wake buttons; this exists so
// it cannot recur.
async function authJSON(path, method, body) {
  const r = await authFetch(path, method, body);
  if (r.status === 401) { showLogin(); throw new Error('not signed in'); }
  if (!r.ok) {
    const text = (await r.text().catch(() => '')).trim();
    throw new Error(text || ('HTTP ' + r.status));
  }
  return r.json();
}

// ---- accounts ----
const acctModal = document.getElementById('acctModal');
document.getElementById('acctBtn').addEventListener('click', () => {
  if (!token()) { showLogin(); return; }
  acctModal.classList.remove('hidden');
  document.getElementById('acctErr').textContent = '';
  loadAccounts();
});
document.getElementById('acctClose').addEventListener('click', () => acctModal.classList.add('hidden'));
acctModal.addEventListener('click', e => { if (e.target === acctModal) acctModal.classList.add('hidden'); });

async function loadAccounts() {
  const el = document.getElementById('acctList');
  try {
    const r = await authFetch('/api/admin/accounts');
    if (r.status === 401) { acctModal.classList.add('hidden'); showLogin(); return; }
    const names = (await r.json()).accounts || [];
    el.innerHTML = names.length
      ? 'Accounts: ' + names.map(n => `${escapeHtml(n)} <a href="#" class="acct-rm" data-u="${escapeHtml(n)}">✕</a>`).join(' · ')
      : 'No password accounts yet — add one below.';
    el.querySelectorAll('.acct-rm').forEach(a => a.onclick = async e => {
      e.preventDefault();
      if (confirm(`Remove account "${a.dataset.u}"?`)) { await authFetch('/api/admin/remove', 'POST', { username: a.dataset.u }); loadAccounts(); }
    });
  } catch (_) {}
}
document.getElementById('acctSave').addEventListener('click', async () => {
  const username = document.getElementById('acctUser').value.trim();
  const password = document.getElementById('acctPass').value;
  const err = document.getElementById('acctErr');
  err.style.color = ''; err.textContent = '';
  const r = await authFetch('/api/admin/set', 'POST', { username, password });
  if (!r.ok) { err.textContent = await r.text(); return; }
  document.getElementById('acctUser').value = '';
  document.getElementById('acctPass').value = '';
  err.style.color = '#3fb950'; err.textContent = 'Saved.';
  setTimeout(() => { err.style.color = ''; err.textContent = ''; }, 2000);
  loadAccounts();
});
document.getElementById('updatesBtn').addEventListener('click', () => {
  updateModal.classList.remove('hidden');
  document.getElementById('updStatus').textContent = '';
  document.getElementById('updApply').classList.add('hidden');
  checkUpdates();
});
document.getElementById('updateClose').addEventListener('click', () => updateModal.classList.add('hidden'));
updateModal.addEventListener('click', e => { if (e.target === updateModal) updateModal.classList.add('hidden'); });

async function checkUpdates() {
  const st = document.getElementById('updStatus');
  st.textContent = 'checking…';
  try {
    const d = await (await authFetch('/api/update/check')).json();
    document.getElementById('updCurrent').textContent = d.current || '—';
    if (d.error) { st.textContent = 'check failed: ' + d.error; return; }
    const apply = document.getElementById('updApply');
    if (d.available) {
      st.textContent = `Update available: ${d.latest}`;
      apply.textContent = `Update hub to ${d.latest}`;
      apply.classList.remove('hidden');
    } else {
      st.textContent = `Up to date (latest ${d.latest || d.current})`;
      apply.classList.add('hidden');
    }
  } catch (e) { st.textContent = 'check error: ' + e; }
}
// Install the H.264 encoder fleet-wide. ffmpeg is not shipped with the agent
// (a libx264 build is GPL), so hosts fetch it from upstream on request.
document.getElementById('codecInstall').addEventListener('click', async () => {
  const st = document.getElementById('updStatus');
  if (!confirm('Download and install the H.264 encoder (ffmpeg) on every online streaming host?')) return;
  st.textContent = 'installing ffmpeg… this downloads ~30 MB per host and can take a few minutes';
  try {
    const r = await authFetch('/api/codec/install-all', 'POST', {});
    if (!r.ok) { st.textContent = 'install failed: ' + ((await r.text().catch(() => '')).trim() || r.status); return; }
    const d = await r.json();
    const rs = d.results || [];
    if (!rs.length) { st.textContent = 'no online streaming hosts to install on'; return; }
    const ok = rs.filter(x => x.ok).length;
    st.textContent = `H.264 encoder: ${ok}/${rs.length} host${rs.length === 1 ? '' : 's'} ready`
      + (ok < rs.length ? ' — ' + rs.filter(x => !x.ok).map(x => (x.hostname || x.agent_id) + ': ' + (x.detail || 'failed')).join('; ') : '');
  } catch (e) { st.textContent = 'install error: ' + e; }
});

document.getElementById('updCheck').addEventListener('click', checkUpdates);
document.getElementById('updApply').addEventListener('click', async () => {
  if (!confirm('Update the hub now? It downloads the new version and restarts (brief downtime).')) return;
  document.getElementById('updStatus').textContent = 'updating…';
  try { await authFetch('/api/update/apply', 'POST'); } catch (_) {}
  waitForHubAndReload(); // stay signed in: overlay until the hub is back, then reload
});

// Show a "reconnecting" overlay while the hub restarts, then reload once it's
// healthy again. localStorage keeps the login token, so you stay signed in.
function waitForHubAndReload() {
  updateModal.classList.add('hidden');
  let ov = document.getElementById('updatingOverlay');
  if (!ov) {
    ov = document.createElement('div');
    ov.id = 'updatingOverlay';
    ov.className = 'updating-overlay';
    ov.innerHTML = '<div class="updating-box"><div class="spin"></div>Updating the hub…<div class="muted">reconnecting</div></div>';
    document.body.appendChild(ov);
  }
  ov.classList.remove('hidden');
  let ok = 0;
  const iv = setInterval(async () => {
    try {
      const r = await fetch('/healthz', { cache: 'no-store' });
      if (r.ok) { if (++ok >= 2) { clearInterval(iv); location.reload(); } } else { ok = 0; }
    } catch (_) { ok = 0; }
  }, 2000);
}
document.getElementById('updPush').addEventListener('click', async () => {
  const st = document.getElementById('updStatus');
  st.textContent = 'notifying hosts…';
  try {
    const d = await (await authFetch('/api/update/push', 'POST')).json();
    st.textContent = `Told ${d.notified} online host${d.notified === 1 ? '' : 's'} to update.`;
  } catch (e) { st.textContent = 'push error: ' + e; }
});

tokenBtn.title = 'Sign in / out';
tokenBtn.addEventListener('click', () => {
  if (token() && confirm('Sign out of autormm?')) { localStorage.removeItem(TOKEN_KEY); }
  showLogin();
});

const cards = new Map(); // agentID -> element
let lastHosts = [];
const detail = { agent: null, range: '6h' };

function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(n < 10 && i > 0 ? 1 : 0) + ' ' + u[i];
}
function fmtUptime(s) {
  if (!s) return '—';
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

async function poll() {
  if (!token()) { if (loginModal.classList.contains('hidden')) showLogin(); return; }
  try {
    const res = await fetch('/api/hosts', { headers: { Authorization: 'Bearer ' + token() } });
    if (res.status === 401) { localStorage.removeItem(TOKEN_KEY); showLogin(); return; }
    const hosts = await res.json();
    render(hosts || []);
    fetchAlerts();
  } catch (e) {
    summaryEl.textContent = 'connection error';
  }
}

async function fetchAlerts() {
  const badge = document.getElementById('alertBadge');
  try {
    const res = await fetch('/api/alerts', { headers: { Authorization: 'Bearer ' + token() } });
    const alerts = (await res.json()) || [];
    if (alerts.length) {
      badge.textContent = `⚠ ${alerts.length} alert${alerts.length > 1 ? 's' : ''}`;
      badge.title = alerts.map(a => a.message).join('\n');
      badge.classList.remove('hidden');
    } else {
      badge.classList.add('hidden');
    }
  } catch (e) {
    badge.classList.add('hidden');
  }
}

// Filter the grid by tag. Tags were recorded at enrolment and then used by
// nothing; on a fleet past a screenful, being able to look at just the storage
// boxes is the difference between a dashboard and a wall.
const tagFilterEl = document.getElementById('tagFilter');
let tagFilter = localStorage.getItem('autormm_tagfilter') || '';
tagFilterEl.addEventListener('change', () => {
  tagFilter = tagFilterEl.value;
  localStorage.setItem('autormm_tagfilter', tagFilter);
  render(lastHosts);
});

function hostTags(h) {
  return (h.tags || '').split(/[,;\s]+/).map(t => t.trim()).filter(Boolean);
}

function refreshTagFilter(hosts) {
  const tags = [...new Set(hosts.flatMap(hostTags).map(t => t.toLowerCase()))].sort();
  // Nothing to filter by on a fleet with no tags: hide the control entirely
  // rather than offering an empty dropdown.
  tagFilterEl.classList.toggle('hidden', tags.length === 0);
  const want = ['', ...tags].join('|');
  if (tagFilterEl.dataset.built === want) { tagFilterEl.value = tagFilter; return; }
  tagFilterEl.dataset.built = want;
  tagFilterEl.innerHTML = '<option value="">All tags</option>' +
    tags.map(t => `<option value="${escapeHtml(t)}">${escapeHtml(t)}</option>`).join('');
  // A filter for a tag that no longer exists would hide the whole fleet.
  if (tagFilter && !tags.includes(tagFilter)) { tagFilter = ''; localStorage.removeItem('autormm_tagfilter'); }
  tagFilterEl.value = tagFilter;
}

function render(allHosts) {
  lastHosts = allHosts;
  loadNetChecks();
  refreshTagFilter(allHosts);
  const hosts = tagFilter
    ? allHosts.filter(h => hostTags(h).some(t => t.toLowerCase() === tagFilter))
    : allHosts;
  emptyEl.classList.toggle('hidden', hosts.length > 0);
  const online = hosts.filter(h => h.online).length;
  summaryEl.textContent = `${online}/${hosts.length} online` + (tagFilter ? ` · ${tagFilter}` : '');

  const seen = new Set();
  for (const h of hosts) {
    seen.add(h.agent_id);
    let el = cards.get(h.agent_id);
    if (!el) {
      el = document.getElementById('cardTpl').content.firstElementChild.cloneNode(true);
      cards.set(h.agent_id, el);
      grid.appendChild(el);
    }
    updateCard(el, h);
  }
  for (const [id, el] of cards) {
    if (!seen.has(id)) { el.remove(); cards.delete(id); }
  }
  if (detail.agent) refreshDetailLive();
}

function updateCard(el, h) {
  const status = el.querySelector('.status');
  status.className = 'status ' + (h.online ? 'online' : 'offline');
  el.querySelector('.name').textContent = h.hostname || h.agent_id;
  el.querySelector('.platform').textContent = `${h.platform || h.os} · ${h.arch}`;

  const alerts = el.querySelector('.alerts');
  alerts.innerHTML = '';
  for (const a of (h.alerts || [])) {
    const c = document.createElement('span');
    c.className = 'chip' + (/offline|full|high|failing/.test(a) ? ' bad' : '');
    c.textContent = a;
    alerts.appendChild(c);
  }

  const m = h.metrics;
  const cpu = m ? m.cpu_percent : 0;
  const mem = m ? m.mem_percent : 0;
  setBar(el.querySelector('.cpu'), cpu);
  setBar(el.querySelector('.mem'), mem);
  el.querySelector('.cpuVal').textContent = m ? cpu.toFixed(0) + '%' : '—';
  // Temperature next to load, for the hosts that can report it.
  const cpuLabel = el.querySelector('.metric label');
  if (cpuLabel) cpuLabel.textContent = (m && m.cpu_temp_c) ? `CPU ${Math.round(m.cpu_temp_c)}°` : 'CPU';
  el.querySelector('.memVal').textContent = m ? mem.toFixed(0) + '%' : '—';

  renderGPUs(el.querySelector('.gpu-metrics'), (m && m.gpus) || []);

  sparkline(el.querySelector('.cpuSpark'), h.cpu_history || []);

  const det = el.querySelector('.details');
  if (m) {
    const disk = (m.disks || []).map(d => `${d.mount} ${d.percent.toFixed(0)}%`).join('  ');
    det.textContent =
      `up ${fmtUptime(m.uptime_secs)}  ·  load ${m.load1.toFixed(2)}\n` +
      `mem ${fmtBytes(m.mem_used)} / ${fmtBytes(m.mem_total)}\n` +
      `net ↓${fmtBytes(m.net_recv)}/s ↑${fmtBytes(m.net_sent)}/s\n` +
      (m.gpus || []).map(g => `${g.name}  ${fmtBytes(g.mem_used)} / ${fmtBytes(g.mem_total)}\n`).join('') +
      (disk ? disk : '');
  } else {
    det.textContent = h.online ? 'waiting for telemetry…' : `last seen ${new Date(h.last_seen).toLocaleString()}`;
  }

  const btn = el.querySelector('.remote');
  btn.disabled = !(h.online && h.can_stream);
  btn.title = h.can_stream ? 'Open remote desktop' : 'Screen streaming not available on this host';
  btn.onclick = (e) => { e.stopPropagation(); startRemote(h); };

  const term = el.querySelector('.term');
  term.disabled = !(h.online && h.can_exec);
  term.title = h.can_exec ? 'Open a terminal' : 'Shell access is disabled on this host';
  term.onclick = (e) => { e.stopPropagation(); startTerminal(h); };

  el.onclick = () => openDetail(h.agent_id);
}

// GPU utilisation and VRAM, for hosts that report them. Hosts without an NVIDIA
// GPU send no gpus field at all, so the whole block stays hidden rather than
// showing empty rows on every machine that hasn't got one.
function renderGPUs(wrap, gpus) {
  if (!wrap) return;
  wrap.classList.toggle('hidden', gpus.length === 0);
  if (!gpus.length) { wrap.innerHTML = ''; return; }

  // Rebuild only when the number of cards changes. Re-rendering every refresh
  // would restart the bars' width transition a few times a second and make them
  // twitch instead of glide.
  if (wrap.childElementCount !== gpus.length * 2) {
    wrap.innerHTML = gpus.map((g, i) => {
      const n = gpus.length > 1 ? ' ' + i : ''; // index only when it disambiguates
      return `<div class="metric"><label>GPU${n}</label><div class="bar"><i class="gpubar u${i}"></i></div><span class="val ut${i}"></span></div>` +
             `<div class="metric"><label>VRAM${n}</label><div class="bar"><i class="gpubar v${i}"></i></div><span class="val vt${i}"></span></div>`;
    }).join('');
  }
  gpus.forEach((g, i) => {
    setBar(wrap.querySelector('.u' + i), g.percent);
    setBar(wrap.querySelector('.v' + i), g.mem_percent);
    wrap.querySelector('.ut' + i).textContent = (g.percent || 0).toFixed(0) + '%';
    // Bytes rather than a percentage: the bar already shows the proportion, and
    // "18.2 GB" is the number you actually want when deciding if a model fits.
    wrap.querySelector('.vt' + i).textContent = fmtBytes(g.mem_used);
    const row = wrap.querySelectorAll('.metric');
    const title = `${g.name} — ${fmtBytes(g.mem_used)} of ${fmtBytes(g.mem_total)} VRAM` +
      (g.temp_c ? `, ${g.temp_c.toFixed(0)}°C` : '');
    if (row[i * 2]) row[i * 2].title = title;
    if (row[i * 2 + 1]) row[i * 2 + 1].title = title;
  });
}

function setBar(el, pct) {
  el.style.width = Math.max(0, Math.min(100, pct)) + '%';
  el.classList.toggle('hot', pct >= 85);
}

function sparkline(poly, data) {
  if (!data.length) { poly.setAttribute('points', ''); return; }
  const n = data.length;
  const pts = data.map((v, i) => {
    const x = (i / Math.max(1, n - 1)) * 100;
    const y = 24 - (Math.max(0, Math.min(100, v)) / 100) * 24;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  poly.setAttribute('points', pts.join(' '));
}

async function startRemote(h) {
  // 30, matching the viewer's own reconnect path. These two must agree: this is
  // the request that starts the session, and for a long time it asked for 60
  // while the viewer asked for 30, so every session began life at the wrong
  // framerate.
  openSession(h, { fps: 30, quality: 90 }, '/viewer');
}

async function startTerminal(h) {
  // Terminals open in a compact popup window rather than a full tab.
  openSession(h, { kind: 'terminal' }, '/terminal', 'width=980,height=620,menubar=no,toolbar=no,location=no,status=no,resizable=yes');
}

async function openSession(h, body, page, features) {
  try {
    const res = await fetch('/api/session', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + token(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_id: h.agent_id, ...body }),
    });
    if (!res.ok) { alert('Could not start session: ' + (await res.text())); return; }
    const s = await res.json();
    let url = `${page}?token=${encodeURIComponent(s.token)}&host=${encodeURIComponent(h.hostname || h.agent_id)}&agent=${encodeURIComponent(h.agent_id)}`;
    if (isStandalone()) {
      // Installed app: a new window would come back as a plain browser window
      // with all its chrome, which defeats the point. Navigate in place and let
      // the page go fullscreen — the dashboard is one Back away.
      location.href = url + '&app=1';
      return;
    }
    if (features) {
      window.open(url, 'autormm_' + (body.kind || 'session') + '_' + h.agent_id, features);
    } else {
      window.open(url, '_blank', 'noopener');
    }
  } catch (e) {
    alert('Session error: ' + e);
  }
}

// ---- host detail modal ----
const modal = document.getElementById('modal');
const mTitle = document.getElementById('mTitle');
const mSub = document.getElementById('mSub');
const mCharts = document.getElementById('mCharts');
const mProcs = document.getElementById('mProcs');
const mRemote = document.getElementById('mRemote');
const mTerm = document.getElementById('mTerm');

function hostByID(id) { return lastHosts.find(h => h.agent_id === id); }

function openDetail(agentID) {
  detail.agent = agentID;
  modal.classList.remove('hidden');
  const h = hostByID(agentID);
  mTitle.textContent = h ? (h.hostname || agentID) : agentID;
  mSub.textContent = h ? `${h.platform || h.os} · ${h.arch}` : '';
  renderFacts(h);
  renderPatchPanel(h);
  // Wake replaces nothing — it simply only makes sense for a host that is off
  // and whose MAC was learned while it was on.
  const canWake = !!(h && !h.online && h.facts && h.facts.macs && h.facts.macs.length);
  document.getElementById('mWake').classList.toggle('hidden', !canWake);
  resetInventory();
  resetLogs();
  loadPrefs(agentID);
  loadHistory();
}

// Per-host alert overrides. A blank field inherits the global threshold, which
// is why the placeholders read "global" rather than showing a number the host
// is not actually using.
const prefEls = {
  cpu: document.getElementById('prefCPU'),
  mem: document.getElementById('prefMem'),
  disk: document.getElementById('prefDisk'),
  status: document.getElementById('prefStatus'),
};
let allPrefs = {};

async function loadPrefs(agentID) {
  prefEls.status.textContent = '';
  try {
    allPrefs = await authJSON('/api/hostprefs', 'GET');
  } catch (e) { allPrefs = {}; }
  const p = allPrefs[agentID] || {};
  prefEls.cpu.value = p.cpu || '';
  prefEls.mem.value = p.mem || '';
  prefEls.disk.value = p.disk || '';
  showMuteState(p);
}

function showMuteState(p) {
  if (p && p.mute) { prefEls.status.textContent = 'muted indefinitely'; return; }
  if (p && p.mute_until && new Date(p.mute_until) > new Date()) {
    prefEls.status.textContent = 'muted until ' + new Date(p.mute_until).toLocaleTimeString();
    return;
  }
  prefEls.status.textContent = '';
}

async function savePrefs(extra) {
  const h = hostByID(detail.agent);
  if (!h) return;
  const num = (el) => { const v = parseFloat(el.value); return isFinite(v) && v > 0 ? v : 0; };
  const body = {
    agent_id: h.agent_id,
    pref: {
      cpu: num(prefEls.cpu), mem: num(prefEls.mem), disk: num(prefEls.disk),
      ...(extra && extra.pref),
    },
    ...(extra && extra.mute_hours ? { mute_hours: extra.mute_hours } : {}),
  };
  try {
    const r = await authJSON('/api/hostprefs', 'POST', body);
    allPrefs[h.agent_id] = r.pref || {};
    showMuteState(r.pref);
    if (!extra) prefEls.status.textContent = 'saved';
  } catch (e) { prefEls.status.textContent = 'failed: ' + e; }
}

document.getElementById('prefSave').addEventListener('click', () => savePrefs(null));
document.getElementById('prefMute1').addEventListener('click', () => savePrefs({ mute_hours: 1 }));
document.getElementById('prefMute8').addEventListener('click', () => savePrefs({ mute_hours: 8 }));
document.getElementById('prefUnmute').addEventListener('click', () => savePrefs({ pref: { mute: false, mute_until: null } }));

// Recent errors and warnings from the host's own log. On demand rather than
// polled: this is the question you ask when something already went wrong, and
// shipping every host's log to the hub continuously is a different feature.
const logsOut = document.getElementById('logsOut');
const logsStatus = document.getElementById('logsStatus');
const logsBtn = document.getElementById('logsBtn');

function resetLogs() {
  logsOut.classList.add('hidden');
  logsOut.textContent = '';
  logsStatus.textContent = '';
  logsBtn.disabled = false;
}

logsBtn.addEventListener('click', async () => {
  const h = hostByID(detail.agent);
  if (!h) return;
  logsBtn.disabled = true;
  logsStatus.textContent = 'reading…';
  try {
    const r = await authJSON('/api/eventlog', 'POST', { agent_id: h.agent_id });
    logsOut.textContent = r.output || '';
    logsOut.classList.remove('hidden');
    logsStatus.textContent = r.truncated ? 'truncated' : '';
  } catch (e) {
    logsStatus.textContent = 'failed: ' + e;
  }
  logsBtn.disabled = false;
});

function closeDetail() { detail.agent = null; modal.classList.add('hidden'); }

document.getElementById('mClose').addEventListener('click', closeDetail);
modal.addEventListener('click', e => { if (e.target === modal) closeDetail(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeDetail(); });
document.querySelectorAll('#mRanges button').forEach(b => {
  b.addEventListener('click', () => {
    detail.range = b.dataset.range;
    document.querySelectorAll('#mRanges button').forEach(x => x.classList.toggle('active', x === b));
    loadHistory();
  });
});
mRemote.addEventListener('click', () => { const h = hostByID(detail.agent); if (h) startRemote(h); });
mTerm.addEventListener('click', () => { const h = hostByID(detail.agent); if (h) startTerminal(h); });
document.getElementById('mFiles').addEventListener('click', () => { const h = hostByID(detail.agent); if (h) openFiles(h); });

// Reboot. Confirmed by name, because the host list is a grid of near-identical
// cards and restarting the wrong machine is not undoable.
//
// Distinct from the Reboot in the Patches row, which is offered only on
// platforms autormm knows how to patch and tells whoever is at the keyboard
// that it is finishing updates. Wanting to restart a machine is reason enough
// on its own.
// Wake-on-LAN. The target is powered off, so the hub asks its LAN peers to
// broadcast the magic packet for it (and shouts once itself). "Sent" is all
// WoL can ever confirm — the real signal is the host coming online, which the
// dashboard already shows.
const mWake = document.getElementById('mWake');
mWake.addEventListener('click', async () => {
  const h = hostByID(detail.agent);
  if (!h) return;
  mWake.disabled = true;
  const prev = mWake.textContent;
  mWake.textContent = 'waking…';
  try {
    const r = await authJSON('/api/wol', 'POST', { agent_id: h.agent_id });
    mWake.textContent = r.ok ? (r.relays ? `sent via ${r.relays} peer${r.relays > 1 ? 's' : ''}` : 'sent') : 'failed';
  } catch (e) {
    mWake.textContent = 'failed';
    alert('Wake failed: ' + e);
  }
  setTimeout(() => { mWake.textContent = prev; mWake.disabled = false; }, 5000);
});

const mReboot = document.getElementById('mReboot');
mReboot.addEventListener('click', async () => {
  const h = hostByID(detail.agent);
  if (!h) return;
  const name = h.hostname || h.agent_id;
  if (!confirm(`Reboot ${name}?\n\nAnyone signed in will lose unsaved work. Windows hosts give a 15-second warning; Linux and macOS restart immediately.`)) return;

  const prev = mReboot.textContent;
  mReboot.disabled = true;
  mReboot.textContent = 'rebooting…';
  try {
    const r = await authJSON('/api/reboot', 'POST', { agent_id: h.agent_id });
    // The host reports whether it could actually do it — an unprivileged agent
    // often cannot — so show what it said rather than assuming it worked.
    mReboot.textContent = r.ok ? 'reboot sent' : 'cannot reboot';
    if (!r.ok && r.output) alert(`${name} could not reboot:\n\n${r.output}`);
  } catch (e) {
    mReboot.textContent = 'failed';
    alert(`Reboot request failed: ${e}`);
  }
  setTimeout(() => { mReboot.textContent = prev; mReboot.disabled = false; }, 4000);
});

// ---- file transfer ----
const fileModal = document.getElementById('fileModal');
let fileWS = null, dl = null;
document.getElementById('fileClose').addEventListener('click', closeFiles);
fileModal.addEventListener('click', e => { if (e.target === fileModal) closeFiles(); });

function flog(msg) { const el = document.getElementById('fileLog'); el.textContent += msg + '\n'; }

async function openFiles(h) {
  let s;
  try {
    const res = await fetch('/api/session', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + token(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_id: h.agent_id, kind: 'file' }),
    });
    if (!res.ok) { alert('Could not start file session: ' + (await res.text())); return; }
    s = await res.json();
  } catch (e) { alert('File session error: ' + e); return; }

  document.getElementById('fileHost').textContent = h.hostname || h.agent_id;
  document.getElementById('fileLog').textContent = '';
  fileModal.classList.remove('hidden');

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  fileWS = new WebSocket(`${proto}://${location.host}/client/session?token=${encodeURIComponent(s.token)}`);
  fileWS.binaryType = 'arraybuffer';
  const state = document.getElementById('fileState');
  const send = document.getElementById('fileSend'), get = document.getElementById('fileGet');
  const go = document.getElementById('fbGo'), up = document.getElementById('fbUp');
  fileWS.onopen = () => {
    state.textContent = 'ready'; state.className = 'pill live';
    send.disabled = false; get.disabled = false; go.disabled = false;
    browseTo(''); // open on the host user's home
  };
  fileWS.onclose = () => { state.textContent = 'closed'; state.className = 'pill dead'; send.disabled = true; get.disabled = true; go.disabled = true; up.disabled = true; };
  fileWS.onerror = () => { state.textContent = 'error'; state.className = 'pill dead'; };
  fileWS.onmessage = onFileMsg;
}

// ---- host file browser ----
// Typed paths were the whole interface before, which meant knowing the layout
// of a machine you opened this panel to go look at.
let fbCwd = '';    // resolved directory currently shown
let fbParent = ''; // its parent ("" at the filesystem root)

function browseTo(path) {
  if (!fileWS || fileWS.readyState !== 1) return;
  fileWS.send(JSON.stringify({ t: 'ls', path }));
}

function fmtEntrySize(n) { return n ? fmtBytes(n) : ''; }

function renderListing(m) {
  fbCwd = m.path; fbParent = m.parent || '';
  document.getElementById('fileGetPath').value = m.path;
  document.getElementById('fbUp').disabled = !fbParent;
  document.getElementById('fbNote').textContent = m.msg || '';
  const list = document.getElementById('fbList');
  list.innerHTML = '';
  if (!m.entries || !m.entries.length) {
    list.innerHTML = '<div class="muted fb-empty">empty folder</div>';
    return;
  }
  for (const e of m.entries) {
    const row = document.createElement('div');
    row.className = 'fb-row' + (e.dir ? ' fb-dir' : '');
    row.title = e.dir ? 'Open folder' : 'Download this file';
    const name = document.createElement('span');
    name.className = 'fb-name';
    name.textContent = (e.dir ? '📁 ' : '') + e.name;
    const size = document.createElement('span');
    size.className = 'fb-size';
    size.textContent = e.dir ? '' : fmtEntrySize(e.size);
    row.append(name, size);
    const full = joinHostPath(fbCwd, e.name);
    row.onclick = () => {
      if (e.dir) browseTo(full);
      else { flog(`requesting ${e.name}…`); fileWS.send(JSON.stringify({ t: 'get', path: full })); }
    };
    list.appendChild(row);
  }
}

// joinHostPath joins with the separator the *host* uses, judged from the path
// itself — this browser runs against Windows and Unix hosts alike, and the
// viewer's own platform is irrelevant.
function joinHostPath(dir, name) {
  const sep = dir.includes('\\') && !dir.includes('/') ? '\\' : '/';
  return dir.endsWith(sep) ? dir + name : dir + sep + name;
}

document.getElementById('fbGo').addEventListener('click', () => browseTo(document.getElementById('fileGetPath').value.trim()));
document.getElementById('fbUp').addEventListener('click', () => { if (fbParent) browseTo(fbParent); });
document.getElementById('fileGetPath').addEventListener('keydown', e => {
  if (e.key === 'Enter') browseTo(e.target.value.trim());
});

function closeFiles() {
  if (fileWS) { try { fileWS.close(); } catch (_) {} fileWS = null; }
  dl = null;
  fileModal.classList.add('hidden');
}

function onFileMsg(ev) {
  if (typeof ev.data === 'string') {
    const m = JSON.parse(ev.data);
    if (m.t === 'ok') { flog(`✓ uploaded → ${m.path} (${fmtBytes(m.size)})`); browseTo(fbCwd); }
    else if (m.t === 'err') flog(`✗ ${m.msg}`);
    else if (m.t === 'meta') { dl = { name: m.name, size: m.size, parts: [], got: 0 }; flog(`downloading ${m.name} (${fmtBytes(m.size)})…`); }
    else if (m.t === 'done') finishDownload();
    else if (m.t === 'list') renderListing(m);
    return;
  }
  if (dl) { dl.parts.push(ev.data); dl.got += ev.data.byteLength; }
}

function finishDownload() {
  if (!dl) return;
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob(dl.parts));
  a.download = dl.name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
  flog(`✓ downloaded ${dl.name}`);
  dl = null;
}

document.getElementById('fileSend').addEventListener('click', async () => {
  const inp = document.getElementById('fileUpload');
  const files = inp.files ? [...inp.files] : [];
  if (!files.length || !fileWS || fileWS.readyState !== 1) return;
  // Sequential on purpose: the frames of two interleaved uploads would corrupt
  // both, since the agent reads "put" then counts bytes.
  for (const f of files) {
    fileWS.send(JSON.stringify({ t: 'put', name: f.name, size: f.size, dir: fbCwd }));
    flog(`uploading ${f.name} (${fmtBytes(f.size)}) → ${fbCwd || 'autormm-incoming'}…`);
    const chunk = 256 * 1024;
    for (let off = 0; off < f.size; off += chunk) {
      // simple backpressure so we don't buffer the whole file in memory
      while (fileWS.bufferedAmount > 8 * 1024 * 1024) await new Promise(r => setTimeout(r, 20));
      fileWS.send(await f.slice(off, off + chunk).arrayBuffer());
    }
  }
  inp.value = '';
});

document.getElementById('fileGet').addEventListener('click', () => {
  const path = document.getElementById('fileGetPath').value.trim();
  if (!path || !fileWS || fileWS.readyState !== 1) return;
  fileWS.send(JSON.stringify({ t: 'get', path }));
});

// Update current values / process list from the periodic poll without refetching history.
function refreshDetailLive() {
  const h = hostByID(detail.agent);
  if (!h) return;
  mRemote.disabled = !(h.online && h.can_stream);
  mTerm.disabled = !(h.online && h.can_exec);
  renderProcs(h);
}

async function loadHistory() {
  const agent = detail.agent;
  try {
    const res = await fetch(`/api/history?agent=${encodeURIComponent(agent)}&range=${detail.range}`, {
      headers: { Authorization: 'Bearer ' + token() },
    });
    const data = await res.json();
    if (detail.agent !== agent) return; // switched/closed while loading
    renderCharts(data.enabled, data.points || []);
  } catch (e) {
    mCharts.innerHTML = `<div class="no-data">Could not load history: ${e}</div>`;
  }
  refreshDetailLive();
}

function renderCharts(enabled, pts) {
  if (!enabled) {
    mCharts.innerHTML = `<div class="no-data">History is disabled. Start the server with <code>--db /path/autormm.db</code> to record time-series.</div>`;
    return;
  }
  if (!pts.length) {
    mCharts.innerHTML = `<div class="no-data">No samples in this range yet.</div>`;
    return;
  }
  const cpu = pts.map(p => ({ ts: p.ts, v: p.cpu }));
  const mem = pts.map(p => ({ ts: p.ts, v: p.mem }));
  const disk = pts.map(p => ({ ts: p.ts, v: p.disk_max }));
  const recv = pts.map(p => ({ ts: p.ts, v: p.net_recv }));
  const sent = pts.map(p => ({ ts: p.ts, v: p.net_sent }));
  const netMax = Math.max(1, ...recv.map(p => p.v), ...sent.map(p => p.v));
  mCharts.innerHTML = [
    chart('CPU', [{ color: '#4aa8ff', data: cpu }], 100, v => v.toFixed(0) + '%'),
    chart('Memory', [{ color: '#3fb950', data: mem }], 100, v => v.toFixed(0) + '%'),
    chart('Disk (busiest)', [{ color: '#d29922', data: disk }], 100, v => v.toFixed(0) + '%'),
    chart('Network', [
      { color: '#4aa8ff', data: recv, label: '↓ recv' },
      { color: '#f778ba', data: sent, label: '↑ sent' },
    ], netMax, v => fmtBytes(v) + '/s'),
  ].join('');
}

// chart returns an SVG chart card as an HTML string.
function chart(title, series, max, fmt) {
  const W = 300, H = 90, pad = 3;
  let tmin = Infinity, tmax = -Infinity;
  for (const s of series) for (const p of s.data) { tmin = Math.min(tmin, p.ts); tmax = Math.max(tmax, p.ts); }
  const xspan = Math.max(1, tmax - tmin);
  const xf = t => pad + ((t - tmin) / xspan) * (W - 2 * pad);
  const yf = v => H - pad - (Math.max(0, Math.min(max, v)) / max) * (H - 2 * pad);

  let body = '';
  series.forEach((s, i) => {
    if (!s.data.length) return;
    const pts = s.data.map(p => `${xf(p.ts).toFixed(1)},${yf(p.v).toFixed(1)}`).join(' ');
    if (i === 0) {
      const first = xf(s.data[0].ts).toFixed(1), last = xf(s.data[s.data.length - 1].ts).toFixed(1);
      body += `<path d="M${first},${H - pad} L ${pts} L ${last},${H - pad} Z" fill="${s.color}" opacity="0.12"/>`;
    }
    body += `<polyline points="${pts}" fill="none" stroke="${s.color}" stroke-width="1.5" vector-effect="non-scaling-stroke"/>`;
  });

  const cur = series[0].data.length ? fmt(series[0].data[series[0].data.length - 1].v) : '—';
  const legend = series.length > 1
    ? `<span class="legend">${series.map(s => `<span><i style="background:${s.color}"></i>${s.label || ''}</span>`).join('')}</span>`
    : `<span class="cur">${cur}</span>`;
  return `<div class="chart">
    <div class="chart-head"><span>${title}</span>${legend}</div>
    <svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">${body}</svg>
  </div>`;
}

function renderProcs(h) {
  const procs = h.metrics && h.metrics.procs ? h.metrics.procs : [];
  if (!procs.length) { mProcs.innerHTML = ''; return; }
  const rows = procs.map(p =>
    `<tr><td>${p.pid}</td><td>${escapeHtml(p.name)}</td><td>${p.cpu.toFixed(1)}%</td><td>${fmtBytes(p.mem_rss)}</td>` +
    `<td class="proc-actions">` +
    `<button class="btn ghost proc-restart" data-pid="${p.pid}" data-name="${escapeHtml(p.name)}" title="Restart process">⟳</button>` +
    `<button class="btn ghost proc-kill" data-pid="${p.pid}" data-name="${escapeHtml(p.name)}" title="Kill process">✕</button></td></tr>`
  ).join('');
  mProcs.innerHTML = `<table class="proc-table">
    <thead><tr><th>PID</th><th>Process</th><th>CPU</th><th>Memory</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table>`;
  mProcs.querySelectorAll('.proc-kill').forEach(b => b.onclick = () => killProc(b.dataset.pid, b.dataset.name));
  mProcs.querySelectorAll('.proc-restart').forEach(b => b.onclick = () => restartProc(b.dataset.pid, b.dataset.name));
}

// ---- process / service actions (#20) ----
async function hostAction(body, label) {
  const h = hostByID(detail.agent);
  if (!h) return;
  try {
    const res = await fetch('/api/action', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + token(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_id: h.agent_id, ...body }),
    });
    const txt = await res.text();
    if (!res.ok) { alert(`${label}: ${txt}`); return; }
    const r = JSON.parse(txt);
    if (!r.ok) { alert(`${label} failed (exit ${r.exit_code})\n${r.output || r.err || ''}`); }
    // on success the change shows up on the next metrics poll
  } catch (e) { alert('Action error: ' + e); }
}

function killProc(pid, name) {
  if (!confirm(`Force-kill "${name}" (PID ${pid}) on this host?`)) return;
  hostAction({ kind: 'proc', action: 'force', pid: parseInt(pid, 10) }, `kill ${name}`);
}

function restartProc(pid, name) {
  if (!confirm(`Restart "${name}" (PID ${pid})? It's stopped and relaunched with the same command line.`)) return;
  hostAction({ kind: 'proc', action: 'restart', pid: parseInt(pid, 10) }, `restart ${name}`);
}

document.querySelectorAll('#mServices button[data-svc]').forEach(b => b.addEventListener('click', () => {
  const name = document.getElementById('svcName').value.trim();
  if (!name) return;
  hostAction({ kind: 'service', action: b.dataset.svc, service: name }, `${b.dataset.svc} ${name}`);
}));

// ---- patches (#55) ----
function renderPatchPanel(h) {
  const os = h && h.os;
  const supported = os === 'linux' || os === 'windows';
  // Windows Update needs SYSTEM, so installing requires the elevated helper.
  const canInstall = supported && (os !== 'windows' || !!h.elevated);
  document.getElementById('patchCheck').disabled = !supported;
  document.getElementById('patchInstall').disabled = !canInstall;
  document.getElementById('patchReboot').disabled = !canInstall;
  let note = '';
  if (!supported) note = os ? `patching is not supported on ${os} hosts` : '';
  else if (!canInstall) note = 'install the elevated helper to apply updates (Add host → Windows elevated helper)';
  document.getElementById('patchStatus').textContent = note;
  document.getElementById('patchOut').textContent = '';
}
document.getElementById('patchCheck').addEventListener('click', async () => {
  const h = hostByID(detail.agent); if (!h) return;
  const st = document.getElementById('patchStatus'); st.textContent = 'checking…';
  try {
    const r = await authFetch('/api/patch/status?agent_id=' + encodeURIComponent(h.agent_id));
    if (r.status === 401) { showLogin(); return; }
    const d = await r.json();
    if (!d.supported) { st.textContent = d.note || 'not supported'; return; }
    if (d.error) { st.textContent = 'check failed: ' + d.error; return; }
    st.textContent = `${d.updates} update${d.updates === 1 ? '' : 's'} available`
      + (d.security ? `, ${d.security} security` : '')
      + (d.reboot_required ? ' · reboot required' : '')
      + (d.note ? ` · ${d.note}` : '');
  } catch (e) { st.textContent = 'check error: ' + e; }
});
document.getElementById('patchInstall').addEventListener('click', async () => {
  const h = hostByID(detail.agent); if (!h) return;
  const slow = h.os === 'windows'; // Windows Update routinely runs for tens of minutes
  if (!confirm(`Install all available updates on ${h.hostname || h.agent_id}?`
    + (slow ? ' Windows Update can take 30+ minutes — leave this tab open.' : ' This can take several minutes.'))) return;
  const st = document.getElementById('patchStatus'), out = document.getElementById('patchOut');
  st.textContent = slow ? 'installing… Windows Update can take 30+ minutes' : 'installing… (may take several minutes)';
  out.textContent = '';
  try {
    const r = await authFetch('/api/patch/install', 'POST', { agent_id: h.agent_id });
    if (!r.ok) { // errors come back as plain text (http.Error), not JSON
      st.textContent = 'install failed: ' + ((await r.text().catch(() => '')).trim() || r.status);
      return;
    }
    const d = await r.json().catch(() => ({}));
    st.textContent = d.ok ? 'done' : `finished (exit ${d.exit_code})`;
    out.textContent = d.output || '';
  } catch (e) { st.textContent = 'install error: ' + e; }
});
document.getElementById('patchReboot').addEventListener('click', async () => {
  const h = hostByID(detail.agent); if (!h) return;
  if (!confirm(`Reboot ${h.hostname || h.agent_id} now?`)) return;
  try {
    const r = await authFetch('/api/patch/reboot', 'POST', { agent_id: h.agent_id });
    document.getElementById('patchStatus').textContent = r.ok ? 'reboot sent' : 'reboot failed';
  } catch (e) { document.getElementById('patchStatus').textContent = 'reboot error: ' + e; }
});

const mFacts = document.getElementById('mFacts');
function renderFacts(h) {
  const f = (h && h.facts) || {};
  const items = [];
  if (f.ips && f.ips.length) items.push(['IP', f.ips.join(', ')]);
  if (f.macs && f.macs.length) items.push(['MAC', f.macs.join(', ')]);
  if (f.cpu_model) items.push(['CPU', f.cpu_model + (f.cpu_cores ? ` · ${f.cpu_cores} cores` : '')]);
  if (f.mem_total) items.push(['RAM', fmtBytes(f.mem_total)]);
  items.push(['OS', h ? (h.platform || h.os || '') : '']);
  if (f.kernel_version) items.push(['Kernel', f.kernel_version]);
  if (f.virtualization) items.push(['Virtualization', f.virtualization]);
  items.push(['Agent', (h && h.agent_version) || '—']);
  // Drive health, for hosts whose agent can read S.M.A.R.T. (needs smartctl
  // and enough privilege — the root/system agents on storage boxes, typically).
  const drives = (h && h.metrics && h.metrics.smart) || [];
  for (const d of drives) {
    const healthy = d.passed && !d.reallocated && !d.pending && !d.uncorrectable && !d.media_errors && !d.critical_warn;
    let v = healthy ? 'healthy ✓' : '⚠ FAILING';
    if (!healthy) {
      const why = [];
      if (!d.passed) why.push('self-test failed');
      if (d.reallocated) why.push(`${d.reallocated} reallocated`);
      if (d.pending) why.push(`${d.pending} pending`);
      if (d.uncorrectable) why.push(`${d.uncorrectable} uncorrectable`);
      if (d.media_errors) why.push(`${d.media_errors} media errors`);
      if (d.critical_warn) why.push('critical warning');
      v += ' — ' + why.join(', ');
    }
    const extra = [];
    if (d.temp_c) extra.push(`${d.temp_c}°C`);
    if (d.percent_used) extra.push(`${d.percent_used}% worn`);
    if (d.power_on_hours) extra.push(`${Math.round(d.power_on_hours / 24 / 365 * 10) / 10}y on`);
    if (extra.length) v += ` (${extra.join(', ')})`;
    items.push([d.model || d.device, v]);
  }
  if (h && h.metrics && h.metrics.reboot_pending) {
    const why = h.metrics.reboot_reason;
    items.push(['Restart', why ? `pending — ${why}` : 'pending']);
  }
  if (h && h.elevated) items.push(['Admin helper', 'installed ✓']);
  if (h && h.console) items.push(['Lock screen', 'capturable ✓']);
  mFacts.innerHTML = items
    .map(([k, v]) => `<div class="fact"><span class="fk">${k}</span><span class="fv">${escapeHtml(v)}</span></div>`)
    .join('');
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
}

// ---- software inventory panel ----
const mInvBtn = document.getElementById('mInvBtn');
const mInvFilter = document.getElementById('mInvFilter');
const mInvList = document.getElementById('mInvList');
const mInvCount = document.getElementById('mInvCount');
let invPackages = null;

function resetInventory() {
  invPackages = null;
  mInvList.innerHTML = '';
  mInvCount.textContent = '';
  mInvFilter.value = '';
  mInvFilter.classList.add('hidden');
  mInvBtn.classList.remove('hidden');
  mInvBtn.disabled = false;
  mInvBtn.textContent = 'Load software inventory';
}

mInvBtn.addEventListener('click', loadInventory);
mInvFilter.addEventListener('input', renderInventory);

async function loadInventory() {
  const agent = detail.agent;
  mInvBtn.disabled = true;
  mInvBtn.textContent = 'Loading…';
  try {
    const res = await fetch(`/api/inventory?agent=${encodeURIComponent(agent)}`, {
      headers: { Authorization: 'Bearer ' + token() },
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    if (detail.agent !== agent) return;
    if (data.error) throw new Error(data.error);
    invPackages = data.packages || [];
    mInvBtn.classList.add('hidden');
    mInvFilter.classList.remove('hidden');
    mInvCount.textContent = `${data.count} packages (${data.source})`;
    renderInventory();
  } catch (e) {
    mInvBtn.disabled = false;
    mInvBtn.textContent = 'Retry';
    mInvCount.textContent = 'error: ' + (e.message || e);
  }
}

function renderInventory() {
  if (!invPackages) return;
  const needle = mInvFilter.value.toLowerCase();
  const rows = invPackages
    .filter(p => !needle || p.name.toLowerCase().includes(needle))
    .slice(0, 1000)
    .map(p => `<div><span>${escapeHtml(p.name)}</span><span>${escapeHtml(p.version)}</span></div>`)
    .join('');
  mInvList.innerHTML = rows || '<div class="no-data">no matches</div>';
}

setInterval(() => { if (detail.agent) loadHistory(); }, 15000);

// ---- add host (enroll) modal ----
const enrollModal = document.getElementById('enrollModal');
document.getElementById('enrollBtn').addEventListener('click', openEnroll);
document.getElementById('enrollClose').addEventListener('click', () => enrollModal.classList.add('hidden'));
enrollModal.addEventListener('click', e => { if (e.target === enrollModal) enrollModal.classList.add('hidden'); });
document.querySelectorAll('#enrollBody .copy').forEach(b => b.addEventListener('click', () => {
  const txt = document.getElementById(b.dataset.t).textContent;
  navigator.clipboard.writeText(txt).then(() => {
    const o = b.textContent; b.textContent = 'Copied'; setTimeout(() => (b.textContent = o), 1200);
  });
}));

async function openEnroll() {
  enrollModal.classList.remove('hidden');
  const note = document.getElementById('enrollNote');
  note.textContent = '';
  try {
    const res = await fetch('/api/enroll', { headers: { Authorization: 'Bearer ' + token() } });
    if (!res.ok) throw new Error(await res.text());
    const d = await res.json();
    document.getElementById('cmdLinux').textContent = d.commands.linux;
    document.getElementById('cmdLinuxDesktop').textContent = d.commands.linux_desktop;
    document.getElementById('cmdWindows').textContent = d.commands.windows;
    document.getElementById('cmdWindowsElevated').textContent = d.commands.windows_elevated;
    document.getElementById('cmdMac').textContent = d.commands.macos;
    if (!d.bundled) {
      note.textContent = 'Note: this hub build does not bundle agent binaries — rebuild with `make` so hosts can download the agent from the hub.';
    }
  } catch (e) {
    note.textContent = 'Error: ' + e.message;
  }
}

// Bridge for scripts.js (shares auth + the current host list).
window.autormm = {
  token,
  execHosts: () => lastHosts.filter(h => h.online && h.can_exec),
  allHosts: () => lastHosts,
};

poll();
setInterval(poll, 3000);

// ---- agentless network devices ----
// Half a homelab is not a computer: switches, printers, appliances, the
// zero-trust connector. They cannot run an agent, so the hub probes them
// directly and shows them beside the hosts they sit alongside.
const netSection = document.getElementById('netSection');
const netGrid = document.getElementById('netGrid');
const netModal = document.getElementById('netModal');

async function loadNetChecks() {
  let list = [];
  try {
    list = await authJSON('/api/netchecks', 'GET');
  } catch (e) { return; } // unauthenticated or unsupported: leave the section alone
  list = list || [];
  // Hidden entirely when nothing is configured, so a fleet that does not use
  // this never sees an empty shelf.
  netSection.classList.toggle('hidden', list.length === 0);
  document.getElementById('netSummary').textContent =
    list.length ? `${list.filter(c => c.up).length}/${list.length} reachable` : '';

  netGrid.innerHTML = '';
  for (const c of list) {
    const el = document.createElement('div');
    el.className = 'netcard';
    const sub = c.up
      ? `${c.address}${c.port ? ':' + c.port : ''} · ${Math.round(c.latency_ms)}ms`
      : `${c.address}${c.port ? ':' + c.port : ''} · unreachable`;
    el.innerHTML =
      `<span class="status ${c.checked ? (c.up ? 'online' : 'offline') : ''}"></span>` +
      `<div class="nc-body"><div class="nc-name"></div><div class="nc-sub"></div></div>` +
      `<button class="nc-del" title="Stop monitoring">✕</button>`;
    el.querySelector('.nc-name').textContent = c.name || c.address;
    el.querySelector('.nc-sub').textContent = c.checked ? sub : 'not checked yet';
    el.querySelector('.nc-del').onclick = async () => {
      if (!confirm(`Stop monitoring ${c.name || c.address}?`)) return;
      try {
        await authJSON('/api/netchecks?id=' + encodeURIComponent(c.id), 'DELETE');
        loadNetChecks();
      } catch (e) { alert('Could not remove: ' + e.message); }
    };
    netGrid.appendChild(el);
  }
}

document.getElementById('netAdd').addEventListener('click', () => netModal.classList.remove('hidden'));
document.getElementById('netClose').addEventListener('click', () => netModal.classList.add('hidden'));
netModal.addEventListener('click', e => { if (e.target === netModal) netModal.classList.add('hidden'); });

document.getElementById('netSave').addEventListener('click', async () => {
  const addr = document.getElementById('netAddr').value.trim();
  const st = document.getElementById('netStatus');
  if (!addr) { st.textContent = 'an address is required'; return; }
  const portRaw = document.getElementById('netPort').value.trim();
  st.textContent = 'saving…';
  try {
    await authJSON('/api/netchecks', 'POST', {
      name: document.getElementById('netName').value.trim(),
      address: addr,
      port: portRaw ? parseInt(portRaw, 10) : 0,
      tags: document.getElementById('netTags').value.trim(),
    });
    for (const id of ['netName', 'netAddr', 'netPort', 'netTags']) document.getElementById(id).value = '';
    st.textContent = '';
    netModal.classList.add('hidden');
    loadNetChecks();
  } catch (e) { st.textContent = 'failed: ' + e.message; }
});
