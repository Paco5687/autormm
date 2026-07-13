// autormm scripts panel: manage scripts, run them on hosts, and schedule them.
(function () {
  const $ = id => document.getElementById(id);
  const modal = $('scriptsModal');
  const bridge = () => window.autormm || { token: () => '', execHosts: () => [] };

  let scripts = [];
  let current = null; // selected script or null (new)

  function auth() { return { Authorization: 'Bearer ' + bridge().token() }; }

  async function api(method, path, body) {
    const opts = { method, headers: { ...auth() } };
    if (body) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
    const res = await fetch(path, opts);
    if (res.status === 409) throw { disabled: true };
    if (!res.ok) throw new Error(await res.text());
    const ct = res.headers.get('content-type') || '';
    return ct.includes('json') ? res.json() : null;
  }

  $('scriptsBtn').addEventListener('click', open);
  $('scClose').addEventListener('click', () => modal.classList.add('hidden'));
  modal.addEventListener('click', e => { if (e.target === modal) modal.classList.add('hidden'); });
  $('scNew').addEventListener('click', () => selectScript(null));
  $('scSave').addEventListener('click', saveScript);
  $('scDelete').addEventListener('click', deleteScript);
  $('scRun').addEventListener('click', runScript);
  $('scSchedule').addEventListener('click', scheduleScript);

  async function open() {
    modal.classList.remove('hidden');
    $('scDisabled').classList.add('hidden');
    fillHosts();
    try {
      await refresh();
      selectScript(null);
    } catch (e) {
      if (e.disabled) { $('scDisabled').classList.remove('hidden'); }
      else { $('scOutput').textContent = 'error: ' + e.message; }
    }
  }

  function fillHosts() {
    const sel = $('scRunHost');
    sel.innerHTML = '';
    for (const h of bridge().execHosts()) {
      const o = document.createElement('option');
      o.value = h.agent_id;
      o.textContent = h.hostname || h.agent_id;
      sel.appendChild(o);
    }
  }

  async function refresh() {
    scripts = (await api('GET', '/api/scripts')) || [];
    renderList();
    renderSchedules(await api('GET', '/api/schedules') || []);
    renderRuns(await api('GET', '/api/runs?limit=25') || []);
  }

  function renderList() {
    const list = $('scList');
    list.innerHTML = '';
    for (const s of scripts) {
      const el = document.createElement('div');
      el.className = 'sc-item' + (current && current.id === s.id ? ' active' : '');
      el.textContent = s.name;
      el.onclick = () => selectScript(s);
      list.appendChild(el);
    }
  }

  function selectScript(s) {
    current = s;
    $('scName').value = s ? s.name : '';
    $('scShell').value = s ? s.shell : '';
    $('scContent').value = s ? s.content : '';
    $('scOutput').textContent = '';
    renderList();
  }

  async function saveScript() {
    const body = { id: current ? current.id : '', name: $('scName').value.trim(), shell: $('scShell').value, content: $('scContent').value };
    if (!body.name || !body.content) { $('scOutput').textContent = 'name and content are required'; return; }
    try {
      const saved = await api('POST', '/api/scripts', body);
      await refresh();
      selectScript(scripts.find(s => s.id === saved.id) || null);
    } catch (e) { $('scOutput').textContent = 'error: ' + e.message; }
  }

  async function deleteScript() {
    if (!current) return;
    if (!confirm(`Delete script "${current.name}"?`)) return;
    try { await api('DELETE', '/api/scripts?id=' + encodeURIComponent(current.id)); await refresh(); selectScript(null); }
    catch (e) { $('scOutput').textContent = 'error: ' + e.message; }
  }

  async function runScript() {
    if (!current) { $('scOutput').textContent = 'save the script first'; return; }
    const agent = $('scRunHost').value;
    if (!agent) { $('scOutput').textContent = 'no eligible host selected'; return; }
    $('scOutput').textContent = 'running…';
    try {
      const run = await api('POST', '/api/scripts/run', { script_id: current.id, agent_id: agent });
      $('scOutput').textContent =
        (run.stdout || '') + (run.stderr ? '\n[stderr]\n' + run.stderr : '') +
        (run.error ? '\n[error] ' + run.error : '') + `\n[exit ${run.exit_code}]`;
      renderRuns(await api('GET', '/api/runs?limit=25') || []);
    } catch (e) { $('scOutput').textContent = 'error: ' + e.message; }
  }

  async function scheduleScript() {
    if (!current) { $('scOutput').textContent = 'save the script first'; return; }
    const agent = $('scRunHost').value;
    const cron = $('scCron').value.trim();
    if (!agent || !cron) { $('scOutput').textContent = 'select a host and enter a cron expression'; return; }
    try {
      await api('POST', '/api/schedules', { script_id: current.id, agent_id: agent, cron, enabled: true });
      $('scCron').value = '';
      renderSchedules(await api('GET', '/api/schedules') || []);
    } catch (e) { $('scOutput').textContent = 'error: ' + e.message; }
  }

  function nameFor(id) { const s = scripts.find(x => x.id === id); return s ? s.name : id; }

  function renderSchedules(schedules) {
    const box = $('scSchedules');
    if (!schedules.length) { box.innerHTML = '<div class="muted" style="font-size:12px">no schedules</div>'; return; }
    box.innerHTML = '<table class="proc-table"><thead><tr><th>Script</th><th>Host</th><th>Cron</th><th></th></tr></thead><tbody>' +
      schedules.map(s => `<tr><td>${esc(nameFor(s.script_id))}</td><td>${esc(s.agent_id)}</td><td>${esc(s.cron)}</td>` +
        `<td><a href="#" data-id="${s.id}" class="sc-unsched">remove</a></td></tr>`).join('') + '</tbody></table>';
    box.querySelectorAll('.sc-unsched').forEach(a => a.onclick = async (e) => {
      e.preventDefault();
      await api('DELETE', '/api/schedules?id=' + encodeURIComponent(a.dataset.id));
      renderSchedules(await api('GET', '/api/schedules') || []);
    });
  }

  function renderRuns(runs) {
    const box = $('scRuns');
    if (!runs.length) { box.innerHTML = '<div class="muted" style="font-size:12px">no runs yet</div>'; return; }
    box.innerHTML = '<table class="proc-table"><thead><tr><th>When</th><th>Script</th><th>Host</th><th>Exit</th><th>Source</th></tr></thead><tbody>' +
      runs.map(r => `<tr><td>${new Date(r.started * 1000).toLocaleString()}</td><td>${esc(r.script_name)}</td>` +
        `<td>${esc(r.agent_id)}</td><td>${r.exit_code}</td><td>${esc(r.source)}</td></tr>`).join('') + '</tbody></table>';
  }

  function esc(s) { return String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c])); }
})();
