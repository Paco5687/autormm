// autormm dashboard: polls /api/hosts and renders host cards.
const grid = document.getElementById('grid');
const emptyEl = document.getElementById('empty');
const summaryEl = document.getElementById('summary');
const tokenBtn = document.getElementById('tokenBtn');

const TOKEN_KEY = 'autormm_token';
function token() { return localStorage.getItem(TOKEN_KEY) || ''; }

// ---- login ----
const loginModal = document.getElementById('loginModal');
async function showLogin() {
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
document.getElementById('updCheck').addEventListener('click', checkUpdates);
document.getElementById('updApply').addEventListener('click', async () => {
  const st = document.getElementById('updStatus');
  if (!confirm('Update the hub now? It downloads the new version and restarts (brief downtime).')) return;
  st.textContent = 'updating… the hub will restart';
  try {
    const r = await authFetch('/api/update/apply', 'POST');
    const d = await r.json().catch(() => ({}));
    st.textContent = (d.message || 'updating…') + ' — reload the page in ~15s';
  } catch (e) { st.textContent = 'the hub is restarting… reload in ~15s'; }
});
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
  if (!token()) { showLogin(); return; }
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

function render(hosts) {
  lastHosts = hosts;
  emptyEl.classList.toggle('hidden', hosts.length > 0);
  const online = hosts.filter(h => h.online).length;
  summaryEl.textContent = `${online}/${hosts.length} online`;

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
    c.className = 'chip' + (/offline|full|high/.test(a) ? ' bad' : '');
    c.textContent = a;
    alerts.appendChild(c);
  }

  const m = h.metrics;
  const cpu = m ? m.cpu_percent : 0;
  const mem = m ? m.mem_percent : 0;
  setBar(el.querySelector('.cpu'), cpu);
  setBar(el.querySelector('.mem'), mem);
  el.querySelector('.cpuVal').textContent = m ? cpu.toFixed(0) + '%' : '—';
  el.querySelector('.memVal').textContent = m ? mem.toFixed(0) + '%' : '—';

  sparkline(el.querySelector('.cpuSpark'), h.cpu_history || []);

  const det = el.querySelector('.details');
  if (m) {
    const disk = (m.disks || []).map(d => `${d.mount} ${d.percent.toFixed(0)}%`).join('  ');
    det.textContent =
      `up ${fmtUptime(m.uptime_secs)}  ·  load ${m.load1.toFixed(2)}\n` +
      `mem ${fmtBytes(m.mem_used)} / ${fmtBytes(m.mem_total)}\n` +
      `net ↓${fmtBytes(m.net_recv)}/s ↑${fmtBytes(m.net_sent)}/s\n` +
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
  openSession(h, { fps: 12, quality: 60 }, '/viewer');
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
    const url = `${page}?token=${encodeURIComponent(s.token)}&host=${encodeURIComponent(h.hostname || h.agent_id)}`;
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
  resetInventory();
  loadHistory();
}

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
  fileWS.onopen = () => { state.textContent = 'ready'; state.className = 'pill live'; send.disabled = false; get.disabled = false; };
  fileWS.onclose = () => { state.textContent = 'closed'; state.className = 'pill dead'; send.disabled = true; get.disabled = true; };
  fileWS.onerror = () => { state.textContent = 'error'; state.className = 'pill dead'; };
  fileWS.onmessage = onFileMsg;
}

function closeFiles() {
  if (fileWS) { try { fileWS.close(); } catch (_) {} fileWS = null; }
  dl = null;
  fileModal.classList.add('hidden');
}

function onFileMsg(ev) {
  if (typeof ev.data === 'string') {
    const m = JSON.parse(ev.data);
    if (m.t === 'ok') flog(`✓ uploaded → ${m.path} (${m.size} bytes)`);
    else if (m.t === 'err') flog(`✗ ${m.msg}`);
    else if (m.t === 'meta') { dl = { name: m.name, size: m.size, parts: [], got: 0 }; flog(`downloading ${m.name} (${m.size} bytes)…`); }
    else if (m.t === 'done') finishDownload();
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
  const f = inp.files && inp.files[0];
  if (!f || !fileWS || fileWS.readyState !== 1) return;
  fileWS.send(JSON.stringify({ t: 'put', name: f.name, size: f.size }));
  flog(`uploading ${f.name} (${f.size} bytes)…`);
  const chunk = 256 * 1024;
  for (let off = 0; off < f.size; off += chunk) {
    // simple backpressure so we don't buffer the whole file in memory
    while (fileWS.bufferedAmount > 8 * 1024 * 1024) await new Promise(r => setTimeout(r, 20));
    fileWS.send(await f.slice(off, off + chunk).arrayBuffer());
  }
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
