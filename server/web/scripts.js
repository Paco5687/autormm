// autormm scripts panel: manage scripts, run them on hosts, and schedule them.
(function () {
  const $ = id => document.getElementById(id);
  const modal = $('scriptsModal');
  const bridge = () => window.autormm ||
    { token: () => '', execHosts: () => [], hostName: (id) => id };

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
    const keep = sel.value;
    sel.innerHTML = '';
    const add = (value, label) => {
      const o = document.createElement('option');
      o.value = value;
      o.textContent = label;
      sel.appendChild(o);
    };
    // Group selectors first: the whole point of tags is not having to pick
    // hosts one at a time. Resolved at run time, so a machine enrolled later is
    // already covered without editing the script or its schedule.
    const hosts = bridge().execHosts();
    add('all', '▸ All online hosts');
    const oses = [...new Set(hosts.map(h => h.os).filter(Boolean))].sort();
    for (const os of oses) add('os:' + os, `▸ All ${os} hosts`);
    const tags = [...new Set(
      hosts.flatMap(h => (h.tags || '').split(/[,;\s]+/)).map(t => t.trim()).filter(Boolean)
    )].sort((a, b) => a.localeCompare(b));
    for (const t of tags) add('tag:' + t, `▸ Tagged "${t}"`);
    for (const h of hosts) add(h.agent_id, h.hostname || h.agent_id);
    if (keep) sel.value = keep;
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
      const res = await api('POST', '/api/scripts/run', { script_id: current.id, agent_id: agent });
      // A selector target returns {runs, targets} rather than one run. Reading
      // it as a single run printed "[exit undefined]" and no output at all, so
      // running a script across a tag reported nothing about any host.
      $('scOutput').textContent = res && res.runs ? formatRuns(res.runs) : formatRun(res);
      renderRuns(await api('GET', '/api/runs?limit=25') || []);
    } catch (e) { $('scOutput').textContent = 'error: ' + e.message; }
  }

  function formatRun(run) {
    if (!run) return 'no result';
    return (run.stdout || '') + (run.stderr ? '\n[stderr]\n' + run.stderr : '') +
      (run.error ? '\n[error] ' + run.error : '') + `\n[exit ${run.exit_code}]`;
  }

  // Per host, worst first: with twenty machines the two that failed are the
  // whole message, and they must not be somewhere in the middle of the scroll.
  function formatRuns(runs) {
    const bad = runs.filter(r => r.exit_code !== 0 || r.error);
    const head = `${runs.length - bad.length}/${runs.length} succeeded` +
      (bad.length ? ` — ${bad.length} failed` : '');
    const order = [...bad, ...runs.filter(r => !bad.includes(r))];
    return head + '\n\n' + order.map(r => {
      const mark = (r.exit_code === 0 && !r.error) ? 'ok' : 'FAILED';
      return `── ${bridge().hostName(r.agent_id)} — ${mark}\n` + formatRun(r).trim();
    }).join('\n\n');
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

  // ---- library ----
  //
  // Picking an entry fills the editor and nothing more: it is not saved and
  // not run, so the operator reads it, edits it and decides where it goes.
  function renderLibrary() {
    const box = $('scLibList');
    const lib = window.SCRIPT_LIBRARY || [];
    // Platforms present in the fleet first, so a Linux-only homelab is not
    // scrolling past PowerShell.
    const present = new Set(bridge().execHosts().map(h => h.os));
    const rank = e => (!e.os ? 1 : present.has(e.os) ? 0 : 2);
    // Grouped by platform within each rank: a list that alternates Windows and
    // Linux entries makes the reader re-check the badge on every row.
    const sorted = [...lib].sort((a, b) =>
      rank(a) - rank(b) || a.os.localeCompare(b.os) || a.name.localeCompare(b.name));
    const label = { linux: 'Linux', windows: 'Windows', darwin: 'macOS', '': 'Any' };

    box.innerHTML = sorted.map((e, i) =>
      `<button class="lib-item" data-i="${lib.indexOf(e)}">` +
        `<span class="lib-os">${esc(label[e.os] || e.os)}</span>` +
        `<span class="lib-name">${esc(e.name)}</span>` +
        `<span class="lib-about">${esc(e.about)}</span>` +
        (e.destructive ? '<span class="lib-warn">changes the host</span>' : '') +
      `</button>`).join('');

    box.querySelectorAll('.lib-item').forEach(b => b.onclick = () => {
      const e = lib[parseInt(b.dataset.i, 10)];
      current = null;               // a library entry is a new script, not an edit
      $('scName').value = e.name;
      $('scShell').value = e.shell;
      $('scContent').value = e.content;
      $('scOutput').textContent = 'loaded from the library — review it, then Save';
      box.classList.add('hidden');
    });
  }

  $('scLib').addEventListener('click', () => {
    const box = $('scLibList');
    const showing = !box.classList.contains('hidden');
    if (showing) { box.classList.add('hidden'); return; }
    renderLibrary();
    box.classList.remove('hidden');
  });

  function nameFor(id) { const s = scripts.find(x => x.id === id); return s ? s.name : id; }

  // A target is either a selector, which already reads as English, or an agent
  // id, which does not — "a2" tells nobody which machine ran the thing.
  function targetLabel(id) {
    if (id === 'all') return 'all online hosts';
    if (id.startsWith('tag:')) return 'tagged "' + id.slice(4) + '"';
    if (id.startsWith('os:')) return 'every ' + id.slice(3) + ' host';
    return bridge().hostName(id);
  }

  function renderSchedules(schedules) {
    const box = $('scSchedules');
    if (!schedules.length) { box.innerHTML = '<div class="muted" style="font-size:12px">no schedules</div>'; return; }
    box.innerHTML = '<table class="proc-table"><thead><tr><th>Script</th><th>Host</th><th>Cron</th><th></th></tr></thead><tbody>' +
      schedules.map(s => `<tr><td>${esc(nameFor(s.script_id))}</td><td>${esc(targetLabel(s.agent_id))}</td><td>${esc(s.cron)}</td>` +
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
      runs.map(r => {
        const ok = r.exit_code === 0 && !r.error;
        return `<tr><td>${new Date(r.started * 1000).toLocaleString()}</td><td>${esc(r.script_name)}</td>` +
          `<td>${esc(targetLabel(r.agent_id))}</td>` +
          `<td class="${ok ? 'run-ok' : 'run-bad'}">${ok ? 'ok' : 'exit ' + r.exit_code}</td>` +
          `<td>${esc(r.source)}</td></tr>`;
      }).join('') + '</tbody></table>';
  }

  function esc(s) { return String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c])); }
})();
