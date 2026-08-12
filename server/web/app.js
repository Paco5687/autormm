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
// The login fields live in real forms so the browser does not go looking
// elsewhere on the page for a username to pair with the password. Nothing is
// actually posted — submission is handled by fetch — so the default is stopped.
for (const id of ['loginSetup', 'loginPw']) {
  document.getElementById(id).addEventListener('submit', e => e.preventDefault());
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

const cards = new Map();
let hostQuery = '';
// Which cards the operator has expanded, by agent id. Kept here rather than in
// the DOM because the card's contents are re-rendered on every poll.
const expandedCards = new Set(); // agentID -> element
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
  let hosts = tagFilter
    ? allHosts.filter(h => hostTags(h).some(t => t.toLowerCase() === tagFilter))
    : allHosts;
  if (hostQuery) {
    hosts = hosts.filter(h =>
      `${h.hostname || ''} ${h.platform || h.os || ''} ${hostTags(h).join(' ')}`
        .toLowerCase().includes(hostQuery));
  }
  // Totals describe the whole fleet, not the current filter — otherwise the
  // strip would quietly stop counting an offline host the moment you searched.
  renderFleet(allHosts);
  // "No hosts have connected yet" is wrong and alarming when the real answer is
  // "your filter matches nothing" — which is exactly what a stray autofill in
  // the search box produces.
  const filtered = !!(hostQuery || tagFilter);
  emptyEl.classList.toggle('hidden', hosts.length > 0);
  document.getElementById('emptyMsg').textContent = filtered
    ? 'No hosts match this filter.'
    : 'No hosts have connected yet.';
  document.getElementById('emptyHint').textContent = filtered
    ? [hostQuery && `search "${hostQuery}"`, tagFilter && `tag "${tagFilter}"`]
        .filter(Boolean).join(' · ')
    : 'Install the agent on a host and point it at this server.';
  document.getElementById('emptyClear').classList.toggle('hidden', !filtered);
  const online = hosts.filter(h => h.online).length;
  summaryEl.textContent = `${online}/${hosts.length} online`
    + (tagFilter ? ` · ${tagFilter}` : '') + (hostQuery ? ` · "${hostQuery}"` : '');

  // Ordered by CSS `order` rather than by moving nodes: the cards are reused
  // across polls, and re-appending them would restart every bar transition a
  // few times a minute. Problems sort to the front.
  const rank = h => (!h.online ? 0 : (h.alerts || []).length ? 1 : 2);
  const ordered = [...hosts].sort((a, b) =>
    rank(a) - rank(b) || String(a.hostname || '').localeCompare(String(b.hostname || '')));

  const seen = new Set();
  for (const [i, h] of ordered.entries()) {
    seen.add(h.agent_id);
    let el = cards.get(h.agent_id);
    if (!el) {
      el = document.getElementById('cardTpl').content.firstElementChild.cloneNode(true);
      cards.set(h.agent_id, el);
      grid.appendChild(el);
    }
    el.style.order = i;
    updateCard(el, h);
  }
  for (const [id, el] of cards) {
    if (!seen.has(id)) { el.remove(); cards.delete(id); }
  }
  layoutGrid();
  if (detail.agent) refreshDetailLive();
}

// Simple monochrome marks rather than an icon font or emoji: emoji render as a
// tofu box wherever the platform lacks the glyph, and a webfont is another
// asset this hub would have to serve.
const OS_MARKS = {
  windows: '<svg viewBox="0 0 16 16"><path d="M0 2.4l6.4-.9v6.1H0zM7.2 1.4L16 .2v7.4H7.2zM0 8.4h6.4v6.1L0 13.6zM7.2 8.4H16v7.4l-8.8-1.2z"/></svg>',
  darwin: '<svg viewBox="0 0 16 16"><path d="M10.9 8.5c0-1.7 1.4-2.5 1.4-2.6-.8-1.1-2-1.3-2.4-1.3-1-.1-2 .6-2.5.6s-1.3-.6-2.1-.6c-1.1 0-2.1.6-2.7 1.6-1.1 2-.3 4.9.8 6.5.5.8 1.2 1.7 2 1.6.8 0 1.1-.5 2-.5s1.2.5 2 .5 1.4-.8 1.9-1.6c.6-.9.8-1.8.8-1.8s-1.6-.6-1.6-2.4zM9.3 3.3c.4-.5.7-1.3.6-2-.6 0-1.4.4-1.9 1-.4.5-.7 1.3-.6 2 .7.1 1.4-.4 1.9-1z"/></svg>',
  linux: '<svg viewBox="0 0 16 16"><path d="M8 .8c-1.9 0-3 1.5-3 3.3 0 1.1.1 1.7-.3 2.5-.5.9-1.6 2-2 3.3-.3.9-.2 1.7.3 2 .4.3.9.1 1.3.4.4.3.5.8 1 1.1.9.6 3.1.6 4 0 .5-.3.6-.8 1-1.1.4-.3.9-.1 1.3-.4.5-.3.6-1.1.3-2-.4-1.3-1.5-2.4-2-3.3-.4-.8-.3-1.4-.3-2.5 0-1.8-1.1-3.3-3-3.3zm-1 2.4c.3 0 .5.3.5.7s-.2.7-.5.7-.5-.3-.5-.7.2-.7.5-.7zm2 0c.3 0 .5.3.5.7s-.2.7-.5.7-.5-.3-.5-.7.2-.7.5-.7zM8 5.4c.7 0 1.7.4 1.7.9 0 .4-1 1-1.7 1s-1.7-.6-1.7-1c0-.5 1-.9 1.7-.9z"/></svg>',
};
function osMark(os) { return OS_MARKS[os] || ''; }

// Relative time, because "last seen 8/11/2026, 6:37:44 PM" makes the reader do
// the arithmetic that matters — how long has this been down.
function fmtAgo(ts) {
  const secs = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
  if (secs < 90) return `${Math.round(secs)}s ago`;
  if (secs < 5400) return `${Math.round(secs / 60)}m ago`;
  if (secs < 172800) return `${Math.round(secs / 3600)}h ago`;
  return `${Math.round(secs / 86400)}d ago`;
}

// ---- fleet totals ----
//
// Derived from the same poll that feeds the cards, so the strip can never
// disagree with what is displayed under it.
const fleetEl = document.getElementById('fleet');
let fleetHist = [];
let fleetSampled = 0;

function renderFleet(hosts) {
  fleetEl.classList.toggle('hidden', hosts.length === 0);
  if (!hosts.length) return;
  const live = hosts.filter(h => h.online && h.metrics);
  const online = hosts.filter(h => h.online).length;

  setText('fHosts', String(online));
  setText('fHostsSub', ` / ${hosts.length}`);
  const down = hosts.length - online;
  setText('fHostsFoot', down ? `${down} offline` : 'all reporting');
  document.getElementById('tileHosts').classList.toggle('t-warn', down > 0);

  // A mean across hosts, which is the figure that answers "is the fleet busy".
  const cpu = live.length ? live.reduce((a, h) => a + h.metrics.cpu_percent, 0) / live.length : 0;
  setText('fCPU', cpu.toFixed(0));
  // Seeded from the per-host histories the hosts already send, so the trace is
  // there on the first paint. Accumulating only from live polls left the tile
  // showing a flat line along the bottom for the first minute after a reload,
  // which reads as "the fleet is idle" rather than "no data yet".
  if (!fleetHist.length) fleetHist = averageHistories(live);
  // Sampled on the clock, not per render: render() also runs on every keystroke
  // in the search box, which would otherwise stuff the trace with duplicates of
  // whatever the fleet happened to be doing while someone was typing.
  const now = Date.now();
  if (now - fleetSampled >= 2500) {
    fleetSampled = now;
    fleetHist.push(cpu);
    if (fleetHist.length > 60) fleetHist.shift();
  }
  sparkline(document.querySelector('.fleetLine'), fleetHist,
    document.querySelector('.fleetFill'), 26);

  const used = live.reduce((a, h) => a + (h.metrics.mem_used || 0), 0);
  const total = live.reduce((a, h) => a + (h.metrics.mem_total || 0), 0);
  setText('fMem', total ? fmtBytes(used) : '—');
  setText('fMemTotal', total ? ` / ${fmtBytes(total)}` : '');
  const memPct = total ? (used / total) * 100 : 0;
  const memBar = document.getElementById('fMemBar');
  memBar.style.width = memPct.toFixed(1) + '%';
  memBar.classList.toggle('hot', memPct >= 85);

  const rx = live.reduce((a, h) => a + (h.metrics.net_recv || 0), 0);
  const tx = live.reduce((a, h) => a + (h.metrics.net_sent || 0), 0);
  setText('fNet', `↓${fmtBytes(rx)}  ↑${fmtBytes(tx)}`);
  setText('fNetFoot', 'per second, all hosts');

  const alerting = hosts.reduce((a, h) => a + (h.alerts || []).length, 0);
  setText('fAlerts', String(alerting));
  setText('fAlertsFoot', alerting ? 'conditions firing' : 'nothing firing');
  document.getElementById('tileAlerts').classList.toggle('t-bad', alerting > 0);
}

// Mean CPU across hosts at each point in time. Histories can differ in length,
// so each index averages only the hosts that actually reach back that far.
function averageHistories(hosts) {
  const hs = hosts.map(h => h.cpu_history || []).filter(a => a.length);
  if (!hs.length) return [];
  const n = Math.min(60, Math.max(...hs.map(a => a.length)));
  const out = [];
  for (let i = 0; i < n; i++) {
    let sum = 0, count = 0;
    for (const a of hs) {
      // Right-align: the newest sample of every host is its last one.
      const v = a[a.length - n + i];
      if (v !== undefined) { sum += v; count++; }
    }
    if (count) out.push(sum / count);
  }
  return out;
}

function setText(id, v) { const el = document.getElementById(id); if (el) el.textContent = v; }

// Chooses a column count that fills its rows evenly.
//
// A plain auto-fill grid packs as many cards per row as will fit and leaves
// whatever is left over on the last one — eight hosts in a five-wide grid gives
// five and then three beside a void. Spreading the same cards over the same
// number of rows uses the width instead of wasting it: eight become four and
// four, six become three and three.
function layoutGrid() {
  const n = grid.childElementCount;
  if (!n) { grid.style.gridTemplateColumns = ''; return; }
  const width = grid.clientWidth;
  if (!width) return; // not laid out yet; the resize observer will call back
  const maxCols = Math.max(1, Math.floor((width + GRID_GAP) / (CARD_MIN + GRID_GAP)));
  const rows = Math.ceil(n / maxCols);
  const cols = Math.min(maxCols, Math.ceil(n / rows));
  grid.style.gridTemplateColumns = `repeat(${cols}, minmax(0, 1fr))`;
}

// Narrower than the cards look, because the host column is now the smaller
// share of the row: at 300 a 1400px screen dropped to two columns and gave
// each card width it had no use for.
const CARD_MIN = 280;  // matches the fallback in the stylesheet
const GRID_GAP = 14;

// Re-balances when the window changes, which a CSS-only grid would do for free
// and this one has to be told about.
if (window.ResizeObserver) new ResizeObserver(layoutGrid).observe(grid);
else window.addEventListener('resize', layoutGrid);

function updateCard(el, h) {
  const status = el.querySelector('.status');
  status.className = 'status ' + (h.online ? 'online' : 'offline');
  el.querySelector('.nametext').textContent = h.hostname || h.agent_id;
  el.querySelector('.platform').textContent = `${h.platform || h.os} · ${h.arch}`;
  const osic = el.querySelector('.osic');
  if (osic.dataset.os !== h.os) { osic.dataset.os = h.os; osic.innerHTML = osMark(h.os); }

  // The card itself carries the state, so a host in trouble is findable by
  // colour from across the room rather than by reading every chip.
  el.classList.toggle('is-offline', !h.online);
  el.classList.toggle('is-warn', !!(h.online && (h.alerts || []).length));

  const alerts = el.querySelector('.alerts');
  alerts.innerHTML = '';
  for (const a of (h.alerts || [])) {
    const c = document.createElement('span');
    c.className = 'chip' + (/offline|full|high|failing/.test(a) ? ' bad' : '');
    c.textContent = a;
    alerts.appendChild(c);
  }

  // A host with no telemetry gets no bars at all: empty rails and a flat
  // sparkline look like real readings of zero.
  const m = h.metrics;
  el.classList.toggle('nodata', !m);
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

  sparkline(el.querySelector('.cpuSpark'), h.cpu_history || [], el.querySelector('.cpuSparkFill'));

  const det = el.querySelector('.details');
  if (m) {
    const rows = [
      ['up', `${fmtUptime(m.uptime_secs)}  ·  load ${m.load1.toFixed(2)}`],
      ['mem', `${fmtBytes(m.mem_used)} / ${fmtBytes(m.mem_total)}`],
      ['net', `↓${fmtBytes(m.net_recv)}/s  ↑${fmtBytes(m.net_sent)}/s`],
    ];
    for (const g of (m.gpus || [])) rows.push(['gpu', `${fmtBytes(g.mem_used)} / ${fmtBytes(g.mem_total)}`]);
    // Built as a definition list rather than one newline-joined string: the
    // figures line up in a column, and each carries its full text as a tooltip
    // for when the card is too narrow to show all of it. Mount points and GPU
    // names are host-reported, so they are escaped rather than trusted.
    // Disks get a bar each rather than a run of percentages: "C: 71%  D: 22%"
    // makes the reader parse a string to find the one that is nearly full.
    const disks = (m.disks || []).map(d =>
      `<div class="dk" title="${escapeHtml(d.mount)} — ${d.percent.toFixed(0)}% used">` +
        `<span class="dk-m">${escapeHtml(d.mount)}</span>` +
        `<span class="dk-bar"><i class="${d.percent >= 85 ? 'hot' : ''}" style="width:${d.percent.toFixed(0)}%"></i></span>` +
        `<span class="dk-v">${d.percent.toFixed(0)}%</span>` +
      `</div>`).join('');
    // Rebuilt on every poll, so the open state has to live outside the markup —
    // otherwise an expanded card would snap shut every three seconds.
    const open = expandedCards.has(h.agent_id) ? ' open' : '';
    det.innerHTML = `<details class="more"${open}><summary>Details</summary>` +
      '<dl class="dl">' + rows.map(([k, v]) =>
        `<dt>${k}</dt><dd title="${escapeHtml(v)}">${escapeHtml(v)}</dd>`).join('') + '</dl>' +
      (disks ? `<div class="disks">${disks}</div>` : '') +
      '</details>';
    const more = det.querySelector('.more');
    more.addEventListener('toggle', () => {
      if (more.open) expandedCards.add(h.agent_id);
      else expandedCards.delete(h.agent_id);
    });
    // Expanding a card is not opening it: without this the disclosure also
    // launches the host detail panel.
    more.querySelector('summary').addEventListener('click', e => e.stopPropagation());
  } else {
    det.innerHTML = '<div class="waiting"></div>';
    det.firstChild.textContent = h.online
      ? 'waiting for telemetry…'
      : `last seen ${fmtAgo(h.last_seen)}`;
    if (!h.online) det.firstChild.title = new Date(h.last_seen).toLocaleString();
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

// Draws the line and, when given a path element, the area beneath it. The fill
// is what makes a 24px-tall trace legible at a glance — a hairline alone reads
// as decoration.
function sparkline(poly, data, fill, h) {
  h = h || 24;
  if (!data.length) {
    poly.setAttribute('points', '');
    if (fill) fill.setAttribute('d', '');
    return;
  }
  const n = data.length;
  const xy = data.map((v, i) => [
    (i / Math.max(1, n - 1)) * 100,
    h - (Math.max(0, Math.min(100, v)) / 100) * h,
  ]);
  poly.setAttribute('points', xy.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' '));
  if (fill) {
    const d = `M0,${h} ` + xy.map(([x, y]) => `L${x.toFixed(1)},${y.toFixed(1)}`).join(' ') + ` L100,${h} Z`;
    fill.setAttribute('d', d);
  }
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
  // Every host starts on Overview, and nothing another host loaded is left on
  // screen: the panes hold that host's software list, log excerpt and patch
  // output, all of which would otherwise be read as this one's.
  loadedPanes = new Set();
  showPane('overview');

  detail.agent = agentID;
  modal.classList.remove('hidden');
  const h = hostByID(agentID);
  mTitle.textContent = h ? (h.hostname || agentID) : agentID;
  mSub.textContent = h ? `${h.platform || h.os} · ${h.arch}` : '';
  renderFacts(h);
  // Also drawn here, not only from the poll: refreshDetailLive runs every three
  // seconds, so opening a host left the Processes tab blank until the next one
  // landed. Harmless when everything was on one scroll; obvious on a tab.
  renderProcs(h);
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
  document.getElementById('prefSvc').value = (p.services || []).join(', ');
  showMuteState(p);
  refreshFixUI(agentID);
  // What the hub has actually observed, so the list is not just a wish. Only
  // services already polled appear; one added a moment ago simply has no state
  // until the next sweep.
  const st = document.getElementById('prefSvcState');
  const h = hostByID(agentID);
  const svc = (h && h.services) || null;
  st.textContent = svc && Object.keys(svc).length
    ? Object.entries(svc).map(([n, up]) => `${n}: ${up ? 'running' : 'stopped'}`).join('  ·  ')
    : '';
  st.className = 'muted';
}

// ---- auto-fix ----
//
// A rule paired with a script: when the condition fires the hub runs the script
// and only alerts if it is still true afterwards.
let allScripts = [];

async function refreshFixUI(agentID) {
  const h = hostByID(agentID);
  const p = allPrefs[agentID] || {};
  const ruleSel = document.getElementById('fixRule');
  const scriptSel = document.getElementById('fixScript');

  // Only conditions this host can actually raise are offered — a rule that
  // cannot fire here is a setting that silently never does anything.
  const rules = [['cpu', 'CPU threshold'], ['mem', 'memory threshold'], ['disk', 'disk threshold']];
  for (const name of (p.services || [])) rules.push(['service:' + name, name + ' stopped']);
  ruleSel.innerHTML = rules.map(([v, l]) => `<option value="${escapeHtml(v)}">${escapeHtml(l)}</option>`).join('');

  try { allScripts = await authJSON('/api/scripts', 'GET') || []; } catch (_) { allScripts = []; }
  scriptSel.innerHTML = allScripts.length
    ? allScripts.map(s => `<option value="${escapeHtml(s.id)}">${escapeHtml(s.name)}</option>`).join('')
    : '<option value="">no scripts saved yet</option>';

  renderFixList(agentID);
}

function renderFixList(agentID) {
  const p = allPrefs[agentID] || {};
  const box = document.getElementById('fixList');
  const entries = Object.entries(p.remediate || {});
  if (!entries.length) { box.textContent = 'nothing set — alerts are sent straight out'; return; }
  const name = id => (allScripts.find(s => s.id === id) || {}).name || id;
  box.innerHTML = entries.map(([rule, sid]) =>
    `<span class="fix">${escapeHtml(rule)} → ${escapeHtml(name(sid))} ` +
    `<a href="#" class="fix-rm" data-rule="${escapeHtml(rule)}">✕</a></span>`).join(' ');
  box.querySelectorAll('.fix-rm').forEach(a => a.onclick = (e) => {
    e.preventDefault();
    const cur = { ...(allPrefs[agentID] || {}).remediate };
    delete cur[a.dataset.rule];
    savePrefs({ pref: { remediate: cur } }).then(() => renderFixList(agentID));
  });
}

document.getElementById('fixAdd').addEventListener('click', async () => {
  const h = hostByID(detail.agent);
  if (!h) return;
  const rule = document.getElementById('fixRule').value;
  const script = document.getElementById('fixScript').value;
  if (!rule || !script) return;
  const cur = { ...((allPrefs[h.agent_id] || {}).remediate || {}) };
  cur[rule] = script;
  await savePrefs({ pref: { remediate: cur } });
  renderFixList(h.agent_id);
});

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
  // Built on top of what is already stored, because the hub replaces the whole
  // entry rather than merging: sending only the thresholds silently cleared the
  // host's mute, and would now drop its watched services too.
  const current = allPrefs[h.agent_id] || {};
  const body = {
    agent_id: h.agent_id,
    pref: {
      ...current,
      cpu: num(prefEls.cpu), mem: num(prefEls.mem), disk: num(prefEls.disk),
      services: splitServices(document.getElementById('prefSvc').value),
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
document.getElementById('prefSvcSave').addEventListener('click', () => savePrefs(null));
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
// ---- detail modal tabs ----
//
// Panes are switched here rather than by each panel knowing about the others,
// and a pane whose content is fetched on demand loads the first time it is
// shown — the buttons remain for reloading. Patches is deliberately not
// auto-loaded: checking asks the host's package manager, which is real work on
// the far end rather than a read of something the hub already holds.
const AUTOLOAD = {
  software: () => document.getElementById('mInvBtn').click(),
  logs: () => document.getElementById('logsBtn').click(),
};
let loadedPanes = new Set();

function showPane(name) {
  for (const t of document.querySelectorAll('#mTabs .mtab')) {
    t.classList.toggle('active', t.dataset.pane === name);
  }
  for (const p of document.querySelectorAll('.mpane')) {
    p.classList.toggle('hidden', p.dataset.pane !== name);
  }
  if (AUTOLOAD[name] && !loadedPanes.has(name)) {
    loadedPanes.add(name);
    try { AUTOLOAD[name](); } catch (_) {}
  }
}

document.getElementById('mTabs').addEventListener('click', (e) => {
  const t = e.target.closest('.mtab');
  if (t) showPane(t.dataset.pane);
});

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

// ---- network discovery ----
const discoverModal = document.getElementById('discoverModal');

document.getElementById('discoverBtn').addEventListener('click', () => {
  discoverModal.classList.remove('hidden');
  runDiscovery();
});
document.getElementById('discoverClose').addEventListener('click', () => discoverModal.classList.add('hidden'));
discoverModal.addEventListener('click', e => { if (e.target === discoverModal) discoverModal.classList.add('hidden'); });

async function runDiscovery() {
  const body = document.getElementById('discoverBody');
  body.innerHTML = '<div class="muted">sweeping the network — this takes a few seconds…</div>';
  try {
    const r = await authJSON('/api/discover');
    const list = r.devices || [];
    if (!list.length) { body.innerHTML = '<div class="muted">Nothing found.</div>'; return; }
    const news = list.filter(d => !d.monitored).length;
    body.innerHTML =
      `<div class="muted" style="margin-bottom:10px">${list.length} device(s), ${news} not yet monitored</div>` +
      '<table class="proc-table"><thead><tr><th>Address</th><th>Name</th><th>MAC</th>' +
      '<th>Open ports</th><th></th></tr></thead><tbody>' +
      list.map((d, i) => `<tr class="${d.monitored ? 'disc-known' : ''}">` +
        `<td>${escapeHtml(d.ip)}</td>` +
        `<td>${escapeHtml(d.name || '')}</td>` +
        `<td class="aud-detail">${escapeHtml(d.mac)}</td>` +
        `<td>${(d.ports || []).join(', ')}</td>` +
        `<td>${d.monitored
            ? `<span class="muted">${escapeHtml(d.why || 'already known')}</span>`
            : `<button class="btn disc-add" data-i="${i}">Monitor</button>`}</td></tr>`).join('') +
      '</tbody></table>';

    body.querySelectorAll('.disc-add').forEach(b => b.onclick = async () => {
      const d = list[parseInt(b.dataset.i, 10)];
      // Added by MAC, not by address: this device was found on DHCP and the
      // address it holds today is not a fact worth writing down.
      const name = prompt('Name for ' + d.ip + '?', d.name || d.ip);
      if (name === null) return;
      b.disabled = true;
      try {
        await authJSON('/api/netchecks', 'POST', { name: name || d.ip, address: d.ip, mac: d.mac });
        b.replaceWith(Object.assign(document.createElement('span'),
          { className: 'muted', textContent: 'added' }));
        loadNetChecks();
      } catch (e) { b.disabled = false; alert('Could not add: ' + e.message); }
    });
  } catch (e) {
    body.innerHTML = `<div class="muted">Discovery failed: ${escapeHtml(e.message)}</div>`;
  }
}

// ---- audit trail ----
const auditModal = document.getElementById('auditModal');

document.getElementById('auditBtn').addEventListener('click', () => {
  auditModal.classList.remove('hidden');
  loadAudit();
});
document.getElementById('auditClose').addEventListener('click', () => auditModal.classList.add('hidden'));
auditModal.addEventListener('click', e => { if (e.target === auditModal) auditModal.classList.add('hidden'); });
document.getElementById('auditFilter').addEventListener('change', loadAudit);

async function loadAudit() {
  const body = document.getElementById('auditBody');
  const filter = document.getElementById('auditFilter').value;
  body.innerHTML = '<div class="muted">loading…</div>';
  try {
    const r = await authJSON('/api/audit?limit=200&action=' + encodeURIComponent(filter));
    if (r.note) { body.innerHTML = `<div class="no-data">${escapeHtml(r.note)}</div>`; return; }
    const ev = r.events || [];
    if (!ev.length) { body.innerHTML = '<div class="muted">Nothing recorded yet.</div>'; return; }
    body.innerHTML = '<table class="proc-table"><thead><tr><th>When</th><th>Who</th>' +
      '<th>Action</th><th>Target</th><th>From</th><th>Detail</th></tr></thead><tbody>' +
      ev.map(e => {
        // A denied outcome is the row somebody is looking for, so it is marked
        // rather than left to be spotted in a column of identical text.
        const bad = e.outcome === 'denied' || e.outcome === 'failed';
        return `<tr class="${bad ? 'run-bad' : ''}">` +
          `<td>${new Date(e.ts * 1000).toLocaleString()}</td>` +
          `<td>${escapeHtml(e.actor)}</td>` +
          `<td>${escapeHtml(e.action)}${bad ? ' · ' + escapeHtml(e.outcome) : ''}</td>` +
          `<td>${escapeHtml(auditTarget(e.target))}</td>` +
          `<td>${escapeHtml(e.remote || '')}</td>` +
          `<td class="aud-detail" title="${escapeHtml(e.detail || '')}">${escapeHtml(e.detail || '')}</td></tr>`;
      }).join('') + '</tbody></table>';
  } catch (e) {
    body.innerHTML = `<div class="muted">Could not load: ${escapeHtml(e.message)}</div>`;
  }
}

// Targets are agent ids, selectors, or usernames for sign-in events.
function auditTarget(t) {
  if (!t) return '';
  if (t === 'all') return 'all online hosts';
  if (t.startsWith('tag:')) return 'tagged "' + t.slice(4) + '"';
  if (t.startsWith('os:')) return 'every ' + t.slice(3) + ' host';
  const h = hostByID(t);
  return h ? (h.hostname || t) : t;
}

// A watch list is typed as free text; the hub rejects anything that is not a
// plausible service name, so trimming here only keeps the display honest.
function splitServices(v) {
  return String(v).split(',').map(x => x.trim()).filter(Boolean);
}

// ---- fleet actions ----
const fleetModal = document.getElementById('fleetModal');

document.getElementById('fleetBtn').addEventListener('click', () => {
  fillFleetTargets();
  loadPolicies();
  showPolicyFor(document.getElementById('fleetTarget').value);
  document.getElementById('fleetOut').classList.add('hidden');
  document.getElementById('fleetStatus').textContent = '';
  fleetModal.classList.remove('hidden');
});
document.getElementById('fleetClose').addEventListener('click', () => fleetModal.classList.add('hidden'));
fleetModal.addEventListener('click', e => { if (e.target === fleetModal) fleetModal.classList.add('hidden'); });

// The same selectors the scripts panel offers, resolved by the hub at run time
// so a host enrolled later is covered without anyone revisiting this list.
function fillFleetTargets() {
  const sel = document.getElementById('fleetTarget');
  const keep = sel.value;
  const hosts = lastHosts.filter(h => h.online && h.can_exec);
  sel.innerHTML = '';
  const add = (value, label) => {
    const o = document.createElement('option');
    o.value = value; o.textContent = label;
    sel.appendChild(o);
  };
  add('all', `▸ All online hosts (${hosts.length})`);
  for (const os of [...new Set(hosts.map(h => h.os).filter(Boolean))].sort()) {
    add('os:' + os, `▸ Every ${os} host (${hosts.filter(h => h.os === os).length})`);
  }
  const tags = new Set();
  for (const h of hosts) for (const t of hostTags(h)) tags.add(t.toLowerCase());
  for (const t of [...tags].sort()) {
    add('tag:' + t, `▸ Tagged "${t}"`);
  }
  for (const h of hosts) add(h.agent_id, h.hostname || h.agent_id);
  if (keep) sel.value = keep;
}

document.getElementById('fleetRun').addEventListener('click', async () => {
  const target = document.getElementById('fleetTarget').value;
  const action = document.getElementById('fleetAction').value;
  const out = document.getElementById('fleetOut');
  const status = document.getElementById('fleetStatus');
  const label = document.getElementById('fleetTarget').selectedOptions[0].textContent;
  // Rebooting a set of machines is not something to discover you have done.
  if (action === 'reboot' && !confirm(`Reboot ${label}?\n\nEvery matching host restarts now.`)) return;

  status.textContent = 'running… this can take a while on many hosts';
  out.classList.add('hidden');
  try {
    const r = await authJSON('/api/fleet', 'POST', { target, action });
    status.textContent = `${r.total - r.failed}/${r.total} succeeded`
      + (r.failed ? ` — ${r.failed} failed` : '');
    // Every host named, failures first, because a fleet action that reports one
    // verdict hides the machines that did not do it.
    out.textContent = r.results.map(x =>
      `── ${x.hostname || x.agent_id} — ${x.ok ? 'ok' : 'FAILED'}\n${(x.output || '').trim()}`
    ).join('\n\n');
    out.classList.remove('hidden');
  } catch (e) {
    status.textContent = 'failed: ' + e.message;
  }
});

// ---- alerting policies ----
//
// A policy is an entry in the same prefs store keyed by a selector rather than
// an agent id, so the hub resolves it for every matching host and a machine's
// own thresholds still win.
async function loadPolicies() {
  const list = document.getElementById('polList');
  try {
    const all = await authJSON('/api/hostprefs', 'GET');
    const keys = Object.keys(all || {}).filter(k => /^(all|tag:|os:)/.test(k)).sort();
    if (!keys.length) { list.innerHTML = '<div class="muted">No policies set.</div>'; return; }
    list.innerHTML = keys.map(k => {
      const p = all[k];
      const bits = [
        p.cpu ? `CPU ${p.cpu}%` : '', p.mem ? `MEM ${p.mem}%` : '', p.disk ? `DISK ${p.disk}%` : '',
        (p.services || []).length ? `watching ${p.services.join(', ')}` : '',
      ].filter(Boolean).join(' · ') || 'nothing set';
      return `<div class="pol"><span class="pol-k">${escapeHtml(k)}</span>` +
        `<span class="pol-v">${escapeHtml(bits)}</span></div>`;
    }).join('');
  } catch (e) { list.innerHTML = '<div class="muted">Could not load policies: ' + escapeHtml(e.message) + '</div>'; }
}

// Shows what is already stored for whatever the target picker is pointing at,
// so saving edits that policy instead of silently starting a second one.
async function showPolicyFor(key) {
  const st = document.getElementById('polStatus');
  st.textContent = '';
  try {
    const all = await authJSON('/api/hostprefs', 'GET');
    const p = (all || {})[key] || {};
    document.getElementById('polCPU').value = p.cpu || '';
    document.getElementById('polMem').value = p.mem || '';
    document.getElementById('polDisk').value = p.disk || '';
    document.getElementById('polSvc').value = (p.services || []).join(', ');
  } catch (_) {}
}

async function savePolicy(clear) {
  const key = document.getElementById('fleetTarget').value;
  const st = document.getElementById('polStatus');
  const num = id => {
    const v = parseFloat(document.getElementById(id).value);
    return Number.isFinite(v) && v > 0 ? v : 0;
  };
  const pref = clear ? { cpu: 0, mem: 0, disk: 0, services: [] }
    : {
        cpu: num('polCPU'), mem: num('polMem'), disk: num('polDisk'),
        services: splitServices(document.getElementById('polSvc').value),
      };
  st.textContent = 'saving…';
  try {
    await authJSON('/api/hostprefs', 'POST', { agent_id: key, pref });
    if (clear) { for (const i of ['polCPU', 'polMem', 'polDisk', 'polSvc']) document.getElementById(i).value = ''; }
    st.textContent = clear ? `cleared for ${key}` : `saved for ${key}`;
    loadPolicies();
  } catch (e) { st.textContent = 'failed: ' + e.message; }
}

document.getElementById('polSave').addEventListener('click', () => savePolicy(false));
document.getElementById('polClear').addEventListener('click', () => savePolicy(true));
document.getElementById('fleetTarget').addEventListener('change', (e) => showPolicyFor(e.target.value));

// Bridge for scripts.js (shares auth + the current host list).
window.autormm = {
  token,
  execHosts: () => lastHosts.filter(h => h.online && h.can_exec),
  allHosts: () => lastHosts,
  // Run records carry an agent id; a per-host result list has to name machines.
  hostName: (id) => {
    const h = hostByID(id);
    return h ? (h.hostname || id) : id;
  },
};

poll();
document.getElementById('emptyClear').addEventListener('click', () => {
  hostQuery = '';
  document.getElementById('hostSearch').value = '';
  // Cleared from storage too, or the tag filter comes back on the next reload
  // and the dashboard looks empty all over again.
  tagFilter = '';
  localStorage.removeItem('autormm_tagfilter');
  tagFilterEl.value = '';
  if (lastHosts) render(lastHosts);
});

document.getElementById('hostSearch').addEventListener('input', (e) => {
  hostQuery = e.target.value.trim().toLowerCase();
  if (lastHosts) render(lastHosts); // re-filter the poll we already have
});

setInterval(poll, 3000);

// ---- agentless network devices ----
// Half a homelab is not a computer: switches, printers, appliances, the
// zero-trust connector. They cannot run an agent, so the hub probes them
// directly and shows them beside the hosts they sit alongside.
const netSection = document.getElementById('netSection');
const netGrid = document.getElementById('netGrid');
const netModal = document.getElementById('netModal');

// One store serves both: an entry is an app when it says so, and the only
// differences are how it is checked (HTTP rather than a socket) and which
// section it appears in.
async function loadNetChecks() {
  let list = [];
  try {
    list = await authJSON('/api/netchecks', 'GET');
  } catch (e) { return; } // unauthenticated or unsupported: leave the sections alone
  list = list || [];
  renderMonitorSection('app', list.filter(c => c.kind === 'app'));
  renderMonitorSection('net', list.filter(c => c.kind !== 'app'));
}

function renderMonitorSection(which, list) {
  const section = document.getElementById(which === 'app' ? 'appSection' : 'netSection');
  const grid = document.getElementById(which === 'app' ? 'appGrid' : 'netGrid');
  const summary = document.getElementById(which === 'app' ? 'appSummary' : 'netSummary');
  // Hidden entirely when nothing is configured, so a fleet that does not use
  // this never sees an empty shelf.
  section.classList.toggle('hidden', list.length === 0);
  summary.textContent = list.length ? `${list.filter(c => c.up).length}/${list.length} reachable` : '';

  grid.innerHTML = '';
  for (const c of list) {
    const el = document.createElement('div');
    el.className = 'netcard';

    const dot = document.createElement('span');
    dot.className = 'status ' + (c.checked ? (c.up ? 'online' : 'offline') : '');

    // A real link, not window.open. Passing a features string to window.open
    // makes browsers treat it as a popup rather than a tab, and popup blockers
    // stop it — which presents as a blank window opening and nothing loading.
    // An anchor is what the browser expects and is never blocked.
    const body = document.createElement(c.web ? 'a' : 'div');
    body.className = 'nc-body';
    if (c.web) {
      body.href = c.web;
      body.target = '_blank';
      body.rel = 'noopener noreferrer';
      body.title = 'Open ' + c.web;
    } else {
      body.title = 'No web interface on this port — add a URL to link one';
    }
    const name = document.createElement('div');
    name.className = 'nc-name';
    name.textContent = c.name || c.address || c.url;
    const sub = document.createElement('div');
    sub.className = 'nc-sub';
    sub.textContent = monitorSubtitle(c);
    // Only on a compact card, where the subtitle *is* the reading. On a card
    // with bars the colour is already on the bar, and a red IP address beside
    // it reads as though the address were the problem.
    const level = snmpLevel(c.snmp);
    if (level && !snmpMetrics(c.snmp).length) sub.classList.add('nc-' + level);
    // The reason a check failed is the whole diagnosis and was being collected
    // and then thrown away. It is long, so it goes in the tooltip.
    if (!c.up && c.error) sub.title = c.error;
    // An SNMP failure does not make the device unreachable, so it is a tooltip
    // and a marker rather than a red dot that would misreport the device.
    // A learned MAC is worth showing: it is the difference between a device
    // that survives a DHCP change and one that does not, and it happened
    // without anyone asking for it.
    const detail = snmpDetail(c.snmp);
    if (detail) sub.title = detail;
    if (c.mac && c.mac_learned) {
      sub.title = (sub.title ? sub.title + '\n' : '') +
        'MAC learned automatically (' + c.mac + ') — this device is now found ' +
        'by MAC if its address changes, and still checked at its address if the ' +
        'MAC cannot be found.';
    }
    if (c.snmp && c.snmp.error) {
      sub.title = 'SNMP: ' + c.snmp.error;
      name.textContent += ' ⚠';
    }
    body.append(name, sub);

    // A device that reports readings gets bars and a full-width card. One that
    // only answers a TCP check stays a single compact row — most of a homelab's
    // devices have nothing more to say, and giving them all the taller card
    // would be a lot of white space to make a firewall look better.
    const metrics = snmpMetrics(c.snmp);
    if (metrics.length) {
      el.classList.add('nc-rich');
      const box = document.createElement('div');
      box.className = 'nc-metrics';
      box.innerHTML = metrics.map(m =>
        `<div class="nc-metric"><label>${escapeHtml(m.label)}</label>` +
        `<span class="bar"><i class="${m.level ? 'hot' : ''}" style="width:${Math.max(0, Math.min(100, m.percent))}%"></i></span>` +
        `<span class="val">${escapeHtml(m.text)}</span></div>`).join('');
      body.appendChild(box);

      const facts = snmpFacts(c.snmp);
      if (facts) {
        const f = document.createElement('div');
        f.className = 'nc-facts';
        f.textContent = facts;
        body.appendChild(f);
      }
    }

    // Editing exists on the hub already — a save with an existing id updates in
    // place — so this is only the affordance. Without it, adding SNMP to a
    // device you already watch means deleting and re-adding it.
    const edit = document.createElement('button');
    edit.className = 'nc-del nc-edit';
    edit.title = 'Edit';
    edit.textContent = '✎';
    edit.onclick = (e) => {
      e.preventDefault();
      e.stopPropagation(); // editing must not also follow the link
      openCheckEditor(c);
    };

    const del = document.createElement('button');
    del.className = 'nc-del';
    del.title = 'Stop monitoring';
    del.textContent = '✕';
    del.onclick = async (e) => {
      e.preventDefault();
      e.stopPropagation(); // removing must not also follow the link
      if (!confirm(`Stop monitoring ${c.name || c.address || c.url}?`)) return;
      try {
        await authJSON('/api/netchecks?id=' + encodeURIComponent(c.id), 'DELETE');
        loadNetChecks();
      } catch (err) { alert('Could not remove: ' + err.message); }
    };

    el.append(dot, body, edit, del);
    grid.appendChild(el);
  }
}

// snmpSummary is the one line a device card has room for: whichever of the
// things SNMP reported is worth interrupting someone about, in that order.
function snmpSummary(s) {
  if (!s) return '';
  const bits = [];
  if (s.ups) {
    // A UPS running the rack off its battery is the whole reason to poll one.
    if (s.ups.on_battery) {
      bits.push('ON BATTERY' + (s.ups.seconds_on_battery ? ` ${s.ups.seconds_on_battery}s` : ''));
    }
    if (s.ups.battery_low) bits.push('battery low');
    if (s.ups.charge_percent >= 0) bits.push(`battery ${s.ups.charge_percent}%`);
    // How long it would last and how hard it is working — the two figures that
    // decide whether a power cut is a shrug or a scramble.
    if (s.ups.minutes_remaining) bits.push(`${s.ups.minutes_remaining}m left`);
    if (s.ups.load_percent) bits.push(`${s.ups.load_percent}% load`);
  }
  // Only supplies that reported a figure; the MIB's "will not say" is not 0%.
  const known = (s.supplies || []).filter(x => x.percent >= 0);
  if (known.length) {
    // Figure first: at half a sidebar's width "Black Toner 8%" truncates to
    // "Black To…" and loses the only part worth reading.
    const low = known.reduce((a, b) => (a.percent <= b.percent ? a : b));
    bits.push(`${low.percent}% ${low.name}`);
  }
  // Host-style readings come before the port count: a firewall's CPU and memory
  // are what you look at, and its interface total counts loopback and pflog
  // alongside the real ports anyway.
  if (s.cpu_percent) bits.push(`cpu ${s.cpu_percent}%`);
  if (s.mem_percent) bits.push(`mem ${s.mem_percent}%`);
  if (s.disk_percent) bits.push(`disk ${s.disk_percent}%`);
  if (s.pf_states) {
    const text = s.pf_state_limit
      ? `${fmtCount(s.pf_states)}/${fmtCount(s.pf_state_limit)} states`
      : `${fmtCount(s.pf_states)} states`;
    // A firewall runs out of state table before it runs out of anything else,
    // so when it is filling up it goes to the front rather than being trimmed
    // off the end behind cpu and memory.
    if (pfStatePressure(s) >= 0.8) bits.unshift(text);
    else bits.push(text);
  }
  // Not on a UPS: its management card reports a loopback and an ethernet port,
  // so "2/2 up" is two words of nothing next to the battery state.
  if (s.if_total && !s.ups) bits.push(`${s.if_up}/${s.if_total} up`);
  if (!bits.length && s.uptime_secs) bits.push('up ' + fmtUptime(s.uptime_secs));
  return bits.slice(0, 3).join(' · ');
}

// pfStatePressure is how full the state table is, 0 when not reported.
function pfStatePressure(s) {
  if (!s || !s.pf_states || !s.pf_state_limit) return 0;
  return s.pf_states / s.pf_state_limit;
}

// fmtCount keeps a state table's occupancy readable: "42.1k", not "42133".
function fmtCount(n) {
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

// snmpMetrics picks the readings worth a bar. Percentages only: a bar for
// something that is not out of a hundred is a decoration.
function snmpMetrics(s) {
  if (!s) return [];
  const out = [];
  if (s.ups) {
    if (s.ups.charge_percent >= 0) {
      out.push({
        label: 'BATT', percent: s.ups.charge_percent,
        text: s.ups.charge_percent + '%',
        level: s.ups.on_battery || s.ups.battery_low || s.ups.charge_percent < 50,
      });
    }
    if (s.ups.load_percent) {
      out.push({ label: 'LOAD', percent: s.ups.load_percent, text: s.ups.load_percent + '%', level: s.ups.load_percent >= 80 });
    }
  }
  if (s.cpu_percent) out.push({ label: 'CPU', percent: s.cpu_percent, text: s.cpu_percent + '%', level: s.cpu_percent >= 90 });
  if (s.mem_percent) out.push({ label: 'MEM', percent: s.mem_percent, text: s.mem_percent + '%', level: s.mem_percent >= 95 });
  if (s.disk_percent) {
    out.push({ label: 'DISK', percent: s.disk_percent, text: s.disk_percent + '%', level: s.disk_percent >= 90 });
  }
  if (s.pf_states && s.pf_state_limit) {
    const pct = (s.pf_states / s.pf_state_limit) * 100;
    out.push({ label: 'STATE', percent: pct, text: fmtCount(s.pf_states), level: pct >= 80 });
  }
  // Consumables that reported a figure. The MIB's "will not say" is not 0%.
  for (const x of (s.supplies || []).filter(v => v.percent >= 0)) {
    out.push({ label: shortSupply(x.name), percent: x.percent, text: x.percent + '%', level: x.percent <= 10 });
  }
  return out.slice(0, 6);
}

// shortSupply trims a cartridge name to something that fits a label column.
// "Black Toner Cartridge HP 26X" is a label, not a sentence.
function shortSupply(name) {
  const n = String(name).replace(/\b(cartridge|toner|unit|supply)\b/gi, '').trim();
  return (n.split(/\s+/)[0] || name).slice(0, 6).toUpperCase();
}

// snmpFacts is the line under the bars: the figures that are not percentages.
function snmpFacts(s) {
  if (!s) return '';
  const bits = [];
  if (s.ups && s.ups.on_battery) {
    bits.push('ON BATTERY' + (s.ups.seconds_on_battery ? ` ${s.ups.seconds_on_battery}s` : ''));
  }
  if (s.ups && s.ups.minutes_remaining) bits.push(`${s.ups.minutes_remaining}m runtime`);
  if (s.load1) bits.push('load ' + s.load1.toFixed(2));
  if (s.if_total && !s.ups) bits.push(`${s.if_up}/${s.if_total} up`);
  if (s.uptime_secs) bits.push('up ' + fmtUptime(s.uptime_secs));
  return bits.slice(0, 3).join('  ·  ');
}

// snmpDetail is everything the device reported, for the tooltip. The subtitle
// has room for three figures; a firewall reports rather more than three.
function snmpDetail(s) {
  if (!s) return '';
  const rows = [];
  if (s.sys_name) rows.push('name: ' + s.sys_name);
  if (s.sys_descr) rows.push(s.sys_descr);
  if (s.uptime_secs) rows.push('uptime: ' + fmtUptime(s.uptime_secs));
  if (s.cpu_percent) rows.push('cpu: ' + s.cpu_percent + '%');
  if (s.load1) rows.push('load: ' + s.load1.toFixed(2));
  if (s.mem_percent) rows.push('memory: ' + s.mem_percent + '%');
  if (s.disk_percent) rows.push('disk: ' + s.disk_percent + '%' + (s.disk_name ? ' (' + s.disk_name + ')' : ''));
  if (s.pf_states) rows.push('pf states: ' + s.pf_states + (s.pf_state_limit ? ' of ' + s.pf_state_limit : ''));
  if (s.if_total) rows.push('interfaces: ' + s.if_up + ' up of ' + s.if_total);
  for (const x of (s.supplies || [])) {
    rows.push(x.name + ': ' + (x.percent >= 0 ? x.percent + '%' : 'not reported'));
  }
  if (s.ups) {
    rows.push('battery: ' + (s.ups.charge_percent >= 0 ? s.ups.charge_percent + '%' : 'unknown') +
      (s.ups.on_battery ? ' — ON BATTERY' : ''));
    if (s.ups.minutes_remaining) rows.push('runtime: ' + s.ups.minutes_remaining + ' minutes');
    if (s.ups.load_percent) rows.push('output load: ' + s.ups.load_percent + '%');
  }
  if (s.version) rows.push('(SNMP ' + s.version + ')');
  return rows.join('\n');
}

// snmpLevel grades a reading, so a UPS running on battery is not the same
// muted grey as a switch quietly reporting its port count.
function snmpLevel(s) {
  if (!s) return '';
  if (s.ups && (s.ups.on_battery || s.ups.battery_low)) return 'bad';
  const known = (s.supplies || []).filter(x => x.percent >= 0);
  if (known.some(x => x.percent <= 10)) return 'bad';
  if (known.some(x => x.percent <= 25)) return 'warn';
  if (s.if_total && s.if_up === 0) return 'bad';
  // A full state table drops connections; a filling one is the warning before.
  const pf = pfStatePressure(s);
  if (pf >= 0.9) return 'bad';
  if (pf >= 0.8) return 'warn';
  if (s.disk_percent >= 90 || s.mem_percent >= 95) return 'warn';
  return '';
}

// shortProbeError turns a Go dial error into something readable at a glance.
// The full text stays in the tooltip.
function shortProbeError(err) {
  if (!err) return '';
  const e = err.toLowerCase();
  if (e.includes('no such host')) return 'name not found';
  if (e.includes('timeout') || e.includes('deadline')) return 'timed out';
  if (e.includes('refused')) return 'refused';
  if (e.includes('no route')) return 'no route';
  if (e.includes('certificate')) return 'TLS problem';
  if (e.includes('unsupported protocol') || e.includes('invalid')) return 'bad address';
  return 'unreachable';
}

// What a card says under its name. An app that answers with something other
// than success is reachable *and* worth knowing about — 502 means the proxy is
// up and whatever sits behind it is not, which a bare green dot would hide.
function monitorSubtitle(c) {
  if (!c.checked) return 'not checked yet';
  // A MAC-tracked device is worth showing at the address it is at *now* —
  // that changing number is the whole point of tracking it by MAC.
  const host = (c.mac && c.ip) ? c.ip : c.address;
  const where = c.kind === 'app'
    ? (c.web || '').replace(/^https?:\/\//, '')
    : `${host || c.mac}${c.port ? ':' + c.port : ''}`;
  if (!c.up) {
    // Say what went wrong, briefly. "unreachable" alone gives nothing to act
    // on, and the full error is in the tooltip.
    const why = shortProbeError(c.error);
    return `${where} · ${why || 'unreachable'}`;
  }
  const code = (c.kind === 'app' && c.code && (c.code < 200 || c.code >= 400)) ? ` · HTTP ${c.code}` : '';
  // A device with bars says its readings there, so the subtitle goes back to
  // identifying the thing — its own name for itself is more use than a latency.
  if (snmpMetrics(c.snmp).length) {
    const who = c.snmp.sys_descr || c.snmp.sys_name;
    return who ? `${where} · ${who}` : where;
  }
  const snmp = snmpSummary(c.snmp);
  if (snmp) return `${where} · ${snmp}`;
  return `${where} · ${Math.round(c.latency_ms)}ms${code}`;
}

// The id being edited, or "" when adding. The hub decides create-vs-update from
// whether an id is present.
let editingCheck = '';

function setSNMPVersionUI(v) {
  document.querySelector('.snmp-community').classList.toggle('hidden', !(v === 'auto' || v === '1' || v === '2c'));
  document.querySelector('.snmp-v3').classList.toggle('hidden', v !== '3');
}
document.getElementById('netSNMPVersion').addEventListener('change', e => setSNMPVersionUI(e.target.value));

// Opens the device form on an existing check. Apps have their own, simpler form.
function openCheckEditor(c) {
  if (c.kind === 'app') { openAppEditor(c); return; }
  editingCheck = c.id;
  document.getElementById('netModalTitle').textContent = 'Edit ' + (c.name || 'device');
  document.getElementById('netSave').textContent = 'Save changes';
  const set = (id, v) => { document.getElementById(id).value = v == null ? '' : v; };
  set('netName', c.name);
  set('netAddr', c.address);
  set('netPort', c.port || '');
  set('netTags', c.tags);
  set('netURL', c.url);
  set('netMAC', c.mac);
  // A learned MAC is shown in the field so it can be corrected or cleared;
  // saving it makes it the operator's, which is the stricter interpretation.
  document.getElementById('netMACHint').textContent = c.mac_learned
    ? 'learned automatically — saving this form adopts it as fixed'
    : '';
  // The secrets come back as a placeholder, and leaving it is what tells the
  // hub to keep what it has.
  set('netSNMP', c.snmp);
  set('netSNMPPort', c.snmp_port || '');
  set('netSNMPUser', c.snmp_user);
  set('netSNMPAuthPass', c.snmp_auth_pass);
  set('netSNMPPrivPass', c.snmp_priv_pass);
  if (c.snmp_auth_proto) set('netSNMPAuthProto', c.snmp_auth_proto);
  if (c.snmp_priv_proto) set('netSNMPPrivProto', c.snmp_priv_proto);
  const v = c.snmp_version || (c.snmp ? 'auto' : '');
  document.getElementById('netSNMPVersion').value = v;
  setSNMPVersionUI(v);
  document.getElementById('netStatus').textContent = '';
  netModal.classList.remove('hidden');
}

function resetCheckEditor() {
  editingCheck = '';
  document.getElementById('netModalTitle').textContent = 'Monitor a network device';
  document.getElementById('netSave').textContent = 'Add device';
  for (const id of ['netName', 'netAddr', 'netPort', 'netTags', 'netURL', 'netMAC',
                    'netSNMP', 'netSNMPPort', 'netSNMPUser', 'netSNMPAuthPass', 'netSNMPPrivPass']) {
    document.getElementById(id).value = '';
  }
  document.getElementById('netSNMPVersion').value = '';
  setSNMPVersionUI('');
  document.getElementById('netStatus').textContent = '';
}

document.getElementById('netAdd').addEventListener('click', () => {
  resetCheckEditor();
  netModal.classList.remove('hidden');
});

// ---- hosted apps ----
const appModal = document.getElementById('appModal');
let editingApp = '';

function openAppEditor(c) {
  editingApp = c.id;
  document.getElementById('appName').value = c.name || '';
  document.getElementById('appURL').value = c.url || '';
  document.getElementById('appTags').value = c.tags || '';
  document.getElementById('appSave').textContent = 'Save changes';
  document.getElementById('appStatus').textContent = '';
  appModal.classList.remove('hidden');
}

function resetAppEditor() {
  editingApp = '';
  for (const id of ['appName', 'appURL', 'appTags']) document.getElementById(id).value = '';
  document.getElementById('appSave').textContent = 'Add app';
  document.getElementById('appStatus').textContent = '';
}

document.getElementById('appAdd').addEventListener('click', () => {
  resetAppEditor();
  appModal.classList.remove('hidden');
});
document.getElementById('appClose').addEventListener('click', () => appModal.classList.add('hidden'));
appModal.addEventListener('click', e => { if (e.target === appModal) appModal.classList.add('hidden'); });

document.getElementById('appSave').addEventListener('click', async () => {
  const st = document.getElementById('appStatus');
  let url = document.getElementById('appURL').value.trim();
  if (!url) { st.textContent = 'a URL is required'; return; }
  // A bare hostname is sent as written: the hub tries https before http and
  // links the card to whichever answered. Forcing a scheme here would decide
  // for an app that only serves the other one, which is how an https-only app
  // ended up reported as down.
  let host = '';
  try {
    host = new URL(/^https?:\/\//i.test(url) ? url : 'https://' + url).hostname;
  } catch (_) { st.textContent = 'that URL is not valid'; return; }

  st.textContent = 'saving…';
  try {
    await authJSON('/api/netchecks', 'POST', {
      kind: 'app',
      name: document.getElementById('appName').value.trim(),
      address: host,   // so the entry still has a host for display and grouping
      url,
      tags: document.getElementById('appTags').value.trim(),
      ...(editingApp ? { id: editingApp } : {}),
    });
    resetAppEditor();
    appModal.classList.add('hidden');
    loadNetChecks();
  } catch (e) { st.textContent = 'failed: ' + e.message; }
});
document.getElementById('netClose').addEventListener('click', () => netModal.classList.add('hidden'));
netModal.addEventListener('click', e => { if (e.target === netModal) netModal.classList.add('hidden'); });

document.getElementById('netSave').addEventListener('click', async () => {
  const addr = document.getElementById('netAddr').value.trim();
  const mac = document.getElementById('netMAC').value.trim();
  // Choosing "SNMP off" clears the credentials rather than leaving the hub to
  // keep them, which the untouched-secret rule would otherwise do.
  if (!document.getElementById('netSNMPVersion').value) {
    for (const id of ['netSNMP', 'netSNMPUser', 'netSNMPAuthPass', 'netSNMPPrivPass']) {
      document.getElementById(id).value = '';
    }
  }
  const st = document.getElementById('netStatus');
  if (!addr && !mac) { st.textContent = 'an address or a MAC is required'; return; }
  const portRaw = document.getElementById('netPort').value.trim();
  st.textContent = 'saving…';
  try {
    await authJSON('/api/netchecks', 'POST', {
      name: document.getElementById('netName').value.trim(),
      address: addr,
      port: portRaw ? parseInt(portRaw, 10) : 0,
      tags: document.getElementById('netTags').value.trim(),
      url: document.getElementById('netURL').value.trim(),
      mac,
      snmp: document.getElementById('netSNMP').value.trim(),
      snmp_port: parseInt(document.getElementById('netSNMPPort').value, 10) || 0,
      // "auto" is the hub's empty string; the select uses a value so the option
      // can be distinguished from "SNMP off".
      snmp_version: (v => (v === 'auto' ? '' : v))(document.getElementById('netSNMPVersion').value),
      snmp_user: document.getElementById('netSNMPUser').value.trim(),
      snmp_auth_proto: document.getElementById('netSNMPAuthProto').value,
      snmp_auth_pass: document.getElementById('netSNMPAuthPass').value,
      snmp_priv_proto: document.getElementById('netSNMPPrivProto').value,
      snmp_priv_pass: document.getElementById('netSNMPPrivPass').value,
      // Saving a form makes whatever is in the MAC box the operator's own,
      // which is the stricter reading: not finding it then means the device is
      // missing rather than merely forgotten by ARP.
      mac_learned: false,
      ...(editingCheck ? { id: editingCheck } : {}),
    });
    resetCheckEditor();
    netModal.classList.add('hidden');
    loadNetChecks();
  } catch (e) { st.textContent = 'failed: ' + e.message; }
});
