/* mtunes UI — vanilla JS against the embedded server's JSON API.
   Implements the claude.ai/design prototype: Library / Recipe / Materialize
   / Cards, device lens, live pre-flight, skip-as-success run reporting. */
'use strict';

const $app = document.getElementById('app');

const S = {
  screen: 'library',
  summary: null, devices: [], packs: [], views: [],
  lens: null,                      // device name or null
  owned: JSON.parse(localStorage.getItem('mtunes.owned') || '{}'),
  lensMenu: false, onlyOwned: JSON.parse(localStorage.getItem('mtunes.onlyOwned') || 'false'),
  search: '', locFilter: '',
  view: null,                      // selected recipe name
  pf: null, pfBusy: false, disabled: new Set(),
  run: { status: 'idle' }, runLog: ['[idle] no run started this session'],
  selCard: 0, locks: [], diff: null, diffBusy: false,
  toast: '',
};

const fmtB = (b) => {
  if (b >= 1024 ** 3) return (b / 1024 ** 3).toFixed(1) + ' GiB';
  if (b >= 1024 ** 2) return Math.round(b / 1024 ** 2) + ' MiB';
  return Math.round(b / 1024) + ' KiB';
};
const n = (x) => (x ?? 0).toLocaleString('en-US');
const esc = (s) => String(s ?? '').replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const api = (p, opt) => fetch(p, opt).then(r => r.json());

async function boot() {
  const [summary, devices, views] = await Promise.all([api('/api/summary'), api('/api/devices'), api('/api/views')]);
  S.summary = summary; S.devices = devices || []; S.views = views || [];
  if (!S.view && S.views.length) S.view = S.views[0].name;
  // default: every device profile in the workspace counts as "mine"
  for (const d of S.devices) if (!(d.name in S.owned)) S.owned[d.name] = true;
  await loadPacks();
  render();
  pollRun();
}

async function loadPacks() {
  const q = new URLSearchParams();
  if (S.lens) q.set('device', S.lens);
  S.packs = await api('/api/packs?' + q) || [];
}

async function loadPreflight() {
  if (!S.view) return;
  S.pfBusy = true; render();
  S.pf = await api('/api/preflight', { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ view: S.view, disabled: [...S.disabled] }) });
  S.pfBusy = false; render();
}

async function loadCards() {
  if (!S.views.length) return;
  const v = S.views[S.selCard];
  S.locks = await api('/api/locks?view=' + encodeURIComponent(v.name)) || [];
  S.diff = null; S.diffBusy = true; render();
  try { S.diff = await api('/api/diff?view=' + encodeURIComponent(v.name)); } catch (e) { S.diff = null; }
  S.diffBusy = false; render();
}

let lastLogged = -1;
async function pollRun() {
  try {
    const r = await api('/api/run');
    const prev = S.run.status;
    S.run = r;
    if (r.status === 'running' && r.count !== lastLogged && r.count > 0) {
      if (lastLogged < 0 || r.count - lastLogged >= 2000 || r.count === r.total) {
        S.runLog.push(`[materialize] ${n(r.count)} / ${n(r.total)} files`);
        lastLogged = r.count;
      }
    }
    if (r.status === 'done' && prev !== 'done') {
      S.runLog.push(`[done] ${n(r.written)} written (${n(r.resumed)} resumed) · ${ (r.skipped||[]).length } skipped`);
      if (r.lock) S.runLog.push(`[lock] ${r.lock.split('/').pop()} written`);
    }
    if (r.status === 'error' && prev !== 'error') S.runLog.push(`[error] ${r.error}`);
    S.runLog = S.runLog.slice(-8);
    if (S.screen === 'run' || r.status === 'running') render();
  } catch (e) { /* server gone; keep quiet */ }
  setTimeout(pollRun, S.run.status === 'running' ? 500 : 2500);
}

async function startRun() {
  const r = await api('/api/materialize', { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ view: S.view }) });
  if (r.error) { S.runLog.push('[refused] ' + r.error); }
  else { S.runLog = ['[start] materializing ' + S.view]; lastLogged = -1; }
  render();
}

/* ---------- render ---------- */

function render() {
  const screens = { library: renderLibrary, recipe: renderRecipe, run: renderRun, cards: renderCards };
  $app.innerHTML = `
    ${titlebar()}
    ${tabbar()}
    <div class="main">${screens[S.screen]()}</div>
    ${statusbar()}
  `;
  wire();
}

function titlebar() {
  const s = S.summary;
  const meta = s ? `${esc(s.workspace.replace(/^\/Users\/[^/]+/, '~'))} · ${s.locations} sources · ${n(s.files)} files · ${fmtB(s.bytes)}` : '…';
  return `<div class="titlebar"><span class="brand">mtunes</span><span class="meta">${meta}</span></div>`;
}

function tabbar() {
  const tabs = [['library', 'Library', '1'], ['recipe', 'Recipe', '2'], ['run', 'Materialize', '3'], ['cards', 'Cards', '4']];
  const lens = S.lens ? `
    <div class="lens-chip"><span class="dot"></span><span class="name">Lens · ${esc(S.lens)}</span>
    <span class="x" data-act="clear-lens">✕</span></div>` : '';
  return `<div class="tabbar">
    ${tabs.map(([k, label, num]) => `
      <div class="tab ${S.screen === k ? 'on' : ''}" data-act="tab" data-k="${k}">
        <span class="num">${num}</span><span class="label">${label}</span>
      </div>`).join('')}
    <div style="flex:1"></div>${lens}
  </div>`;
}

function statusbar() {
  const ann = S.summary ? `annotations: ${n(S.summary.packs_annotated)} packs known` : '';
  return `<div class="statusbar"><span>1–4 screens</span><span>L cycle lens</span><span>⌘K search</span>
    <div style="flex:1"></div><span>${ann}</span></div>`;
}

/* ---------- library ---------- */

function badgeFor(p) {
  if (p.match === 'exact') {
    if (p.samples_listed && p.files < p.samples_listed) {
      const pct = Math.round(p.files / p.samples_listed * 100);
      return `<span class="badge partial">${pct}% OF PACK</span>`;
    }
    return `<span class="badge complete">COMPLETE</span>`;
  }
  if (p.match === 'partial') return `<span class="badge partial">${Math.round(p.match_fraction * 100)}% OF PACK</span>`;
  return '';
}

const artHue = (s) => { let h = 0; for (const c of s) h = (h * 31 + c.charCodeAt(0)) % 360; return h; };

function renderLibrary() {
  const q = S.search.toLowerCase();
  const rows = S.packs.filter(p =>
    (!S.locFilter || p.location === S.locFilter) &&
    (!q || p.name.toLowerCase().includes(q) || (p.provider || '').toLowerCase().includes(q)));

  let sum;
  if (S.lens) {
    const ef = rows.reduce((a, p) => a + (p.eligible || 0), 0), tf = rows.reduce((a, p) => a + p.files, 0);
    const em = rows.reduce((a, p) => a + (p.converted_bytes || 0), 0);
    sum = `${n(ef)} of ${n(tf)} files eligible · ${fmtB(em)} after transcode`;
  } else {
    sum = `${rows.length} packs · ${n(rows.reduce((a, p) => a + p.files, 0))} files · ${fmtB(rows.reduce((a, p) => a + p.bytes, 0))}`;
  }

  const locs = [...new Set(S.packs.map(p => p.location))];
  const ownedDevs = S.devices.filter(d => S.owned[d.name]);
  const otherDevs = S.devices.filter(d => !S.owned[d.name]);
  const devRow = (d) => `
    <div class="row ${S.lens === d.name ? 'picked' : ''}" data-act="pick-lens" data-d="${esc(d.name)}">
      <div style="flex:1;min-width:0;display:flex;flex-direction:column;gap:1px">
        <span class="dname">${esc(d.name)}</span><span class="dsub">${esc(d.sub)}</span>
      </div>
      <span class="star ${S.owned[d.name] ? 'owned' : ''}" data-act="star" data-d="${esc(d.name)}" title="${S.owned[d.name] ? 'in my rig — click to unflag' : 'flag as mine'}">${S.owned[d.name] ? '★' : '☆'}</span>
    </div>`;

  const menu = !S.lensMenu ? '' : `
    <div class="menu-veil" data-act="close-menu"></div>
    <div class="lens-menu">
      <div class="row ${S.lens ? '' : 'picked'}" data-act="no-lens">
        <span class="dname" style="flex:1">No lens</span>
        <span style="font:400 10px var(--mono);color:var(--fg-faint)">whole library</span>
      </div>
      <div class="sect">MY DEVICES</div>
      ${ownedDevs.map(devRow).join('') || '<div style="font:400 10px var(--mono);color:var(--fg-faint);padding:4px 9px">none flagged</div>'}
      ${!S.onlyOwned && otherDevs.length ? `<div class="sect" style="border-top:1px solid var(--bord);margin-top:5px">OTHER DEVICES</div>${otherDevs.map(devRow).join('')}` : ''}
      <div class="foot" data-act="only-owned">
        <span class="ck ${S.onlyOwned ? 'on' : ''}">${S.onlyOwned ? '✓' : ''}</span>
        <span style="font:500 11px var(--sans);color:var(--fg-dim)">Only show my devices</span>
      </div>
    </div>`;

  return `
    <div class="screen-head">
      <h1>Library</h1><span class="sum">${sum}</span>
      <div style="flex:1"></div>
      <div class="search">⌕ <input id="search" placeholder="Search packs…" value="${esc(S.search)}"><span class="kbd">⌘K</span></div>
      <div style="position:relative">
        <div class="lens-btn ${S.lens ? 'on' : ''}" data-act="toggle-menu">
          <span class="dot"></span><span class="label">Lens · ${S.lens ? esc(S.lens) : 'off'}</span><span class="caret">▾</span>
        </div>
        ${menu}
      </div>
    </div>
    <div class="filterbar">
      <span class="chip ${!S.locFilter ? 'active' : ''}" data-act="loc" data-l="">All locations</span>
      ${locs.map(l => `<span class="chip ${S.locFilter === l ? 'active' : ''}" data-act="loc" data-l="${esc(l)}">${esc(l)}</span>`).join('')}
      <div style="flex:1"></div>
      ${S.lens ? '<span class="lens-note">eligible counts · converted sizes</span>' : ''}
    </div>
    <div class="grid">
      ${rows.map(p => {
        const art = p.image
          ? `<div class="art"><img src="${esc(p.image)}" loading="lazy" onerror="this.parentNode.classList.add('none');this.remove();this.textContent='/'"></div>`
          : p.slug
            ? `<div class="art" style="background:linear-gradient(135deg,hsl(${artHue(p.name)},38%,42%),hsl(${artHue(p.name)},45%,24%))">${esc(p.name[0] || '?')}</div>`
            : `<div class="art none">/</div>`;
        const vendor = p.provider || p.location;
        const stats = S.lens
          ? `<div class="stats lens">${n(p.eligible)} <span class="of">of ${n(p.files)}</span> · ${fmtB(p.converted_bytes || 0)}</div>`
          : `<div class="stats">${n(p.files)} files · ${fmtB(p.bytes)}</div>`;
        const link = p.url ? `<a class="link" href="${esc(p.url)}" target="_blank" title="product page">↗</a>` : '';
        return `<div class="pack">${art}
          <div class="body">
            <div class="name" title="${esc(p.dir)}">${esc(p.name)}</div>
            <div class="vline"><span class="vendor">${esc(vendor)}</span>${badgeFor(p)}</div>
            ${stats}
          </div>${link}</div>`;
      }).join('')}
    </div>`;
}

/* ---------- recipe ---------- */

function renderRecipe() {
  if (!S.pf && !S.pfBusy) loadPreflight();
  const pf = S.pf;
  const viewOpts = S.views.map(v => `<option ${v.name === S.view ? 'selected' : ''}>${esc(v.name)}</option>`).join('');
  const head = `
    <div style="display:flex;align-items:center;gap:10px;margin-bottom:4px">
      <span style="font:600 14px var(--sans)">Recipe</span>
      <select id="view-pick" style="font:500 11.5px var(--mono);color:var(--fg-dim);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:3px 8px">${viewOpts}</select>
      ${pf ? `<span class="rtag dev">${esc(pf.device)}</span><span class="rtag" style="cursor:default">${esc(pf.storage)}</span>` : ''}
    </div>
    <div style="font:400 11px var(--sans);color:var(--fg-faint);margin-bottom:2px">Include rules — toggle to preview; the recipe file is never modified from here</div>`;

  if (!pf) return `<div class="recipe-grid"><div class="recipe-left">${head}
    <div style="font:400 11px var(--mono);color:var(--fg-faint);padding:24px 4px">running pre-flight…</div></div>
    <div class="preflight"></div></div>`;

  const rules = pf.rules.map((r, i) => {
    const on = r.enabled;
    const name = r.as || (r.glob.split('/')[0].replace(/[*{}]/g, '') || r.location);
    return `<div class="rule ${on ? '' : 'off'}" data-act="rule" data-i="${i}">
      <span class="ck ${on ? 'on' : ''}">${on ? '✓' : ''}</span>
      <div class="body"><span class="rname">${esc(name)} <span style="font:400 10px var(--mono);color:var(--fg-faint)">${esc(r.location)}</span></span>
      <span class="rpath">${esc(r.glob)}</span></div>
      <span class="match">${n(r.files)} files · ${fmtB(r.converted_bytes)}</span>
    </div>`;
  }).join('');

  const p = pf.plan;
  let right = '';
  if (p) {
    const cap = p.storage.capacity_bytes, usable = p.usable_bytes ?? 0, onDisk = p.total_on_disk ?? 0;
    const over = onDisk - usable;
    const fits = p.fits;
    const warnings = p.warnings || [], errors = p.errors || [];
    const fitLabel = !fits ? "WON'T FIT" : (warnings.length || errors.length) ? `FITS · ${warnings.length + errors.length} issues` : 'FITS · clean';
    const fitColor = !fits ? 'var(--err)' : (warnings.length || errors.length) ? 'var(--warn)' : 'var(--green)';
    const fillPct = Math.min(onDisk, usable) / cap * 100;
    const usablePct = usable / cap * 100, reservePct = 100 - usablePct;
    const ovPct = over > 0 ? Math.min(over / cap * 100, reservePct) : 0;
    const issues = [
      ...errors.map(t => `<div class="issue err"><span class="k">ERROR</span><span class="t">${esc(t)}</span></div>`),
      ...warnings.map(t => `<div class="issue warn"><span class="k">WARN</span><span class="t">${esc(t)}</span></div>`),
    ].join('') || `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:6px 2px">0 issues — clean pre-flight</div>`;
    right = `
      <div style="display:flex;align-items:baseline;gap:10px;margin-bottom:12px">
        <span style="font:600 12.5px var(--sans)">Pre-flight</span>
        <span style="font:400 11px var(--mono);color:var(--fg-faint)">${esc(pf.storage)} · ${fmtB(cap)}</span>
        <div style="flex:1"></div>
        <span style="font:700 12px var(--mono);color:${fitColor}">${fitLabel}${S.pfBusy ? ' ·…' : ''}</span>
      </div>
      <div class="meter">
        <div class="fill" style="width:${fillPct}%"></div>
        ${over > 0 ? `<div class="over" style="left:${usablePct}%;width:${ovPct}%"></div>` : ''}
        <div class="reserve" style="width:${reservePct}%"></div>
        <div class="mark" style="left:${usablePct}%"></div>
      </div>
      <div class="legend">
        <span style="color:var(--amber)">■ selection ${fmtB(onDisk)} · ${n(S.pf.files ?? 0)} files</span>
        <span style="color:var(--fg-dim)">usable ${fmtB(usable)}</span>
        <span style="color:var(--fg-faint)">▨ reserve ${fmtB(cap - usable)}</span>
        <div style="flex:1"></div>
        <span style="color:${over > 0 ? 'var(--err)' : 'var(--green)'}">${over > 0 ? fmtB(over) + ' over' : fmtB(usable - onDisk) + ' free'}</span>
      </div>
      <div>${issues}</div>
      <div class="mat-btn ${!fits || errors.length ? 'blocked' : ''}" data-act="go-run">MATERIALIZE — ${n(S.pf.files ?? 0)} FILES</div>
      <div style="font:400 10px var(--mono);color:var(--fg-faint);margin-top:8px;text-align:center">writes to the recipe's target with the full rule set — toggles here are preview only</div>`;
  } else {
    right = `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:24px 4px">no rules enabled</div>`;
  }

  return `<div class="recipe-grid">
    <div class="recipe-left">${head}<div class="rules">${rules}
      <div class="add-rule">+ add rule — edit ${esc(S.view)}.toml (UI editing is next)</div></div></div>
    <div class="preflight">${right}</div>
  </div>`;
}

/* ---------- run ---------- */

function renderRun() {
  const r = S.run;
  const running = r.status === 'running', isDone = r.status === 'done', isErr = r.status === 'error';
  const pct = r.total ? (r.count / r.total * 100) : 0;
  const sub = running ? 'Running — resumable: anything already at the target is verified and reused.'
    : isDone ? `Run finished.${(r.skipped || []).length ? ' Skipped files are listed below — the run continued past each one.' : ''}`
    : isErr ? 'Run aborted — the failure was systemic, nothing partial was locked.'
    : 'Nothing is written until you start. Resumable: files already at the target are verified and skipped.';

  const skips = (r.skipped || []);
  const skipPanel = isDone && skips.length ? `
    <div class="skip-panel">
      <div style="display:flex;align-items:baseline;gap:8px;margin-bottom:8px">
        <span style="font:600 11.5px var(--sans)">${skips.length} skipped files</span>
        <span style="font:400 10.5px var(--sans);color:var(--fg-faint)">the run continued past each — re-run when the cause is fixed; resume makes it cheap</span>
      </div>
      ${skips.slice(0, 8).map(s => `<div class="skip-row"><span style="color:var(--fg-num);flex:1;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(s.OutRel)}</span><span style="color:var(--warn);white-space:nowrap">${esc(s.Err.slice(0, 60))}</span></div>`).join('')}
      ${skips.length > 8 ? `<div style="font:400 10.5px var(--mono);color:var(--fg-faint);padding-top:5px;border-top:1px solid var(--bord)">… and ${skips.length - 8} more</div>` : ''}
    </div>` : '';

  return `<div class="run-wrap">
    <div style="display:flex;align-items:baseline;gap:10px;margin-bottom:4px">
      <span style="font:600 14px var(--sans)">Materialize</span>
      <span style="font:500 11px var(--mono);color:var(--fg-faint)">${esc(r.view || S.view || '')} · lock is written only on success</span>
    </div>
    <div style="font:400 11px var(--sans);color:var(--fg-faint);margin-bottom:16px">${sub}</div>
    <div class="runbar"><div class="fill" style="width:${pct}%"></div></div>
    <div style="display:flex;font:500 11px var(--mono);color:var(--fg-dim);margin-bottom:16px">
      <span>${n(r.count)} of ${n(r.total)} files</span><div style="flex:1"></div>
      <span style="color:var(--fg-faint)">${running ? 'running · ' + elapsed(r.started) : isDone ? 'finished' : ''}</span>
    </div>
    <div class="stat-grid">
      <div class="stat"><div class="v">${n(isDone ? r.written - r.resumed : r.count)}</div><div class="l">${isDone ? 'rendered' : 'processed'}</div></div>
      <div class="stat"><div class="v" style="color:var(--green)">${isDone ? n(r.resumed) : '—'}</div><div class="l">already present — reused</div></div>
      <div class="stat"><div class="v" style="color:${skips.length ? 'var(--warn)' : 'var(--fg)'}">${isDone ? skips.length : '—'}</div><div class="l">failed — skipped, run continued</div></div>
      <div class="stat"><div class="v" style="font-size:11px;padding-top:4px">${isDone && r.lock ? esc(r.lock.split('/').pop()) : '—'}</div><div class="l">lockfile</div></div>
    </div>
    ${!running ? `<div class="go-btn" data-act="start-run">START RUN — ${esc(S.view || '')}</div>` : ''}
    ${isDone ? `<div class="done-band"><span style="font:700 12px var(--mono);color:var(--green)">DONE</span>
      <span style="font:500 12px var(--sans);color:#b8e0c8">${n(r.written)} of ${n(r.total)} written — lock recorded</span></div>` : ''}
    ${isErr ? `<div class="err-band"><span style="font:700 12px var(--mono);color:var(--err)">ABORTED</span>
      <span style="font:500 12px var(--sans);color:#e8a99f">${esc(r.error || '')}</span></div>` : ''}
    ${skipPanel}
    <div class="logpane">${S.runLog.map(l => `<div class="logline">${esc(l)}</div>`).join('')}</div>
  </div>`;
}

const elapsed = (t) => t ? Math.round((Date.now() - new Date(t).getTime()) / 1000) + 's' : '';

/* ---------- cards ---------- */

function renderCards() {
  if (!S.locks.length && !S.diffBusy && S.views.length) loadCards();
  const v = S.views[S.selCard] || {};
  const items = S.views.map((vw, i) => `
    <div class="card-item ${i === S.selCard ? 'sel' : ''}" data-act="pick-card" data-i="${i}">
      <div style="display:flex;align-items:center;gap:8px">
        <span style="font:600 12px var(--mono)">${esc(vw.name)}</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">${esc(vw.device)}</span>
      </div>
      <span style="font:400 10px var(--mono);color:var(--fg-faint)">${vw.rules} rules · ${esc(vw.target || 'no target set')}</span>
    </div>`).join('');

  let stale = '';
  if (S.diffBusy) stale = `<div style="font:400 11px var(--mono);color:var(--fg-faint);margin:10px 0">computing staleness…</div>`;
  else if (S.diff && S.diff.diff) {
    const d = S.diff.diff;
    const counts = [(d.added || []).length, (d.deselected || []).length, (d.gone_from_source || []).length, (d.content_drift || []).length, (d.new_transform || []).length];
    const total = counts.reduce((a, b) => a + b, 0);
    stale = S.diff.in_sync
      ? `<div style="display:flex;align-items:center;gap:8px;background:rgba(87,196,138,.08);border:1px solid rgba(87,196,138,.3);border-radius:4px;padding:7px 10px;margin:10px 0 4px">
          <span style="font:700 10px var(--mono);color:var(--green)">IN SYNC</span>
          <span style="font:400 11.5px var(--sans);color:#b8e0c8">newest lock matches the recipe today — nothing would change</span></div>`
      : `<div class="stale-band"><span style="font:700 10px var(--mono);color:var(--warn)">STALE</span>
          <span style="font:400 11.5px var(--sans);color:#d9c68e">${n(total)} entries would change — ${n(counts[0])} added, ${n(counts[1])} deselected, ${n(counts[2])} gone from source, ${n(counts[3])} content drift, ${n(counts[4])} re-transcode</span></div>`;
  }

  const hist = S.locks.map((l) => `
    <div class="hist-row">
      <div class="hist-rail"><span class="hist-dot ${l.newest ? 'cur' : ''}"></span><span class="hist-line"></span></div>
      <span style="font:600 11.5px var(--mono);color:${l.newest ? 'var(--green)' : 'var(--fg-dim)'};flex:none">${esc(l.file.slice(0, 16))}</span>
      <div style="min-width:0;flex:1;display:flex;flex-direction:column;gap:2px">
        <span style="font:500 12px var(--sans)">${esc(l.view)}.toml</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">${esc(l.created)} · ${n(l.files)} files · ${fmtB(l.bytes)}</span>
      </div>
      ${l.newest ? '<span class="oncard">NEWEST</span>' : ''}
      <span class="restore-btn" data-act="restore" data-f="${esc(l.file)}" title="copies the restore command">restore</span>
    </div>`).join('') || `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:16px 4px">no locks yet — this view has never been materialized</div>`;

  return `<div class="cards-grid">
    <div class="cards-left">
      <div style="font:600 14px var(--sans);margin-bottom:12px;padding:0 4px">Cards</div>${items}
    </div>
    <div style="padding:14px 18px">
      <div style="display:flex;align-items:baseline;gap:10px;margin-bottom:4px">
        <span style="font:600 14px var(--sans)">${esc(v.name || '')}</span>
        <span style="font:400 11px var(--mono);color:var(--fg-faint)">lock history — any entry rebuilds the card byte-for-byte</span>
      </div>
      ${stale}
      <div style="margin-top:12px">${hist}</div>
      ${S.toast ? `<div style="font:500 11px var(--mono);color:var(--green);margin-top:10px">${esc(S.toast)}</div>` : ''}
    </div>
  </div>`;
}

/* ---------- events ---------- */

function wire() {
  $app.querySelectorAll('[data-act]').forEach(el => {
    el.addEventListener('click', (e) => {
      const act = el.dataset.act;
      if (act === 'tab') { S.screen = el.dataset.k; if (S.screen === 'cards') { S.locks = []; } render(); }
      if (act === 'clear-lens') { S.lens = null; loadPacks().then(render); }
      if (act === 'toggle-menu') { S.lensMenu = !S.lensMenu; render(); }
      if (act === 'close-menu') { S.lensMenu = false; render(); }
      if (act === 'no-lens') { S.lens = null; S.lensMenu = false; loadPacks().then(render); }
      if (act === 'pick-lens') { S.lens = el.dataset.d; S.lensMenu = false; loadPacks().then(render); }
      if (act === 'star') {
        e.stopPropagation();
        S.owned[el.dataset.d] = !S.owned[el.dataset.d];
        localStorage.setItem('mtunes.owned', JSON.stringify(S.owned)); render();
      }
      if (act === 'only-owned') {
        S.onlyOwned = !S.onlyOwned;
        localStorage.setItem('mtunes.onlyOwned', JSON.stringify(S.onlyOwned)); render();
      }
      if (act === 'loc') { S.locFilter = el.dataset.l; render(); }
      if (act === 'rule') {
        const i = +el.dataset.i;
        S.disabled.has(i) ? S.disabled.delete(i) : S.disabled.add(i);
        loadPreflight();
      }
      if (act === 'go-run') { if (!el.classList.contains('blocked')) { S.screen = 'run'; render(); } }
      if (act === 'start-run') startRun();
      if (act === 'pick-card') { S.selCard = +el.dataset.i; loadCards(); }
      if (act === 'restore') {
        const cmd = `mtunes restore ${S.views[S.selCard].name} --to <target>  # lock: ${el.dataset.f}`;
        navigator.clipboard?.writeText(cmd);
        S.toast = 'restore command copied — run it in a terminal (target left to you on purpose)';
        render(); setTimeout(() => { S.toast = ''; render(); }, 4000);
      }
    });
  });
  const search = document.getElementById('search');
  if (search) {
    search.addEventListener('input', () => { S.search = search.value; renderPreservingSearch(); });
  }
  const vp = document.getElementById('view-pick');
  if (vp) vp.addEventListener('change', () => { S.view = vp.value; S.disabled = new Set(); S.pf = null; loadPreflight(); });
}

function renderPreservingSearch() {
  render();
  const s = document.getElementById('search');
  if (s) { s.focus(); s.setSelectionRange(s.value.length, s.value.length); }
}

window.addEventListener('keydown', (e) => {
  if (e.target && /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)) {
    if (e.key === 'Escape') e.target.blur();
    return;
  }
  const map = { 1: 'library', 2: 'recipe', 3: 'run', 4: 'cards' };
  if (map[e.key]) { S.screen = map[e.key]; if (S.screen === 'cards') S.locks = []; render(); }
  if (e.key === 'l' || e.key === 'L') {
    const order = [null, ...S.devices.filter(d => S.owned[d.name]).map(d => d.name)];
    const i = order.indexOf(S.lens);
    S.lens = order[(i + 1) % order.length];
    loadPacks().then(render);
  }
  if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
    e.preventDefault(); S.screen = 'library'; render();
    document.getElementById('search')?.focus();
  }
});

boot();
