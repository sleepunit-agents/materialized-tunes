/* mtunes UI — vanilla JS against the embedded server's JSON API.
   Implements the claude.ai/design prototype: Library / Recipe / Materialize
   / Cards, device lens, live pre-flight, skip-as-success run reporting. */
'use strict';

const $app = document.getElementById('app');
const blurbCache = {};
const audio = new Audio();
audio.addEventListener('ended', () => { if (S.player) { S.player.playing = false; render(); } });
audio.addEventListener('timeupdate', () => {
  const f = document.getElementById('wavefill');
  if (f && audio.duration) f.style.width = (audio.currentTime / audio.duration * 100) + '%';
});

const S = {
  screen: 'library',
  summary: null, devices: [], packs: [], views: [],
  lens: null,                      // device name or null
  owned: JSON.parse(localStorage.getItem('mtunes.owned') || '{}'),
  lensMenu: false, onlyOwned: JSON.parse(localStorage.getItem('mtunes.onlyOwned') || 'false'),
  search: '', locFilter: '',
  // discover: the registry with the ownership filter flipped. Library is
  // always the default on load; obtainable-only is always on again next
  // session — both deliberate (SPEC §11.6).
  discover: false, obtainable: true, disc: null, discBusy: false,
  view: null,                      // selected recipe name
  pf: null, pfBusy: false, pfProgress: null, disabled: new Set(),
  pl: { tab: 'queues', kind: '', q: null, busy: false, sel: null, files: null, file: null, tree: null, prefix: '', lex: null, form: null, radius: null, msg: '', local: null, rec: null, recBusy: false },
  rOpen: new Set(), rFilter: '', rModel: null,  // Recipe screen: expanded vendor rows, filter, the group model clicks act on
  run: { status: 'idle' }, runLog: ['[idle] no run started this session'],
  selCard: 0, locks: [], diff: null, diffBusy: false,
  locations: [], suggestions: [], scans: {}, addForm: null,
  storages: [], presets: [], volumes: [], devForm: null, stoForm: null, newRecipe: null, addTo: null, dirPick: null, renaming: false,
  packOpen: null, pd: null, pdFolder: '', pdDesc: '', descOpen: false,
  // cross-pack sample filters: any of these set switches the Library from
  // pack cards to sample rows. Packs stay the default unit; this is the
  // "the vocal pack also has a top loop in it" escape hatch.
  fInst: '', fKey: '', fBpm: '', fCat: '',
  samples: null, samplesBusy: false,
  player: null,  // {path, name, dur, playing}
  toast: '',
  upd: null, updBusy: false, updMsg: '',  // app self-update state
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
  // Wails injects window.runtime; the browser build never has it
  if (window.runtime) {
    document.body.classList.add('in-wails');
    // the traffic-light inset is a macOS thing; Windows/Linux keep the OS title bar
    if (/Mac/i.test(navigator.platform)) document.body.classList.add('in-wails-mac');
    // native Recipes menu → jump to that recipe
    window.runtime.EventsOn('open-view', (name) => {
      stopPlayback();
      S.packOpen = null; S.pd = null;
      S.view = name; S.disabled = new Set(); S.pf = null;
      S.screen = 'recipe';
      loadPreflight();
    });
    // native Go menu (⌘1–4) → main screens
    window.runtime.EventsOn('open-screen', (k) => {
      stopPlayback();
      S.packOpen = null; S.pd = null;
      S.screen = k;
      if (k === 'cards') S.locks = [];
      render();
    });
  }
  const [summary, devices, views] = await Promise.all([api('/api/summary'), api('/api/devices'), api('/api/views')]);
  S.summary = summary; S.devices = devices || []; S.views = views || [];
  if (!S.view && S.views.length) S.view = S.views[0].name;
  // default: every device profile in the workspace counts as "mine"
  for (const d of S.devices) if (!(d.name in S.owned)) S.owned[d.name] = true;
  await loadPacks();
  render();
  pollRun();
  pollScans();
  pollUpdate();
}

/* ---------- app self-update ----------
   The exe tracks the rolling "latest" release the way annotations track
   their repo: poll for a newer build, and one click installs it and
   relaunches. What used to be "download the exe again, replace it, reopen"
   is now the update chip in the tab bar. */

async function pollUpdate() {
  try {
    const u = await api('/api/update');
    const was = S.upd && S.upd.available;
    S.upd = u;
    if ((u && u.available) !== was) render();
  } catch (e) { /* server gone; keep quiet */ }
  setTimeout(pollUpdate, 5 * 60 * 1000);
}

async function applyUpdate() {
  S.updBusy = true; S.updMsg = 'downloading new build…'; render();
  try {
    const r = await api('/api/update', { method: 'POST' });
    if (r.error) { S.updBusy = false; S.updMsg = r.error; }
    else S.updMsg = 'restarting…'; // the process swaps out under us now
  } catch (e) {
    // the desktop app relaunches before the response can land — that's success
    S.updMsg = 'restarting…';
  }
  render();
}

async function loadPacks() {
  const q = new URLSearchParams();
  if (S.lens) q.set('device', S.lens);
  S.packs = await api('/api/packs?' + q) || [];
}

async function loadDiscover() {
  S.discBusy = true; render();
  try { S.disc = await api('/api/discover') || []; } catch (e) { S.disc = []; }
  S.discBusy = false; render();
}

// The plan is a run: POST /api/plan answers from the cached artifact when
// the recipe, toggles and library are unchanged, else reports the build's
// stage until it lands. A newer request supersedes an older poll.
let pfSeq = 0;
async function loadPreflight() {
  if (!S.view) return;
  const seq = ++pfSeq;
  S.pfBusy = true; S.pfProgress = null; render();
  for (;;) {
    const r = await api('/api/plan', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ view: S.view, disabled: [...S.disabled] }) });
    if (seq !== pfSeq) return;
    if (r && r.status === 'running') {
      S.pfProgress = r; render();
      await new Promise(res => setTimeout(res, 400));
      continue;
    }
    S.pf = r; S.pfProgress = null;
    break;
  }
  S.pfBusy = false; render();
}

function pfProgressLabel() {
  const p = S.pfProgress;
  if (!p) return 'planning…';
  const stage = p.stage || 'planning';
  return p.total > 1 ? `${stage} · ${n(p.count)} / ${n(p.total)}` : stage + '…';
}

async function openPack(p) {
  S.packOpen = p; S.pd = null; S.pdFolder = ''; S.pdDesc = ''; S.descOpen = false;
  render();
  const q = new URLSearchParams({ location: p.location, dir: p.dir });
  if (S.lens) q.set('device', S.lens);
  const d = await api('/api/pack?' + q);
  S.pd = d;
  const canon = (d.folders || []).find(f => /^(WAV|Samples|AUDIO)$/i.test(f.name));
  if (canon) { S.pdFolder = canon.name; await loadPdFolder(); }
  // land the user on sounds, not scaffolding: descend into the largest
  // child until this level actually has files
  for (let hops = 0; hops < 8 && !(S.pd.files || []).length && (S.pd.folders || []).length; hops++) {
    const big = S.pd.folders.reduce((a, b) => b.count > a.count ? b : a);
    S.pdFolder = S.pdFolder ? S.pdFolder + '/' + big.name : big.name;
    await loadPdFolder();
  }
  render();
  if (p.description) { S.pdDesc = p.description; render(); return; } // inline (discontinued packs)
  const bk = p.blurb || p.url; // in-archive About.md wins over a product page
  if (bk) {
    if (!blurbCache[bk]) { try { blurbCache[bk] = await api('/api/blurb?u=' + encodeURIComponent(bk)); } catch (e) { blurbCache[bk] = {}; } }
    S.pdDesc = blurbCache[bk].description || '';
    render();
  }
}

async function loadPdFolder() {
  const p = S.packOpen;
  const q = new URLSearchParams({ location: p.location, dir: p.dir, folder: S.pdFolder });
  if (S.lens) q.set('device', S.lens);
  const d = await api('/api/pack?' + q);
  S.pd.folders = d.folders; S.pd.files = d.files; S.pd.total = d.total; S.pd.shown = d.shown;
}

function stopPlayback() {
  if (!S.player) return;
  audio.pause();
  audio.removeAttribute('src');
  S.player = null;
}

// location defaults to the open pack's — cross-pack sample rows pass their own.
function playFile(path, name, dur, location) {
  if (S.player && S.player.path === path) {
    if (S.player.playing) { audio.pause(); S.player.playing = false; }
    else { audio.play(); S.player.playing = true; }
    render(); return;
  }
  const loc = location || S.packOpen?.location;
  if (!loc) return;
  audio.src = '/api/preview?' + new URLSearchParams({ location: loc, path });
  audio.play();
  S.player = { path, name, dur, playing: true };
  render();
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
        S.runLog.push(`[${r.verb || 'materialize'}] ${n(r.count)} / ${n(r.total)} files`);
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

async function startMigrate() {
  const r = await api('/api/migrate', { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ view: S.view }) });
  if (r.error) { S.runLog.push('[refused] ' + r.error); }
  else { S.runLog = ['[start] migrating ' + S.view + ' — renaming into the new layout, nothing re-rendered']; lastLogged = -1; }
  render();
}

/* ---------- render ---------- */

function render() {
  const screens = { library: renderLibrary, recipe: renderRecipe, plan: renderPlan, run: renderRun, cards: renderCards, sources: renderSources };
  $app.innerHTML = `
    ${titlebar()}
    ${tabbar()}
    <div class="main">${screens[S.screen]()}</div>
    ${addToPicker()}
    ${dirPicker()}
    ${statusbar()}
  `;
  wire();
  document.querySelector('.pd-row.playing')?.scrollIntoView({ block: 'nearest' });
}

// groupRule builds the single [[include]] that covers every pack in a
// head-bar group (a location, or a vendor under vendor-dirs): the
// "all of splice" button. Same layout as adding each pack by hand —
// provider packs land under LOCATION/<pack>/ — but one rule, not forty.
// Returns null when the group's packs don't share a root (mixed
// locations), in which case the caller adds them one by one.
function groupRule(group) {
  const packs = S.packs.filter(p => packGroup(p) === group);
  if (!packs.length) return null;
  const loc = packs[0].location;
  if (packs.some(p => p.location !== loc)) return null;
  const as = packs.some(p => p.provider) ? loc.toUpperCase() : '';
  const tops = new Set(packs.map(p => p.dir.split('/')[0]));
  const nested = packs.every(p => p.dir.includes('/'));
  // A group whose packs sit at the top of their location IS that location
  // (the chip reads "Splice", the location is "splice" — don't compare
  // names, look at the layout). Whole-location rule, and it REPLACES any
  // narrower rules for the location so new packs just fall in.
  if (!nested) return { location: loc, glob: '**', as, label: `all of ${group} (${packs.length} packs)`, replace: true };
  if (tops.size === 1) {
    const top = [...tops][0];
    return { location: loc, glob: `${top}/**`, as: as ? `${as}/${top}` : '', label: `all of ${group} (${packs.length} packs)` };
  }
  return null;
}

// A rule's static root: the path before its first metacharacter, with a
// trailing slash. "" for a whole-location "**".
function globRoot(glob) {
  const out = [];
  for (const seg of String(glob).split('/')) {
    if (/[*?[{]/.test(seg)) break;
    out.push(seg);
  }
  return out.length ? out.join('/') + '/' : '';
}

// How a rule relates to a pack: 'all' when its root sits at or above the
// pack (a whole-vendor or whole-location rule takes the pack entire),
// 'part' when it aims INSIDE the pack (someone added one folder of it).
function ruleCovers(rule, pack) {
  if (rule.location !== pack.location) return null;
  const root = globRoot(rule.glob), dir = pack.dir + '/';
  if (root === '' || dir.startsWith(root)) return 'all';
  if (root.startsWith(dir)) return 'part';
  return null;
}

// The Recipe screen's model: the library grouped the way the Library groups
// it — by vendor, falling back to location — with each group told what the
// recipe's rules currently do to it.
//
// [[include]] blocks are an implementation detail of this picker. Two
// hundred per-pack rules and one whole-vendor rule are the same row here;
// `tidy` turns the first into the second without changing the selection.
function recipeGroups(pf) {
  const rules = pf.rules || [], cuts = new Set(pf.excludes || []);
  const claimed = new Set(), byKey = new Map();
  for (const p of S.packs) {
    const key = packGroup(p);
    let g = byKey.get(key);
    if (!g) { g = { key, location: p.location, packs: [], rules: new Set(), mixedLoc: false }; byKey.set(key, g); }
    if (p.location !== g.location) g.mixedLoc = true;
    const dir = p.dir + '/';
    const e = { p, own: [], wide: [], cut: cuts.has(p.dir + '/**'), whole: false };
    rules.forEach((r, i) => {
      const c = ruleCovers(r, p);
      if (!c) return;
      claimed.add(i); g.rules.add(i);
      if (globRoot(r.glob).length >= dir.length) e.own.push(i); else e.wide.push(i);
      if (c === 'all') e.whole = true;
    });
    e.in = (e.own.length || e.wide.length) > 0 && !e.cut;
    e.some = e.in && !e.whole;   // only a folder inside the pack is in
    g.packs.push(e);
  }
  // A rule that touches two groups (a location-wide "**" over a location
  // holding several vendors) can't be deleted on one group's say-so.
  const groups = [...byKey.values()];
  const touches = new Map();
  for (const g of groups) for (const i of g.rules) touches.set(i, (touches.get(i) || 0) + 1);
  for (const g of groups) {
    g.exclusive = [...g.rules].every(i => touches.get(i) === 1);
    g.rules = [...g.rules].sort((a, b) => a - b);
    g.packs.sort((a, b) => a.p.name.localeCompare(b.p.name, undefined, { numeric: true, sensitivity: 'base' }));
    g.in = g.packs.filter(e => e.in).length;
    g.state = !g.in ? 'none' : (g.in === g.packs.length && !g.packs.some(e => e.some)) ? 'all' : 'partial';
    g.files = g.packs.reduce((a, e) => a + (e.in ? e.p.files : 0), 0);
    g.bytes = g.packs.reduce((a, e) => a + (e.in ? e.p.bytes : 0), 0);
    g.groupRule = groupRule(g.key);
    // Fully in, on more than one rule: the row this screen exists to collapse.
    g.collapsible = g.state === 'all' && g.exclusive && !!g.groupRule && g.rules.length > 1;
  }
  const rank = { all: 0, partial: 1, none: 2 };
  groups.sort((a, b) => rank[a.state] - rank[b.state] || a.key.localeCompare(b.key, undefined, { sensitivity: 'base' }));
  const extras = rules.map((r, i) => ({ r, i })).filter(x => !claimed.has(x.i));
  return { groups, extras, cuts };
}

function addToPicker() {
  const a = S.addTo;
  if (!a) return '';
  return `<div class="menu-veil" data-act="add-to-cancel" style="background:rgba(0,0,0,.45)"></div>
    <div style="position:fixed;z-index:7;left:50%;top:34%;transform:translateX(-50%);width:520px;background:#16191c;border:1px solid #2f353b;border-radius:8px;box-shadow:0 16px 48px rgba(0,0,0,.6);padding:16px;display:flex;flex-direction:column;gap:10px">
      <span style="font:600 13px var(--sans)">Add to recipe</span>
      <div style="font:400 11px var(--mono);color:var(--fg-dim);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:7px 9px;word-break:break-all">${esc(a.location)} : ${esc(a.glob)}${a.as ? ` → ${esc(a.as)}/` : ''}</div>
      ${a.replace !== undefined ? `<label style="display:flex;gap:6px;align-items:center;font:400 11px var(--sans);color:var(--fg-dim);cursor:pointer"><input type="checkbox" id="at-replace" ${a.replace ? 'checked' : ''}> replace every existing <b>${esc(a.location)}</b> rule in the recipe with this one — packs you add later just fall in</label>` : ''}
      <div style="display:flex;gap:8px;align-items:center">
        ${sel('at-view', S.views.map(v => [v.name, `${v.name} — ${v.device}`]), S.view)}
        <span class="restore-btn" data-act="add-to-cancel">cancel</span>
        <span class="mat-btn" style="margin:0;padding:6px 16px;font-size:11px" data-act="add-to-save">add rule</span>
      </div>
      <div style="font:400 10px var(--sans);color:var(--fg-faint)">Appends an [[include]] block to the recipe's TOML — your comments and hand edits stay put.</div>
    </div>`;
}

/* ---------- folder picker ----------
   A browser tab cannot hand us a real path from <input type=file>, so the
   picker is ours: /api/dirs walks the host filesystem one level at a time.
   dirPick = { path, data, onPick, title }. The path box stays editable —
   typing is still the fastest way in when you know where you are going. */
function dirPicker() {
  const d = S.dirPick;
  if (!d) return '';
  const data = d.data || { entries: [], roots: [] };
  const rows = data.entries.map(e => `<div class="dp-row" data-act="dp-go" data-path="${esc(e.path)}">📁 ${esc(e.name)}</div>`).join('')
    || `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:8px">${data.error ? esc(data.error) : 'no subfolders — pick this one, or type a new folder name onto the path above'}</div>`;
  const roots = data.roots.map(r => `<span class="dp-root ${r.path === data.path ? 'on' : ''}" data-act="dp-go" data-path="${esc(r.path)}">${esc(r.name)}</span>`).join('');
  return `<div class="menu-veil" data-act="dp-cancel" style="background:rgba(0,0,0,.45)"></div>
    <div style="position:fixed;z-index:7;left:50%;top:18%;transform:translateX(-50%);width:620px;max-height:70vh;background:#16191c;border:1px solid #2f353b;border-radius:8px;box-shadow:0 16px 48px rgba(0,0,0,.6);padding:16px;display:flex;flex-direction:column;gap:10px">
      <span style="font:600 13px var(--sans)">${esc(d.title || 'Choose a folder')}</span>
      <div style="display:flex;gap:6px;flex-wrap:wrap">${roots}</div>
      <div style="display:flex;gap:8px;align-items:center">
        <span class="restore-btn" data-act="dp-up" title="up one level" ${data.parent ? '' : 'style="opacity:.3;pointer-events:none"'}>↑</span>
        ${inp('dp-path', 'path', d.path)}
        <span class="restore-btn" data-act="dp-type" title="go to the typed path">go</span>
      </div>
      <div style="overflow:auto;min-height:120px;max-height:38vh;border:1px solid var(--bord-raise);border-radius:4px;background:var(--bg-raise)">${rows}</div>
      <div style="display:flex;gap:8px;align-items:center;justify-content:flex-end">
        <span style="flex:1;font:400 10px var(--sans);color:var(--fg-faint)">Output goes directly into this folder — the recipe name is NOT added. Pick (or type) the final folder, e.g. Packs/My Recipe. It does not need to exist yet; materialize creates it and leaves existing files alone.</span>
        <span class="restore-btn" data-act="dp-cancel">cancel</span>
        <span class="mat-btn" style="margin:0;padding:6px 16px;font-size:11px" data-act="dp-pick">use this folder</span>
      </div>
    </div>`;
}

async function dirPickGo(path) {
  const d = S.dirPick;
  if (!d) return;
  const data = await api('/api/dirs?path=' + encodeURIComponent(path || ''));
  d.data = data || { entries: [], roots: [] };
  d.path = (data && data.path) || path;
  render();
  document.getElementById('dp-path')?.focus();
}

function openDirPicker(title, start, onPick) {
  S.dirPick = { title, path: start || '', data: null, onPick };
  dirPickGo(start || '');
}

function titlebar() {
  const s = S.summary;
  const meta = s ? `${esc(s.workspace.replace(/^\/Users\/[^/]+/, '~'))} · ${s.locations} sources · ${n(s.files)} files · ${fmtB(s.bytes)}` : '…';
  return `<div class="titlebar"><span class="brand">mtunes</span><span class="meta">${meta}</span></div>`;
}

// The screens are steps, not tabs (SPEC §19.1): Library and Setup are
// places; Recipe → Plan → Materialize happen in that order, and a step
// is reachable only when the one before it has something to hand over.
function runAllowed() {
  const r = S.run || {};
  if (r.status === 'running' || r.status === 'done' || r.status === 'error') return true;
  const p = S.pf && S.pf.plan;
  return !!(S.view && p && p.fits && !(p.errors || []).length);
}
function stepAllowed(k) {
  if (k === 'plan') return !!S.view;
  if (k === 'run') return runAllowed();
  return true;
}
function tabbar() {
  const tab = (k, label, num, off, title) => `
      <div class="tab ${S.screen === k ? 'on' : ''} ${off ? 'off' : ''}" data-act="tab" data-k="${k}" title="${esc(title || '')}">
        <span class="num">${num}</span><span class="label">${label}</span>
      </div>`;
  const steps = [['recipe', 'Recipe', '2', ''], ['plan', 'Plan', '3', 'pick a recipe first'], ['run', 'Materialize', '4', 'the plan has to fit, with no errors']];
  const lens = S.lens ? `
    <div class="lens-chip"><span class="dot"></span><span class="name">Lens · ${esc(S.lens)}</span>
    <span class="x" data-act="clear-lens">✕</span></div>` : '';
  // one click from "fix pushed" to "running the fix" — visible on every screen
  let upd = '';
  if (S.updBusy || S.updMsg) {
    upd = `<span style="font:500 10.5px var(--mono);color:var(--amber);white-space:nowrap;margin-right:8px">${esc(S.updMsg)}</span>`;
  } else if (S.upd && S.upd.available) {
    const r = S.upd.remote || {};
    upd = `<span data-act="upd-apply" title="${esc(r.sha || '')} · ${esc(r.subject || '')}" style="cursor:pointer;font:600 10.5px var(--mono);color:var(--amber);border:1px solid rgba(224,182,79,.4);border-radius:10px;padding:2px 9px;margin-right:8px">update → ${esc((r.sha || '').slice(0, 7))}</span>`;
  }
  return `<div class="tabbar">
    ${tab('library', 'Library', '1')}
    <span class="steps">
      ${steps.map(([k, label, num, why], i) => (i ? '<span class="arrow">→</span>' : '') + tab(k, label, num, !stepAllowed(k), stepAllowed(k) ? '' : why)).join('')}
    </span>
    ${S.screen === 'cards' ? tab('cards', 'History', '5') : ''}
    <div style="flex:1"></div>${upd}${lens}
    ${tab('sources', 'Setup', '6')}
  </div>`;
}

function statusbar() {
  const ann = S.summary ? `annotations: ${n(S.summary.packs_annotated)} packs known` : '';
  // "which build am I actually running" must be answerable without an update
  // pending — the tab-bar chip only exists when a newer build is known.
  const u = S.upd;
  const build = u ? `build ${u.commit || u.version || '?'}` : '';
  return `<div class="statusbar"><span>1–5 screens</span><span>L cycle lens</span><span>⌘K search</span>
    <div style="flex:1"></div>${build ? `<span title="${esc(u.note || (u.available ? 'a newer build is published' : 'up to date'))}">${esc(build)}</span>` : ''}<span>${ann}</span></div>`;
}

/* ---------- library ---------- */

// Having the whole pack is the silent default — a badge only appears
// when the local copy is a subset.
function badgeFor(p) {
  const pct = (x) => {
    const v = x * 100;
    if (v >= 1) return Math.round(v) + '%';
    if (v >= 0.1) return v.toFixed(1) + '%';
    return '<0.1%';
  };
  if (p.match === 'exact' && p.samples_listed && p.files < p.samples_listed) {
    return `<span class="badge subset">${pct(p.files / p.samples_listed)}</span>`;
  }
  if (p.match === 'partial' && p.match_fraction < 1) {
    return `<span class="badge subset">${pct(p.match_fraction)}</span>`;
  }
  return '';
}

const artHue = (s) => { let h = 0; for (const c of s) h = (h * 31 + c.charCodeAt(0)) % 360; return h; };

// The head-bar chips group packs by who made them: the vendor when the row
// knows one (vendor-dirs archives, annotated locations), else the location.
const packGroup = p => p.vendor || p.location;

const sampleMode = () => !!(S.fInst || S.fKey || S.fBpm || S.fCat);

// Instruments offered in the filter bar, grouped the way a producer thinks.
// These are canonical ids from the annotations lexicon; a vendor that never
// labelled a sound simply has none of it here.
const INSTRUMENTS = [
  ['drums', ['kick', 'snare', 'clap', 'hat', 'cymbal', 'tom', 'rim', 'break', 'drums']],
  ['percussion', ['shaker', 'tambourine', 'conga', 'cowbell', 'clave', 'percussion']],
  ['bass', ['sub', 'reese', 'bass']],
  ['keys', ['piano', 'organ', 'keys', 'mallet', 'bell']],
  ['synth', ['lead', 'pad', 'pluck', 'stab', 'arp', 'synth']],
  ['acoustic', ['guitar', 'strings', 'brass', 'woodwind']],
  ['voice / fx', ['vocal', 'fx', 'foley']],
];

function filterBar() {
  const opts = INSTRUMENTS.map(([group, ids]) =>
    `<optgroup label="${group}">${ids.map(i => `<option value="${i}" ${S.fInst === i ? 'selected' : ''}>${i}</option>`).join('')}</optgroup>`).join('');
  const cats = ['one-shots', 'loops', 'multisamples', 'fx'];
  const inp = (id, ph, val, w) =>
    `<input id="${id}" class="filt" placeholder="${ph}" value="${esc(val)}" style="width:${w}">`;
  const active = sampleMode();
  return `
    <div class="filters">
      <span class="flabel">Find</span>
      <select id="f-inst" class="filt" style="width:150px">
        <option value="">any instrument</option>${opts}
      </select>
      <select id="f-cat" class="filt" style="width:130px">
        <option value="">any category</option>
        ${cats.map(c => `<option value="${c}" ${S.fCat === c ? 'selected' : ''}>${c}</option>`).join('')}
      </select>
      ${inp('f-key', 'key (Am, C#1)', S.fKey, '120px')}
      ${inp('f-bpm', 'bpm (120-130)', S.fBpm, '120px')}
      ${active ? `<span class="restore-btn" data-act="clear-filters">clear</span>` : ''}
      <div style="flex:1"></div>
      <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">
        ${active ? 'showing individual samples across every pack' : 'filter to search inside packs'}
      </span>
    </div>`;
}

function renderSamples() {
  const d = S.samples;
  const chips = (d?.instruments || []).slice(0, 12).map(f =>
    `<span class="chip ${S.fInst === f.id ? 'active' : ''}" data-act="f-inst" data-v="${esc(f.id)}">${esc(f.id)} <span style="color:var(--fg-faint)">${n(f.count)}</span></span>`).join('');
  const head = `
    <div class="screen-head">
      <h1>Samples</h1>
      <span class="sum">${S.samplesBusy ? 'searching…' : d ? `${n(d.total)} match${d.total === 1 ? '' : 'es'}${d.total > d.shown ? ` · showing ${n(d.shown)}` : ''}` : ''}</span>
      <div style="flex:1"></div>
      <div class="search">⌕ <input id="search" placeholder="Search names…" value="${esc(S.search)}"><span class="kbd">⌘K</span></div>
      <div style="position:relative">
        <div class="lens-btn ${S.lens ? 'on' : ''}" data-act="toggle-menu">
          <span class="dot"></span><span class="label">Lens · ${S.lens ? esc(S.lens) : 'off'}</span><span class="caret">▾</span>
        </div>
      </div>
    </div>
    ${filterBar()}
    ${chips ? `<div class="filters" style="padding-top:0;border:0">${chips}</div>` : ''}`;

  if (S.samplesBusy && !d) return head + `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:20px">searching…</div>`;
  if (!d || !d.samples.length) {
    return head + `<div style="font:400 11.5px var(--mono);color:var(--fg-faint);padding:24px;line-height:1.7">
      nothing matches.<br><span style="color:var(--fg-ghost)">only what the vendor labelled is searchable — unlabelled samples never match rather than being guessed at.</span></div>`;
  }
  const rows = d.samples.map(s => `
    <div class="srow" data-act="play-sample" data-loc="${esc(s.location)}" data-path="${esc(s.path)}">
      <span class="sname" title="${esc(s.path)}">${esc(s.name)}</span>
      <span class="spack" title="${esc(s.pack)}">${esc(s.pack)}</span>
      <span class="smeta">${s.instrument ? `<b>${esc(s.instrument)}</b>` : ''}</span>
      <span class="smeta">${s.key ? esc(s.key) : ''}</span>
      <span class="smeta">${s.bpm ? s.bpm + ' bpm' : ''}</span>
      <span class="smeta">${esc(s.category || '')}</span>
      <span class="smeta" style="text-align:right">${fmtDur(s.duration)}</span>
    </div>`).join('');
  return head + `<div class="slist">${rows}</div>`;
}

async function loadSamples() {
  const q = new URLSearchParams();
  if (S.fInst) q.set('instrument', S.fInst);
  if (S.fKey) q.set('key', S.fKey);
  if (S.fBpm) q.set('bpm', S.fBpm);
  if (S.fCat) q.set('category', S.fCat);
  if (S.locFilter) q.set('pack', S.locFilter);
  if (S.search) q.set('q', S.search);
  if (S.lens) q.set('device', S.lens);
  S.samplesBusy = true; render();
  try { S.samples = await api('/api/samples?' + q); } catch (e) { S.samples = { total: 0, samples: [], instruments: [] }; }
  S.samplesBusy = false; render();
}

/* ---------- discover: browse what you don't have ----------
   Thin cards on purpose: registry identity only (name, vendor, art,
   description, license ceiling, relations, the pointer). No auditioning,
   no per-hit browsing — mtunes doesn't hold the bytes; previewing is the
   vendor's job on the page we link to. The license value is a ceiling on
   claims: only royalty-free is ever LABELLED royalty-free. */

const segToggle = () => `<div class="seg">
  <span class="${S.discover ? '' : 'on'}" data-act="disc-off">Library</span>
  <span class="${S.discover ? 'on' : ''}" data-act="disc-on">Discover</span>
</div>`;

const LICENSE_BADGE = { 'royalty-free': 'royalty-free', 'cc0': 'CC0', 'cc-by': 'CC BY', 'cc-by-nc': 'CC BY-NC', 'informal-free': 'free · informal' };
const obtainable = (r) => ['vendor-free', 'vendor-paid', 'distributor'].includes(r.class) && r.url;

// Relation + containment hints against the library. skip = you already hold
// this content one way or another; upgrade = you own a taste of it and the
// full thing exists at the vendor — the single best discovery row there is.
function discHints(r) {
  const hints = []; let skip = false, upgrade = false;
  if (r.have_fraction >= 0.999) { hints.push('all of this content is already in your library'); skip = true; }
  else if (r.have_fraction > 0) hints.push(`${Math.round(r.have_fraction * 100)}% of this content is already in your library`);
  for (const rel of r.relations || []) {
    if (!rel.owned) continue;
    const sub = rel.type === 'subset-of' || rel.type === 'sampler-of';
    if (sub && !rel.inverse) { hints.push(`contained in ${rel.pack} — you own it`); skip = true; }
    else if (sub && rel.inverse) { hints.push(`you own its ${rel.type === 'sampler-of' ? 'sampler' : 'subset'}: ${rel.pack}`); upgrade = true; }
    else if (rel.type === 'superseded-by' && !rel.inverse) { hints.push(`superseded by ${rel.pack} — you own it`); skip = true; }
    else if (rel.type === 'superseded-by' && rel.inverse) { hints.push(`newer edition of ${rel.pack}, which you own`); upgrade = true; }
    else if (rel.type === 'bundle-of' && rel.inverse) { hints.push(`inside ${rel.pack} — you own the bundle`); skip = true; }
    else if (rel.type === 'reissue-of' && !rel.inverse) { hints.push(`reissue of ${rel.pack} — you own it`); skip = true; }
    else hints.push(`${rel.type.replace(/-/g, ' ')} ${rel.pack} (owned)`);
  }
  return { hints, skip, upgrade };
}

function discCard(r, ghost) {
  const art = r.image
    ? `<div class="art"><img src="/api/art?u=${encodeURIComponent(r.image)}" loading="lazy" onerror="this.parentNode.classList.add('none');this.remove();this.textContent='/'"></div>`
    : `<div class="art" style="background:linear-gradient(135deg,hsl(${artHue(r.name)},38%,42%),hsl(${artHue(r.name)},45%,24%))">${esc(r.name[0] || '?')}</div>`;
  const badges = [];
  if (r.class === 'vendor-free' || (r.class === 'distributor' && r.gate !== 'purchase')) badges.push('<span class="badge free">free</span>');
  if (r.class === 'vendor-paid' || (r.class === 'distributor' && r.gate === 'purchase')) badges.push('<span class="badge paid">paid</span>');
  if (r.discontinued) badges.push('<span class="badge orphan">out of print</span>');
  const lic = LICENSE_BADGE[r.license]; // ceiling on claims: anything else says nothing
  if (lic) badges.push(`<span class="badge lic">${esc(lic)}</span>`);
  const { hints, skip, upgrade } = discHints(r);
  const hintHtml = hints.slice(0, 2).map(h => `<div class="hint ${upgrade && !skip ? 'upgrade' : ''}" title="${esc(h)}">${esc(h)}</div>`).join('');
  const via = r.class === 'distributor' && r.via ? ` · via ${esc(r.via)}` : '';
  const stats = r.samples_listed ? `<div class="stats">vendor lists ${n(r.samples_listed)} samples</div>` : '';
  const link = obtainable(r)
    ? `<a class="get-link" href="${esc(r.url)}" target="_blank" title="${esc(r.gate && r.gate !== 'none' ? 'vendor page — asks for ' + r.gate : 'vendor page')}">get it ↗</a>`
    : '';
  return `<div class="pack thin ${ghost || skip ? 'ghost' : ''}" title="${esc(r.description || '')}">${art}
    <div class="body">
      <div class="name" title="${esc(r.name)}">${esc(r.name)}</div>
      <div class="vline"><span class="vendor">${esc(r.vendor)}${via}</span>${badges.join('')}</div>
      ${hintHtml}${stats}
    </div>${link}</div>`;
}

function renderDiscover() {
  const q = S.search.toLowerCase();
  const all = (S.disc || []).filter(r =>
    !q || r.name.toLowerCase().includes(q) || r.vendor.toLowerCase().includes(q) || (r.tags || []).some(tg => tg.includes(q)));
  const acq = all.filter(obtainable);
  const rest = all.filter(r => !obtainable(r));
  // upgrades float, already-covered content sinks
  const rank = (r) => { const h = discHints(r); return h.upgrade && !h.skip ? 0 : h.skip ? 2 : 1; };
  acq.sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));

  const head = `
    <div class="screen-head">
      <h1>Discover</h1>${segToggle()}
      <span class="sum">${S.discBusy ? 'loading…' : `${acq.length} obtainable${rest.length ? ` · ${rest.length} recognized only` : ''}`}</span>
      <div style="flex:1"></div>
      <span class="chip ${S.obtainable ? 'active' : ''}" data-act="obtainable" title="acquirable packs only — flip it off to see the out-of-print tail">obtainable${S.obtainable ? ' ✓' : ''}</span>
      <div class="search">⌕ <input id="search" placeholder="Search packs…" value="${esc(S.search)}"><span class="kbd">⌘K</span></div>
      <span class="chip" data-act="go-recipe" title="pick or make a recipe — the way out of the library into the pipeline" style="color:var(--fg);border-color:var(--bord-hover)">materialize…</span>
    </div>
    <div class="disc-note" style="padding:8px 18px 0">Everything here is in the community registry because someone owns it. Cards are thin on purpose — previews live on the vendor's page, and that's where the link goes.</div>`;

  if (S.discBusy && !S.disc) return head + `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:20px">loading…</div>`;
  const grid = acq.length
    ? `<div class="grid">${acq.map(r => discCard(r, false)).join('')}</div>`
    : `<div style="font:400 11.5px var(--mono);color:var(--fg-faint);padding:24px 18px">nothing to discover — every annotated pack is already in your library.</div>`;
  const tail = !S.obtainable && rest.length ? `
    <div class="disc-sect">RECOGNIZED, NOT SOURCED</div>
    <div class="disc-note">These existed — that's real information — but there's no legitimate source we'd point you at. Out-of-print reference material, not a storefront.</div>
    <div class="grid">${rest.map(r => discCard(r, true)).join('')}</div>` : '';
  return head + grid + tail;
}

function renderLibrary() {
  if (S.packOpen) return renderPackDetail();
  if (S.discover) return renderDiscover();
  if (sampleMode()) return renderSamples();
  const q = S.search.toLowerCase();
  const rows = S.packs.filter(p =>
    (!S.locFilter || packGroup(p) === S.locFilter) &&
    (!q || p.name.toLowerCase().includes(q) || (p.provider || '').toLowerCase().includes(q) || (p.vendor || '').toLowerCase().includes(q) || (p.tags || []).some(tg => tg.includes(q))))
    .slice().sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' }));

  let sum;
  if (S.lens) {
    const ef = rows.reduce((a, p) => a + (p.eligible || 0), 0), tf = rows.reduce((a, p) => a + p.files, 0);
    const em = rows.reduce((a, p) => a + (p.converted_bytes || 0), 0);
    sum = `${n(ef)} of ${n(tf)} files eligible · ${fmtB(em)} after transcode`;
  } else {
    sum = `${rows.length} packs · ${n(rows.reduce((a, p) => a + p.files, 0))} files · ${fmtB(rows.reduce((a, p) => a + p.bytes, 0))}`;
  }

  const locs = [...new Set(S.packs.map(packGroup))].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
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
      <h1>Library</h1>${segToggle()}<span class="sum">${sum}</span>
      <div style="flex:1"></div>
      ${locs.map(l => `<span class="chip ${S.locFilter === l ? 'active' : ''}" data-act="loc" data-l="${esc(l)}" title="${S.locFilter === l ? 'click to clear' : 'only ' + esc(l)}">${esc(l)}${S.locFilter === l ? ' ✕' : ''}</span>`).join('')}
      ${S.locFilter ? `<span class="chip active" data-act="add-group" data-g="${esc(S.locFilter)}" title="add every ${esc(S.locFilter)} pack to a recipe as one rule">+ add all ${n(rows.length)} to recipe</span>` : ''}
      <div class="search">⌕ <input id="search" placeholder="Search packs…" value="${esc(S.search)}"><span class="kbd">⌘K</span></div>
      <span class="chip" data-act="go-recipe" title="pick or make a recipe — the way out of the library into the pipeline" style="color:var(--fg);border-color:var(--bord-hover)">materialize…</span>
      <div style="position:relative">
        <div class="lens-btn ${S.lens ? 'on' : ''}" data-act="toggle-menu">
          <span class="dot"></span><span class="label">Lens · ${S.lens ? esc(S.lens) : 'off'}</span><span class="caret">▾</span>
        </div>
        ${menu}
      </div>
    </div>
    ${filterBar()}

    <div class="grid">
      ${rows.map(p => {
        const art = p.image
          ? `<div class="art"><img src="/api/art?u=${encodeURIComponent(p.image)}" loading="lazy" onerror="this.parentNode.classList.add('none');this.remove();this.textContent='/'"></div>`
          : p.slug
            ? `<div class="art" style="background:linear-gradient(135deg,hsl(${artHue(p.name)},38%,42%),hsl(${artHue(p.name)},45%,24%))">${esc(p.name[0] || '?')}</div>`
            : `<div class="art none">/</div>`;
        const vendor = p.provider || p.vendor || p.location;
        const stats = S.lens
          ? `<div class="stats lens">${n(p.eligible)} <span class="of">of ${n(p.files)}</span> · ${fmtB(p.converted_bytes || 0)}</div>`
          : `<div class="stats">${n(p.files)} files · ${fmtB(p.bytes)}</div>`;
        const link = `<span style="display:flex;flex-direction:column;align-items:center;gap:6px;align-self:center">
          ${p.url ? `<a class="link" href="${esc(p.url)}" target="_blank" title="product page">↗</a>` : ''}
          <span data-act="add-to" data-loc="${esc(p.location)}" data-glob="${esc(p.dir)}/**" data-as="${esc(p.provider ? p.location.toUpperCase() + '/' + p.dir : '')}" data-label="${esc(p.name)}" title="add this pack to a recipe" style="font:600 13px var(--mono);color:var(--fg-ghost);cursor:pointer">+</span>
        </span>`;
        return `<div class="pack" data-blurb="${esc(p.description ? '' : (p.blurb || p.url || ''))}" title="${esc(p.description || '')}" data-act="open-pack" data-loc="${esc(p.location)}" data-dir="${esc(p.dir)}">${art}
          <div class="body">
            <div class="name" title="${esc(p.dir)}">${esc(p.name)}</div>
            <div class="vline"><span class="vendor">${esc(vendor)}</span>${badgeFor(p)}</div>
            ${stats}
          </div>${link}</div>`;
      }).join('')}
    </div>`;
}

function fmtDur(s) {
  if (!s) return '—';
  if (s < 10) return s.toFixed(2) + 's';
  const m = Math.floor(s / 60);
  return m ? `${m}:${String(Math.round(s % 60)).padStart(2, '0')}` : Math.round(s) + 's';
}

function renderPackDetail() {
  const po = S.packOpen, pd = S.pd;
  const art = po.image
    ? `<div class="pd-art"><img src="/api/art?u=${encodeURIComponent(po.image)}"></div>`
    : po.slug
      ? `<div class="pd-art" style="background:linear-gradient(135deg,hsl(${artHue(po.name)},38%,42%),hsl(${artHue(po.name)},45%,24%))">${esc(po.name[0] || '?')}</div>`
      : `<div class="pd-art none">/</div>`;
  const lensLine = S.lens && po.eligible != null
    ? `<span style="font:500 11px var(--mono);color:var(--teal)">${esc(S.lens)}: ${n(po.eligible)} of ${n(po.files)} eligible · ${fmtB(po.converted_bytes || 0)} converted</span>` : '';
  const desc = S.pdDesc
    ? `<div style="font:400 12px/1.55 var(--sans);color:#b6bcc2;max-width:680px;max-height:96px;overflow-y:auto;white-space:pre-line;padding-right:8px">${esc(S.pdDesc)}</div>`
    : po.slug ? '' : `<div style="font:400 11.5px var(--mono);color:var(--fg-faint)">no annotation for this source — folder indexed from ${esc(po.location)}</div>`;
  const urlChip = po.url ? `<a href="${esc(po.url)}" target="_blank" title="${esc(po.url)}" style="font:600 10.5px var(--sans);color:var(--teal);border:1px solid rgba(61,196,207,.35);border-radius:4px;padding:2px 9px;text-decoration:none">product page ↗</a>` : '';

  let body = `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:20px">loading…</div>`;
  if (pd) {
    const here = S.pdFolder ? S.pdFolder + '/' : '';
    const up = S.pdFolder ? `
      <div class="pd-folder" data-act="pd-up">
        <span style="font:500 11.5px var(--mono);color:var(--fg-dim);flex:1">..</span>
      </div>` : '';
    const folders = `
      <div style="font:500 10px var(--mono);color:var(--fg-faint);padding:2px 10px 8px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc('/' + here)}">${esc(S.pdFolder ? (S.pdFolder.includes('/') ? '…/' : '/') + S.pdFolder.split('/').pop() + '/' : '/')}</div>
      ${up}` + (pd.folders || []).map(f => `
      <div class="pd-folder" data-act="pd-folder" data-f="${esc(S.pdFolder ? S.pdFolder + '/' + f.name : f.name)}">
        <span style="font:500 11.5px var(--mono);color:var(--fg-dim);flex:1">${esc(f.name)}/</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">${n(f.count)}</span>
      </div>`).join('');
    const rows = (pd.files || []).map(fl => {
      const isPlaying = S.player && S.player.path === fl.path;
      const fmt = fl.format ? `${fl.format} ${fl.depth || '?'}/${fl.rate ? (fl.rate / 1000).toFixed(1) : '?'}k ${fl.channels === 1 ? 'mono' : fl.channels === 2 ? 'st' : (fl.channels || '') + 'ch'}` : '—';
      const lensTxt = S.lens ? (fl.ineligible ? fl.ineligible : fl.converted ? '→ ' + fmtB(fl.converted) : '') : '';
      const lensColor = fl.ineligible ? 'var(--warn)' : 'var(--fg-faint)';
      const playable = !!fl.format;
      return `<div class="pd-row ${isPlaying ? 'playing' : ''}" ${playable ? `data-act="play" data-p="${esc(fl.path)}" data-n="${esc(fl.name)}" data-d="${fl.duration || 0}"` : ''}>
        <span style="font:400 9.5px var(--sans);color:${isPlaying ? 'var(--teal)' : playable ? 'var(--fg-faint)' : '#33393f'}">${isPlaying && S.player.playing ? '❚❚' : playable ? '▷' : '·'}</span>
        <span style="font:400 11px var(--mono);color:var(--fg-num);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(fl.name)}</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-log)">${esc(fmt)}</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-log)">${fmtDur(fl.duration)}</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-log)">${fl.key ? esc(fl.key.toUpperCase() + (fl.chord === 'minor' ? 'm' : '')) : ''}${fl.bpm ? (fl.key ? ' · ' : '') + fl.bpm : ''}</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-log)">${fmtB(fl.size)}</span>
        <span style="font:500 10.5px var(--mono);color:${lensColor};white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(lensTxt)}</span>
      </div>`;
    }).join('');
    const more = pd.total > pd.shown ? `<div style="font:400 10.5px var(--mono);color:var(--fg-faint);padding:8px 12px">… and ${n(pd.total - pd.shown)} more in this folder</div>` : '';
    body = `<div class="pd-grid">
      <div style="border-right:1px solid var(--bord);padding:10px 8px;overflow:auto">${folders}</div>
      <div style="min-width:0;display:flex;flex-direction:column">
        <div class="pd-cols"><span></span><span>FILE</span><span>FORMAT</span><span>LENGTH</span><span>KEY · BPM</span><span>SIZE</span><span>${S.lens ? esc(S.lens.toUpperCase()) + ' LENS' : ''}</span></div>
        <div style="overflow:auto;flex:1">${rows}${more}</div>
      </div>
    </div>`;
  }

  const pl = S.player;
  const bars = pl ? Array.from({ length: 90 }, (_, i) => {
    let h = 0; for (const c of pl.path) h = (h * 31 + c.charCodeAt(0) + i * 7) % 97;
    return `<span style="height:${4 + (h % 20)}px"></span>`;
  }).join('') : '';
  const playerBar = pl ? `
    <div class="player">
      <span class="play-btn" data-act="toggle-play">${pl.playing ? '❚❚' : '▶'}</span>
      <span style="font:500 11px var(--mono);color:var(--fg);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:320px">${esc(pl.name)}</span>
      <div class="wave">${bars}<div class="fill" id="wavefill"></div></div>
      <span style="font:400 10.5px var(--mono);color:var(--fg-dim)">${fmtDur(pl.dur)}</span>
      <span style="font:400 10px var(--sans);color:var(--fg-ghost)">preview streams from source — nothing is written</span>
    </div>` : '';

  return `
    <div style="display:flex;flex-direction:column;min-height:100%">
    <div style="display:flex;align-items:center;gap:10px;padding:10px 18px;border-bottom:1px solid var(--bord)">
      <span class="crumb-btn" data-act="close-pack">← Library</span>
      <span style="font:400 11px var(--mono);color:var(--fg-faint)">library / ${esc(po.provider || po.vendor || po.location)} / ${esc(po.name)}</span>
      <div style="flex:1"></div>${lensLine}
      <span class="restore-btn" data-act="add-to" data-loc="${esc(po.location)}" data-glob="${esc(po.dir)}${S.pdFolder ? '/' + esc(S.pdFolder) : ''}/**" data-as="${esc(po.provider ? po.location.toUpperCase() + '/' + po.dir + (S.pdFolder ? '/' + S.pdFolder : '') : '')}" data-label="${esc(po.name)}${S.pdFolder ? ' / ' + esc(S.pdFolder) : ''}" title="add what you're looking at to a recipe">+ add to recipe</span>
    </div>
    <div class="pd-head">
      ${art}
      <div style="min-width:0;flex:1;display:flex;flex-direction:column;gap:6px">
        <div style="display:flex;align-items:center;gap:10px">
          <span style="font:700 18px var(--sans)">${esc(po.name)}</span>${badgeFor(po)}
        </div>
        <div style="display:flex;align-items:center;gap:10px">
          <span style="font:400 11.5px var(--sans);color:var(--fg-dim)">${esc(po.provider || po.vendor || po.location)}</span>${urlChip}
        </div>
        ${desc}
        ${(po.tags || []).length ? `<div style="display:flex;gap:5px;flex-wrap:wrap;margin-top:2px">${po.tags.map(tg => `<span class="tagchip">${esc(tg)}</span>`).join('')}</div>` : ''}
        <div style="font:500 11px var(--mono);color:var(--fg-faint);margin-top:2px">${n(po.files)} files · ${fmtB(po.bytes)}${po.samples_listed ? ` · vendor lists ${n(po.samples_listed)} samples` : ''}</div>
      </div>
    </div>
    ${body}
    ${playerBar}
    </div>`;
}

/* ---------- sources ---------- */

async function loadSources() {
  const [locs, sugg, stos, pres, vols, ann] = await Promise.all([
    api('/api/locations'), api('/api/suggestions'), api('/api/storages'),
    api('/api/presets'), api('/api/volumes'), api('/api/annotations')]);
  S.locations = locs || []; S.suggestions = sugg || [];
  S.storages = stos || []; S.presets = pres || []; S.volumes = vols || [];
  S.ann = ann || null;
  render();
}

async function updateAnnotations() {
  S.annBusy = true; S.annMsg = ''; render();
  const r = await api('/api/annotations', { method: 'POST' });
  S.annBusy = false;
  if (r && !r.error) {
    S.ann = r;
    S.annMsg = r.action === 'updated' ? (r.note || 'updated') + (r.reharvested ? ' — classifications refreshed' : '') + (r.redundant_local ? ` — ${r.redundant_local} of your local corrections are now in the repo; reconcile them on the Plan step` : '')
      : r.action === 'cloned' ? 'annotations fetched' + (r.reharvested ? ' — classifications refreshed' : '')
      : r.action === 'current' ? 'already up to date'
      : (r.note || 'could not update');
  } else {
    S.annMsg = (r && r.error) || 'could not reach the app';
  }
  render();
}

async function viewAction(body) {
  const r = await api('/api/view', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  if (r.error) { S.toast = r.error; render(); return false; }
  S.views = await api('/api/views') || [];
  return true;
}

/* ---------- recipe mutations ----------
   Every one of these is a whole logical edit ("all of Splice", "not this
   pack") expressed in as few /api/view calls as possible, and each call is
   either index-free (add-rule with replace_prefix, add/remove-exclude by
   glob) or a single batched remove-rules. Nothing here fires two
   index-bearing calls in a row, so the indexes rendered are the indexes
   acted on. */

// One [[include]] covering every pack in a group, replacing the narrower
// rules underneath it. Selection-preserving by construction: the group is
// already fully in.
function collapseGroup(g) {
  const gr = g.groupRule;
  if (!gr) return Promise.resolve(false);
  const prefix = globRoot(gr.glob);
  return viewAction({ action: 'add-rule', name: S.view, location: gr.location, glob: gr.glob, as: gr.as,
    replace_prefix: prefix, replace_location: prefix === '', note: 'all of ' + g.key });
}

async function checkGroup(g) {
  for (const e of g.packs) {
    if (e.cut && !await viewAction({ action: 'remove-exclude', name: S.view, exclude_glob: e.p.dir + '/**' })) return false;
  }
  if (g.groupRule) return collapseGroup(g);
  // The group's packs sit in different locations, so no single glob reaches
  // them all — the one case that still writes a rule per pack.
  for (const e of g.packs) {
    if (e.in) continue;
    if (!await addPackRule(e)) return false;
  }
  return true;
}

async function uncheckGroup(g) {
  if (g.exclusive) return viewAction({ action: 'remove-rules', name: S.view, indices: g.rules });
  // One of these rules also feeds another vendor — take this group's own
  // rules out and carve the rest out with excludes rather than cutting a
  // rule someone else is standing on.
  const own = [...new Set(g.packs.filter(e => e.in).flatMap(e => e.own))];
  if (own.length && !await viewAction({ action: 'remove-rules', name: S.view, indices: own })) return false;
  for (const e of g.packs) {
    if (e.in && e.wide.length && !await cutPack(e)) return false;
  }
  return true;
}

const addPackRule = (e) => viewAction({ action: 'add-rule', name: S.view, location: e.p.location, glob: e.p.dir + '/**',
  as: e.p.provider ? e.p.location.toUpperCase() + '/' + e.p.dir : '', note: 'added from the library: ' + e.p.name });

const cutPack = (e) => viewAction({ action: 'add-exclude', name: S.view, exclude_glob: e.p.dir + '/**',
  note: 'carved out of a wider rule: ' + e.p.name });

async function togglePack(e) {
  if (e.in) {
    if (e.own.length && !await viewAction({ action: 'remove-rules', name: S.view, indices: e.own })) return false;
    return e.wide.length ? cutPack(e) : true;
  }
  if (e.cut && !await viewAction({ action: 'remove-exclude', name: S.view, exclude_glob: e.p.dir + '/**' })) return false;
  return e.wide.length ? true : addPackRule(e);
}

// Applies an edit and re-reads the recipe: the group model is derived from
// pre-flight, so there is nothing to keep in sync by hand.
function recipeEdit(promise, toast) {
  S.pfBusy = true; render();
  Promise.resolve(promise).then(ok => {
    if (!ok) { S.pfBusy = false; render(); return; }
    if (toast) { S.toast = toast; setTimeout(() => { S.toast = ''; render(); }, 3500); }
    loadPreflight();
  });
}

async function pollScans() {
  S.scans = await api('/api/scan') || {};
  const busy = Object.values(S.scans).some(s => s.status === 'running');
  if (S.screen === 'sources') {
    if (busy) render();
    else if (S._wasBusy) { await loadSources(); await loadPacks(); render(); }
  }
  S._wasBusy = busy;
  setTimeout(pollScans, busy ? 700 : 4000);
}

async function startScan(name) {
  await api('/api/scan', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ location: name }) });
  S.scans[name] = { location: name, status: 'running' }; S._wasBusy = true; render();
}

async function addLocation(body) {
  const r = await api('/api/locations', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  if (r.error) { S.toast = r.error; render(); return; }
  S.addForm = null; S._wasBusy = true;
  await loadSources();
}

const RESCANS = [['manual', 'manual only'], ['1h', 'hourly'], ['6h', 'every 6h'], ['24h', 'daily']];

function renderSources() {
  if (!S.locations.length && !S._srcLoaded) { S._srcLoaded = true; loadSources(); }

  const rows = S.locations.map(l => {
    const sc = S.scans[l.name];
    const running = sc && sc.status === 'running';
    const prog = running && sc.total ? ` ${n(sc.done)}/${n(sc.total)} ${esc(sc.stage || '')}` : '';
    const when = l.scanned ? new Date(l.scanned).toLocaleString() : 'never scanned';
    return `<div style="display:flex;align-items:center;gap:12px;background:var(--bg-card);border:1px solid ${l.stale ? 'rgba(224,182,79,.3)' : 'var(--bord-card)'};border-radius:6px;padding:11px 13px">
      <div style="min-width:0;flex:1;display:flex;flex-direction:column;gap:3px">
        <div style="display:flex;align-items:center;gap:8px">
          <span style="font:600 12.5px var(--sans)">${esc(l.name)}</span>
          <span style="font:400 10px var(--mono);color:var(--fg-faint)">${esc(l.type)}${l.host ? ' · ' + esc(l.host) : ''}</span>
          ${l.vendor ? `<span class="tagchip">${esc(l.vendor)}</span>` : ''}
          ${l.stale ? '<span class="badge subset" style="color:var(--warn);border-color:rgba(224,182,79,.35)">stale</span>' : ''}
        </div>
        <span style="font:400 10.5px var(--mono);color:var(--fg-faint);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(l.root)}</span>
        <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">${n(l.files)} files · scanned ${esc(when)}</span>
      </div>
      <select data-act="rescan" data-l="${esc(l.name)}" style="font:500 10.5px var(--mono);color:var(--fg-dim);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:3px 6px">
        ${RESCANS.map(([v, lbl]) => `<option value="${v}" ${(l.rescan || 'manual') === v ? 'selected' : ''}>${lbl}</option>`).join('')}
      </select>
      ${running
        ? `<span style="font:500 11px var(--mono);color:var(--amber);white-space:nowrap;min-width:120px;text-align:right">scanning${prog}</span>`
        : `<span class="restore-btn" data-act="scan" data-l="${esc(l.name)}">rescan</span>`}
    </div>`;
  }).join('');

  const sugg = S.suggestions.map(g => `
    <div style="display:flex;align-items:center;gap:12px;border:1px dashed var(--bord-raise);border-radius:6px;padding:10px 13px">
      <div style="min-width:0;flex:1;display:flex;flex-direction:column;gap:2px">
        <div style="display:flex;align-items:center;gap:8px">
          <span style="font:600 12px var(--sans)">${esc(g.label)}</span>
          <span style="font:400 10px var(--mono);color:var(--fg-faint)">${n(g.entries)} folders</span>
          ${g.vendor ? `<span class="tagchip">annotated vendor</span>` : ''}
        </div>
        <span style="font:400 10.5px var(--mono);color:var(--fg-faint);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(g.root)}</span>
        ${g.note ? `<span style="font:400 10px var(--sans);color:var(--fg-faint)">${esc(g.note)}</span>` : ''}
      </div>
      <span class="mat-btn" style="margin:0;padding:6px 14px;font-size:11px" data-act="add-sugg" data-i="${S.suggestions.indexOf(g)}">add source</span>
    </div>`).join('');

  const f = S.addForm;
  const form = !f ? '' : `
    <div style="background:var(--bg-card);border:1px solid var(--bord-hover);border-radius:6px;padding:13px;display:flex;flex-direction:column;gap:9px;max-width:620px">
      <div style="font:600 12.5px var(--sans)">Add a source</div>
      <div style="display:flex;gap:8px;align-items:center">
        <input id="af-name" placeholder="name (lowercase)" value="${esc(f.name)}" style="flex:1;font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px 8px;color:var(--fg)">
        <select id="af-type" style="font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px;color:var(--fg-dim)">
          <option value="local" ${f.type === 'local' ? 'selected' : ''}>local folder</option>
          <option value="ssh" ${f.type === 'ssh' ? 'selected' : ''}>ssh</option>
        </select>
      </div>
      <div style="display:flex;gap:8px">
        <input id="af-host" placeholder="ssh host (from ~/.ssh/config)" value="${esc(f.host || '')}" style="width:200px;font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px 8px;color:var(--fg);${f.type === 'ssh' ? '' : 'display:none'}">
        <input id="af-root" placeholder="/path/to/samples  (or ~/Samples)" value="${esc(f.root)}" style="flex:1;font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px 8px;color:var(--fg)">
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <span style="font:400 11px var(--sans);color:var(--fg-faint)">rescan</span>
        <select id="af-rescan" style="font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px;color:var(--fg-dim)">
          ${RESCANS.map(([v, lbl]) => `<option value="${v}" ${f.rescan === v ? 'selected' : ''}>${lbl}</option>`).join('')}
        </select>
        <div style="flex:1"></div>
        <span class="restore-btn" data-act="cancel-add">cancel</span>
        <span class="mat-btn" style="margin:0;padding:6px 16px;font-size:11px" data-act="save-add">add + scan</span>
      </div>
    </div>`;

  return `
    <div class="screen-head"><h1>Sources</h1>
      <span class="sum">${S.locations.length} configured${S.suggestions.length ? ` · ${S.suggestions.length} suggested` : ''}</span>
      <div style="flex:1"></div>
      ${f ? '' : '<span class="restore-btn" data-act="new-source">+ add source</span>'}
    </div>
    <div style="padding:14px 18px;display:flex;flex-direction:column;gap:9px;max-width:900px">
      ${form}
      ${rows}
      ${sugg ? `<div style="font:600 9px var(--sans);color:var(--fg-faint);letter-spacing:.1em;padding:10px 2px 2px">FOUND ON THIS MACHINE</div>${sugg}` : ''}
      ${renderAnnotations()}
      ${renderDevices()}
      ${renderStorages()}
      ${S.toast ? `<div style="font:500 11px var(--mono);color:var(--warn)">${esc(S.toast)}</div>` : ''}
    </div>`;
}

// The classification rules live in a data repo that moves without app
// releases; this card answers "which rules am I actually on" and lets the
// user pull the newest right now instead of trusting the scan-time sync.
function renderAnnotations() {
  const ann = S.ann || {};
  const h = ann.head;
  return `
    <div style="font:600 9px var(--sans);color:var(--fg-faint);letter-spacing:.1em;padding:10px 2px 2px">CLASSIFICATION RULES</div>
    <div style="display:flex;align-items:center;gap:12px;background:var(--bg-card);border:1px solid var(--bord-card);border-radius:6px;padding:9px 12px">
      <div style="min-width:0;flex:1;display:flex;flex-direction:column;gap:2px">
        ${h ? `<span style="font:400 10.5px var(--mono);color:var(--fg-dim);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">annotations ${esc(h.sha)} · ${esc(h.date)} · ${esc(h.subject)}</span>`
            : `<span style="font:400 10.5px var(--mono);color:var(--fg-faint)">annotations not fetched yet — update now, or scanning a source fetches them</span>`}
        <span style="font:400 10px var(--mono);color:var(--fg-faint)">refreshed at launch and before every scan · app ${esc(ann.version || '?')}</span>
        ${S.annMsg ? `<span style="font:500 10.5px var(--mono);color:var(--amber)">${esc(S.annMsg)}</span>` : ''}
      </div>
      ${S.annBusy
        ? `<span style="font:500 11px var(--mono);color:var(--amber);white-space:nowrap">updating…</span>`
        : `<span class="restore-btn" data-act="ann-update">update now</span>`}
    </div>`;
}

const inp = (id, ph, val, w) => `<input id="${id}" placeholder="${ph}" value="${esc(val ?? '')}" style="${w ? 'width:' + w + ';' : 'flex:1;'}font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px 8px;color:var(--fg)">`;
const sel = (id, opts, cur, w) => `<select id="${id}" style="${w ? 'width:' + w + ';' : ''}font:400 11px var(--mono);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:6px;color:var(--fg-dim)">${opts.map(([v, l]) => `<option value="${esc(v)}" ${String(cur) === String(v) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select>`;

function renderDevices() {
  const d = S.devForm;
  const list = S.devices.map(x => `<div style="display:flex;align-items:center;gap:10px;background:var(--bg-card);border:1px solid var(--bord-card);border-radius:6px;padding:9px 12px">
      <span style="font:600 12px var(--sans);min-width:130px">${esc(x.name)}</span>
      <span style="font:400 10.5px var(--mono);color:var(--fg-faint);flex:1">${esc(x.sub)}</span>
    </div>`).join('');
  const form = !d ? '' : `
    <div style="background:var(--bg-card);border:1px solid var(--bord-hover);border-radius:6px;padding:13px;display:flex;flex-direction:column;gap:9px">
      <div style="display:flex;gap:8px;align-items:center">
        <span style="font:600 12.5px var(--sans);flex:1">New device</span>
        ${sel('dv-preset', [['', 'start from preset…']].concat(S.presets.map(p => [p.id, p.label])), d.preset)}
      </div>
      <div style="display:flex;gap:8px">
        ${inp('dv-name', 'name (lowercase)', d.name)}
        ${sel('dv-depth', [[16, '16-bit'], [24, '24-bit']], d.bit_depth, '110px')}
        ${sel('dv-rate', [[44100, '44.1 kHz'], [48000, '48 kHz']], d.sample_rate, '110px')}
        ${sel('dv-ch', [['stereo', 'stereo-preserving'], ['mono', 'mono (fold)']], d.channels, '160px')}
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        ${sel('dv-mode', [['card', 'card (mounted)'], ['staged', 'staged folder']], d.mode, '150px')}
        ${sel('dv-layout', [['mirror', 'mirror folders'], ['flatten', 'flatten (no folders)']], d.layout, '175px')}
        ${sel('dv-fs', [['', 'no filesystem rules'], ['fat32', 'fat32'], ['exfat', 'exfat']], d.filesystem, '160px')}
        ${inp('dv-dur', 'max seconds (0 = none)', d.max_duration_seconds || '', '160px')}
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        ${inp('dv-maxfiles', 'max files/folder', d.max_files_per_dir || '', '140px')}
        ${inp('dv-maxname', 'max filename chars', d.max_filename_length || '', '150px')}
        ${inp('dv-display', 'display crop (chars shown)', d.display_length || '', '170px')}
        <label style="font:400 11px var(--sans);color:var(--fg-dim);display:flex;align-items:center;gap:5px" title="names identical within the display crop get their differing tokens moved to the front">
          <input type="checkbox" id="dv-rename" ${d.rename ? 'checked' : ''}> distinguishing-first
        </label>
        <label style="font:400 11px var(--sans);color:var(--fg-dim);display:flex;align-items:center;gap:5px">
          <input type="checkbox" id="dv-san" ${d.sanitize ? 'checked' : ''}> sanitize names (# & ')
        </label>
        <label style="font:400 11px var(--sans);color:var(--fg-dim);display:flex;align-items:center;gap:5px" title="Ableton racks/presets/sets (.adg .adv .als) ride along with their sample refs rewritten to the materialized paths — for a target inside the Live User Library">
          <input type="checkbox" id="dv-comp" ${d.companions ? 'checked' : ''}> Ableton racks (.adg/.adv/.als)
        </label>
        <div style="flex:1"></div>
        <span class="restore-btn" data-act="dev-cancel">cancel</span>
        <span class="mat-btn" style="margin:0;padding:6px 16px;font-size:11px" data-act="dev-save">create device</span>
      </div>
      <div style="font:400 10px var(--sans);color:var(--fg-faint)">Presets are starting points from published specs — check them against your manual. Everything here is editable, and the .toml is yours to hand-edit after.</div>
    </div>`;
  return `<div style="font:600 9px var(--sans);color:var(--fg-faint);letter-spacing:.1em;padding:14px 2px 2px;display:flex;align-items:center;gap:10px">
      DEVICES <div style="flex:1"></div>${d ? '' : '<span class="restore-btn" data-act="dev-new">+ add device</span>'}
    </div>${form}${list}`;
}

function renderStorages() {
  const s = S.stoForm;
  const list = S.storages.map(x => `<div style="display:flex;align-items:center;gap:10px;background:var(--bg-card);border:1px solid var(--bord-card);border-radius:6px;padding:9px 12px">
      <span style="font:600 12px var(--sans);min-width:130px">${esc(x.name)}</span>
      <span style="font:400 10.5px var(--mono);color:var(--fg-faint);flex:1">${esc(x.kind)} · ${fmtB(x.capacity_bytes)}${x.reserve ? ' · reserve ' + esc(x.reserve) : ''}</span>
    </div>`).join('');
  const form = !s ? '' : `
    <div style="background:var(--bg-card);border:1px solid var(--bord-hover);border-radius:6px;padding:13px;display:flex;flex-direction:column;gap:9px">
      <div style="display:flex;gap:8px;align-items:center">
        <span style="font:600 12.5px var(--sans);flex:1">New card / storage</span>
        ${sel('st-vol', [['', 'measure a mounted volume…']].concat(S.volumes.map(v => [v.path, `${v.name} — ${fmtB(v.capacity_bytes)}`])), s.vol)}
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        ${inp('st-name', 'name (lowercase)', s.name)}
        ${inp('st-cap', 'capacity bytes', s.capacity_bytes || '', '190px')}
        ${inp('st-reserve', 'reserve (10% or bytes)', s.reserve || '10%', '170px')}
        ${sel('st-cluster', [[32768, '32 KiB clusters'], [4096, '4 KiB'], [65536, '64 KiB'], [0, 'no cluster math']], s.cluster_bytes ?? 32768, '160px')}
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <div style="flex:1;font:400 10px var(--sans);color:var(--fg-faint)">Reserve is headroom the plan refuses to fill — device recordings, sidecars, whatever else shares the card.</div>
        <span class="restore-btn" data-act="sto-cancel">cancel</span>
        <span class="mat-btn" style="margin:0;padding:6px 16px;font-size:11px" data-act="sto-save">create storage</span>
      </div>
    </div>`;
  return `<div style="font:600 9px var(--sans);color:var(--fg-faint);letter-spacing:.1em;padding:14px 2px 2px;display:flex;align-items:center;gap:10px">
      CARDS &amp; STORAGE <div style="flex:1"></div>${s ? '' : '<span class="restore-btn" data-act="sto-new">+ add storage</span>'}
    </div>${form}${list}`;
}

/* ---------- recipe ---------- */

// Layout picker: presets come from the server (view.LayoutPresets) so the
// list lives in one place; the recipe stores only the template string, so
// a hand-written template shows as "custom" and stays editable.
function layoutPick(pf, vmeta) {
  const presets = (pf && pf.layouts) || [];
  if (!presets.length) return '';
  const cur = vmeta.layout || '';
  if (S.layoutEdit) {
    return `${inp('lay-tpl', '{family}/{instrument}/{category}/{pack}/{file}', cur, '360px')}
      <span class="restore-btn" data-act="lay-cancel">cancel</span>
      <span class="mat-btn" style="margin:0;padding:4px 12px;font-size:11px" data-act="lay-save">set layout</span>`;
  }
  const known = presets.some(p => p.template === cur);
  const opts = presets.map(p => `<option value="${esc(p.template)}" ${p.template === cur ? 'selected' : ''}>${esc(p.label)}</option>`).join('')
    + `<option value="__custom" ${!known ? 'selected' : ''}>${!known ? 'custom: ' + esc(cur) : 'custom template…'}</option>`;
  return `<select id="layout-pick" title="how output folders are laid out — every file's path comes from this" style="font:500 11.5px var(--mono);color:var(--fg-dim);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:3px 8px;max-width:300px">${opts}</select>`;
}

function layoutHint(pf, vmeta) {
  const presets = (pf && pf.layouts) || [];
  const cur = vmeta.layout || '';
  const p = presets.find(x => x.template === cur);
  const ex = p ? p.example : (cur ? cur : '');
  return ex ? ` <span style="color:var(--fg-ghost)">Layout → <span style="font-family:var(--mono)">${esc(ex)}</span></span>` : '';
}

async function setLayout(tpl) {
  const ok = await viewAction({ action: 'set-layout', name: S.view, layout: tpl });
  if (ok) { S.pf = null; loadPreflight(); } else render();
}

function renderRecipe() {
  if (!S.pf && !S.pfBusy) loadPreflight();
  const pf = S.pf;
  const viewOpts = S.views.map(v => `<option ${v.name === S.view ? 'selected' : ''}>${esc(v.name)}</option>`).join('');
  const vmeta = S.views.find(v => v.name === S.view) || {};
  const nr = S.newRecipe;
  const nrForm = !nr ? '' : `
    <div style="background:var(--bg-card);border:1px solid var(--bord-hover);border-radius:6px;padding:13px;display:flex;flex-direction:column;gap:9px;margin-bottom:12px;max-width:640px">
      <span style="font:600 12.5px var(--sans)">New recipe</span>
      <div style="display:flex;gap:8px">
        ${inp('nr-name', 'name (letters, digits, - _)', '')}
        ${sel('nr-device', S.devices.map(d => [d.name, d.name]), nr.device, '160px')}
        ${sel('nr-storage', S.storages.map(s => [s.name, s.name]), nr.storage, '170px')}
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        ${inp('nr-target', 'target folder — optional', nr.target || '')}
        <span class="restore-btn" data-act="nr-browse" title="browse for a folder">browse…</span>
        <span class="restore-btn" data-act="recipe-cancel">cancel</span>
        <span class="mat-btn" style="margin:0;padding:6px 16px;font-size:11px" data-act="recipe-create">create</span>
      </div>
      ${!S.devices.length || !S.storages.length ? '<div style="font:400 10.5px var(--sans);color:var(--warn)">Add a device and a storage profile first — Setup (⌘5).</div>' : ''}
    </div>`;
  const head = `
    <div style="display:flex;align-items:center;gap:10px;margin-bottom:4px">
      <span style="font:600 14px var(--sans)">Recipe</span>
      <select id="view-pick" style="font:500 11.5px var(--mono);color:var(--fg-dim);background:var(--bg-raise);border:1px solid var(--bord-raise);border-radius:4px;padding:3px 8px">${viewOpts}</select>
      ${pf ? `<span class="rtag dev">${esc(pf.device)}</span><span class="rtag" style="cursor:default">${esc(pf.storage)}</span>` : ''}
      <span class="rtag" data-act="set-target" title="choose the folder this recipe materializes into">${vmeta.target ? esc(vmeta.target) : '+ set target'}</span>
      ${layoutPick(pf, vmeta)}
      ${S.renaming ? `${inp('rn-name', 'new name', S.view, '160px')}<span class="restore-btn" data-act="rename-cancel">cancel</span><span class="mat-btn" style="margin:0;padding:4px 12px;font-size:11px" data-act="rename-save">rename</span>`
        : `<span class="restore-btn" data-act="rename-start" title="rename this recipe">rename</span>`}
      <div style="flex:1"></div>
      <span class="restore-btn" data-act="recipe-new">+ new recipe</span>
    </div>
    <div style="font:400 11px var(--sans);color:var(--fg-faint);margin-bottom:2px">One row per vendor: check it to take everything they made, open ▸ to pick packs. Rules are written for you.${layoutHint(pf, vmeta)}</div>
    ${nrForm}`;

  if (!pf || pf.error || !pf.rules) return `<div class="recipe-grid"><div class="recipe-left">${head}
    <div style="font:400 11px var(--mono);color:${pf && pf.error ? 'var(--warn)' : 'var(--fg-faint)'};padding:24px 4px">${pf && pf.error ? 'plan failed: ' + esc(pf.error) : esc(pfProgressLabel())}</div></div>
    <div class="preflight"></div></div>`;

  // The recipe as vendors, not as [[include]] blocks. S.rModel is what the
  // click handlers act on — rendered and clicked state are the same object.
  const M = S.rModel = recipeGroups(pf);
  const filt = S.rFilter.trim().toLowerCase();
  const hit = (g) => !filt || g.key.toLowerCase().includes(filt) || g.packs.some(e => e.p.name.toLowerCase().includes(filt));
  const shown = M.groups.filter(hit);
  const nRules = pf.rules.length;
  const canTidy = M.groups.filter(g => g.collapsible);
  const after = nRules - canTidy.reduce((a, g) => a + g.rules.length - 1, 0);
  const tidyBar = !canTidy.length ? '' : `<div class="tidy">
      <span style="flex:1">${n(nRules)} rules where <b style="color:var(--fg)">${n(after)}</b> would do — ${canTidy.map(g => esc(g.key)).join(', ')} ${canTidy.length === 1 ? 'is' : 'are'} fully in. Collapsing changes nothing about what gets picked, and packs you buy later fall in on their own.</span>
      <span class="restore-btn" data-act="tidy" style="white-space:nowrap">tidy → ${n(after)} ${after === 1 ? 'rule' : 'rules'}</span>
    </div>`;

  const packRow = (g, e, j) => `<div class="pk ${e.in ? 'on' : ''} ${e.cut ? 'cut' : ''}" data-act="pk" data-g="${esc(g.key)}" data-j="${j}" title="${esc(e.p.dir)}">
      <span class="ck ${e.in ? (e.some ? 'part' : 'on') : ''}">${e.in && !e.some ? '✓' : ''}</span>
      <span class="pname">${esc(e.p.name)}</span>
      ${e.some ? '<span class="pnum" style="color:var(--warn)">part of it</span>' : ''}
      ${e.cut ? '<span class="pnum" style="color:var(--warn)">excluded</span>' : ''}
      <span class="pnum">${n(e.p.files)} files</span>
    </div>`;

  const groupRow = (g) => {
    const open = S.rOpen.has(g.key);
    const ckCls = g.state === 'all' ? 'ck on' : g.state === 'partial' ? 'ck part' : 'ck';
    const nr = g.rules.length > 1 ? ` · ${n(g.rules.length)} rules` : '';
    const np = `${n(g.packs.length)} ${g.packs.length === 1 ? 'pack' : 'packs'}`;
    const sub = g.state === 'none' ? `${np} · not in this recipe`
      : g.state === 'all' ? `${g.packs.length === 1 ? np : 'all ' + np} · ${esc(g.location)}${nr}`
      : `${n(g.in)} of ${np} · ${esc(g.location)}${nr}`;
    const packs = !open ? '' : `<div class="packs">${g.packs.map((e, j) => packRow(g, e, j)).join('')}</div>`;
    return `<div class="rule head ${g.state === 'none' ? 'off' : ''}">
      <span data-act="grp" data-g="${esc(g.key)}" style="position:absolute;inset:0;cursor:pointer" title="${g.state === 'all' ? 'take this vendor out of the recipe' : 'put everything this vendor made in'}"></span>
      <span class="${ckCls}">${g.state === 'all' ? '✓' : ''}</span>
      <div class="body"><span class="rname">${esc(g.key)}</span><span class="rpath">${sub}</span></div>
      ${g.state === 'none' ? '' : `<span class="match">${n(g.files)} files · ${fmtB(g.bytes)}</span>`}
      ${g.collapsible ? `<span class="restore-btn" data-act="collapse" data-g="${esc(g.key)}" style="position:relative;z-index:1;margin:0" title="replace these ${n(g.rules.length)} rules with one">collapse to 1</span>` : ''}
      <span class="caret" data-act="grp-open" data-g="${esc(g.key)}" style="position:relative;z-index:1;cursor:pointer;padding:0 3px" title="${open ? 'hide packs' : 'show packs'}">${open ? '▾' : '▸'}</span>
    </div>${packs}`;
  };

  // Anything the library can't explain stays visible and removable rather
  // than disappearing into a vendor row that doesn't cover it.
  const extras = !M.extras.length ? '' : `
    <div style="font:400 10.5px var(--mono);color:var(--fg-faint);margin:14px 0 -4px">rules that don't map to a pack in your library</div>
    ${M.extras.map(x => `<div class="rule extra">
      <div class="body"><span class="rname">${esc(x.r.as || x.r.location)}</span><span class="rpath">${esc(x.r.location)} : ${esc(x.r.glob)}</span></div>
      <span class="match">${n(x.r.files)} files · ${fmtB(x.r.converted_bytes)}</span>
      <span data-act="rule-remove" data-i="${x.i}" title="remove this rule from the recipe" style="font:500 12px var(--mono);color:var(--fg-ghost);cursor:pointer;padding:0 2px">✕</span>
    </div>`).join('')}`;

  const rules = shown.map(groupRow).join('') || `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:18px 4px">${S.packs.length ? 'nothing matches that filter' : 'no packs indexed yet — add a source in Setup'}</div>`;

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
      ...errors.map(t => `<div class="issue err" data-act="issue-toggle" title="click to expand"><span class="k">ERROR</span><span class="t">${esc(t)}</span></div>`),
      ...warnings.map(t => `<div class="issue warn" data-act="issue-toggle" title="click to expand"><span class="k">WARN</span><span class="t">${esc(t)}</span></div>`),
    ].join('') || `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:6px 2px">0 issues — clean pre-flight</div>`;
    right = `
      <div style="display:flex;align-items:baseline;gap:10px;margin-bottom:12px">
        <span style="font:600 12.5px var(--sans)">Pre-flight</span>
        <span style="font:400 11px var(--mono);color:var(--fg-faint)">${esc(pf.storage)} · ${fmtB(cap)}</span>
        <div style="flex:1"></div>
        <span style="font:700 12px var(--mono);color:${fitColor}">${fitLabel}</span>
        ${S.pfBusy ? `<span style="font:400 11px var(--mono);color:var(--fg-faint)">${esc(pfProgressLabel())}</span>` : ''}
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
      <div class="issues">${issues}</div>
      <div class="mat-btn" data-act="go-plan">PLAN → ${n(S.pf.files ?? 0)} FILES${(pf.plan && (pf.plan.unsorted || pf.plan.uncategorized || pf.plan.general)) ? ` · ${n((pf.plan.unsorted||0) + (pf.plan.uncategorized||0) + (pf.plan.general||0))} NEED A DECISION` : ''}</div>
      <div style="font:400 10px var(--mono);color:var(--fg-faint);margin-top:8px;text-align:center">see where every file lands and fix what's wrong — materialize is the plan's exit</div>`;
  } else {
    right = `<div style="font:400 11px var(--mono);color:var(--fg-faint);padding:24px 4px">nothing selected yet — check a vendor on the left</div>`;
  }

  const bar = `<div style="display:flex;align-items:center;gap:10px;margin-top:10px">
      <div class="search" style="flex:none;width:230px">⌕ <input id="rfilter" placeholder="Filter vendors and packs…" value="${esc(S.rFilter)}"></div>
      <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">${n(M.groups.filter(g => g.state !== 'none').length)} of ${n(M.groups.length)} vendors in · ${n(nRules)} ${nRules === 1 ? 'rule' : 'rules'} written${M.cuts.size ? ` · ${n(M.cuts.size)} excluded` : ''}</span>
    </div>`;
  return `<div class="recipe-grid">
    <div class="recipe-left">${head}${tidyBar}${bar}<div class="rules">${rules}${extras}</div></div>
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
      <span style="font:600 14px var(--sans)">${r.verb === 'migrate' ? 'Migrate' : 'Materialize'}</span>
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
    ${!running && !isDone ? `<div class="go-btn" data-act="start-run">START RUN — ${esc(S.view || '')}</div>` : ''}
    ${!running ? `<div style="display:flex;gap:8px;margin:10px 0"><span class="pl-btn" data-act="back-plan">← back to the plan${isDone ? ' — it re-reads the lock' : ''}</span><span class="pl-btn" data-act="tab" data-k="cards">history &amp; diff</span></div>` : ''}
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


/* ---------- plan: the review surface (SPEC §19.2) ---------- */

const KIND_LABEL = { unsorted: 'no instrument', uncategorized: 'loop or one-shot?', general: 'family only' };
const KIND_ASK = {
  unsorted: 'Nothing on the path names an instrument. Say what this folder holds — at whatever depth you honestly know — or leave it.',
  uncategorized: 'The instrument is known; nothing says loop or one-shot. One answer for the folder, or a default that lets any labelled file keep its own word.',
  general: 'Only the family is known. Name the instrument if there is one — numbered takes often have none, and leaving it is the honest answer.',
};
const TIER_LABEL = {
  'dir': 'pack [[dir]]', 'dir-default': 'pack [[dir]] default', 'dedicated-pack': 'vendor dedicated_packs', 'vendor-category': 'vendor [[category]]',
  'categories': 'categories.toml', 'multisample': 'multisample shape of the directory', 'pack-instrument': 'pack [[instrument]]',
  'vendor-instrument': 'vendor [[instrument]]', 'override': 'override', 'compound': 'compound segment → family catch-all', 'lexicon': 'instruments.toml',
  'pack-code': 'pack [[instrument]] code', 'vendor-code': 'vendor [[instrument]] code', 'override-code': 'override code', 'lexicon-code': 'instruments.toml code',
  'demoted': 'word demoted (its category disagrees) → family catch-all',
};
function whySrc(src) {
  if (!src) return '<span class="t">nothing spoke</span>';
  let out = `<span class="t">${esc(TIER_LABEL[src.tier] || src.tier)}</span>`;
  if (src.word) out += ` "${esc(src.word)}"`;
  if (src.segment) out += ` <span class="t">on</span> "${esc(src.segment)}"${src.echo ? ' <span class="t">(pack-name echo)</span>' : ''}`;
  return out;
}
function whyPanel(f) {
  const w = f.why || {};
  return `<div class="why">
    <div><b>category</b> ${esc(f.category || '—')} · ${whySrc(w.category)}</div>
    <div><b>instrument</b> ${esc(f.instrument || '—')}${f.family && f.family !== f.instrument ? ` (${esc(f.family)})` : ''} · ${whySrc(w.instrument)}</div>
  </div>`;
}

async function loadPlanReview() {
  const pl = S.pl;
  if (!S.view || pl.busy) return;
  pl.busy = true; render();
  try {
    if (!S.pf) await loadPreflight();
    if (!S.pf || S.pf.error) return;
    if (!pl.lex) pl.lex = await api('/api/lexicon');
    pl.local = await api('/api/local');
    if (pl.tab === 'queues') {
      const q = new URLSearchParams({ view: S.view });
      if (pl.kind) q.set('kind', pl.kind);
      pl.q = await api('/api/plan/queues?' + q);
    } else {
      pl.tree = await api('/api/plan/tree?' + new URLSearchParams({ view: S.view, prefix: pl.prefix }));
    }
  } finally { pl.busy = false; render(); }
}

async function openQueueRow(i) {
  const pl = S.pl;
  const row = pl.q.rows[i];
  pl.sel = row; pl.file = null; pl.files = null; pl.radius = null; pl.msg = '';
  // the default action per kind: a category answer, an instrument, or a name
  pl.form = { location: row.location, path: row.folder, facet: row.kind === 'uncategorized' ? 'category' : 'instrument',
    value: '', mode: row.kind === 'unsorted' ? 'default' : 'pin', note: '', local: false, word: '' };
  render();
  pl.files = await api('/api/plan/folder?' + new URLSearchParams({ view: S.view, location: row.location, folder: row.folder }));
  render();
}

function openTreeFile(f) {
  const pl = S.pl;
  pl.file = f; pl.sel = null; pl.radius = null; pl.msg = '';
  const folder = f.source_path.split('/').slice(0, -1).join('/');
  pl.form = { location: f.location, path: folder, facet: 'instrument', value: '', mode: 'pin', note: '', local: false,
    word: (f.why && f.why.instrument && f.why.instrument.word) || '' };
  render();
}

function readForm() {
  const f = S.pl.form; if (!f) return null;
  const g = (id) => document.getElementById(id);
  if (g('pl-note')) f.note = g('pl-note').value;
  if (g('pl-word')) f.word = g('pl-word').value;
  if (g('pl-local')) f.local = g('pl-local').checked;
  if (g('pl-path')) f.path = g('pl-path').value;
  if (g('pl-value')) f.value = g('pl-value').value;
  return f;
}

async function planCorrect(preview) {
  const f = readForm(); if (!f) return;
  const pl = S.pl;
  if (!f.value && f.facet !== 'role') { pl.msg = 'pick a value first'; render(); return; }
  pl.busy = true; pl.msg = ''; render();
  const body = { location: f.location, path: f.path, facet: f.facet, value: f.value, mode: f.mode, note: f.note, local: f.local, word: f.word, preview };
  const r = await api('/api/correct', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  pl.busy = false;
  if (r.error) { pl.msg = r.error; render(); return; }
  pl.radius = r.radius;
  if (!preview) {
    pl.msg = `written to annotations.local/${r.radius.target.file} — re-planning`;
    pl.sel = null; pl.file = null; pl.form = null; pl.radius = null;
    S.pf = null; render();
    await loadPreflight();
    await loadPlanReview();
    return;
  }
  render();
}

async function planAck() {
  const pl = S.pl; const f = readForm(); if (!pl.sel) return;
  await api('/api/ack', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ location: pl.sel.location, folder: pl.sel.folder, note: f ? f.note : '' }) });
  pl.msg = 'left as-is — it will not come back to the queue'; pl.sel = null; pl.form = null; pl.files = null;
  await loadPlanReview();
}

async function planReport() {
  const f = readForm(); if (!f) return;
  await api('/api/report', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ location: f.location, path: S.pl.file ? S.pl.file.source_path : f.path, note: f.note, value: f.value }) });
  S.pl.msg = 'reported — logged to annotations.local/corrections.jsonl with what the app resolved and why'; render();
}

async function planReconcile() {
  const pl = S.pl;
  pl.recBusy = true; pl.rec = null; render();
  pl.rec = await api('/api/local/reconcile');
  pl.recBusy = false; render();
}

async function planDrop(entries) {
  const pl = S.pl;
  if (!entries.length) return;
  pl.recBusy = true; render();
  const r = await api('/api/local/drop', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ entries, reason: 'upstream agrees' }) });
  pl.recBusy = false;
  pl.msg = r.error ? r.error : `dropped ${r.dropped} — the checkout says the same now`;
  pl.local = await api('/api/local');
  await planReconcile();
}

function renderReconcile() {
  const pl = S.pl;
  if (!pl.local || !pl.local.entries || !pl.local.entries.length) return '';
  const rec = pl.rec;
  const head = `<div style="display:flex;align-items:center;gap:8px;margin-top:6px">
      <span style="font:600 10px var(--sans);color:var(--fg-faint);letter-spacing:.05em;text-transform:uppercase">reconcile</span>
      <span class="pl-btn" data-act="pl-reconcile">${pl.recBusy ? 'checking…' : 'check against the checkout'}</span>
      ${rec && rec.redundant ? `<span class="pl-btn go" data-act="pl-drop-all">drop ${rec.redundant} redundant</span>` : ''}
    </div>`;
  if (!rec) return head + `<div style="font:400 10.5px var(--sans);color:var(--fg-faint)">an entry the repo now says itself is a shadow — remove it and nothing moves. This finds those.</div>`;
  const rows = (rec.verdicts || []).map((v, i) => {
    const what = v.kind === 'dir' ? esc(v.entry.path || '') : 'word ' + esc((v.entry.aliases || []).join(', '));
    const state = v.unmatched ? '<span style="color:var(--fg-faint)">no files under it</span>'
      : v.redundant ? `<span style="color:var(--green)">redundant — ${n(v.covered)} files, nothing moves</span>`
      : `<span style="color:var(--amber)">still needed — ${n(v.changed)} of ${n(v.covered)} would move</span>`;
    return `<div style="display:flex;gap:8px;align-items:center;font:400 10.5px var(--mono);color:var(--fg-dim)">
        <span style="flex:1;min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${esc(v.vendor)}/${esc(v.pack)} · ${what}</span>${state}
        ${v.redundant || v.unmatched ? `<span class="pl-btn" style="padding:2px 7px" data-act="pl-drop" data-i="${i}">drop</span>` : ''}
      </div>`;
  }).join('');
  return head + rows;
}

function instrumentOptions(lex, current) {
  if (!lex) return '';
  const fams = {};
  for (const i of lex.instruments || []) (fams[i.family || '?'] ||= []).push(i);
  return Object.keys(fams).sort().map(fam => `<optgroup label="${esc(fam)}">${fams[fam].map(i =>
    `<option value="${esc(i.id)}" ${i.id === current ? 'selected' : ''}>${esc(i.display || i.id)}${i.id === fam ? ' (family catch-all)' : ''}</option>`).join('')}</optgroup>`).join('');
}

function renderPlanForm() {
  const pl = S.pl, f = pl.form, lex = pl.lex;
  if (!f) return '';
  const facet = f.facet;
  const facetSeg = ['instrument', 'category', 'alias', 'role'].map(k =>
    `<span class="${facet === k ? 'on' : ''}" data-act="pl-facet" data-k="${k}">${k === 'alias' ? 'word means' : k === 'role' ? 'skip' : k}</span>`).join('');
  let value = '';
  if (facet === 'category') {
    value = `<div class="pl-btns">${(lex ? lex.categories : ['loops', 'one-shots']).map(c =>
      `<span class="pl-btn ${f.value === c ? 'on' : ''}" data-act="pl-value" data-v="${esc(c)}">${esc(c)}</span>`).join('')}</div>`;
  } else if (facet === 'instrument' || facet === 'alias') {
    value = `<select id="pl-value" data-act="pl-value-sel"><option value="">— pick an instrument —</option>${instrumentOptions(lex, f.value)}</select>`;
  } else if (facet === 'role') {
    value = `<div class="pl-btns">${['format-tree', 'docs'].map(c =>
      `<span class="pl-btn ${f.value === c ? 'on' : ''}" data-act="pl-value" data-v="${c}">${c}</span>`).join('')}</div>
      <div style="font:400 10.5px var(--sans);color:var(--fg-faint)">marks the folder as not content: a parallel sampler export, or manuals and artwork. Nothing under it materializes.</div>`;
  }
  const mode = (facet === 'category' || facet === 'instrument') ? `
    <label>how it applies</label>
    <div class="seg"><span class="${f.mode === 'pin' ? 'on' : ''}" data-act="pl-mode" data-k="pin">pin — beats the filenames</span><span class="${f.mode === 'default' ? 'on' : ''}" data-act="pl-mode" data-k="default">default — only where nothing spoke</span></div>` : '';
  const word = facet === 'alias' ? `
    <label>the word, as written in this pack</label>
    <input type="text" id="pl-word" value="${esc(f.word)}" placeholder="e.g. Bass">
    <div style="font:400 10.5px var(--sans);color:var(--fg-faint)">a pack [[instrument]] block: inside this pack only, this word means the instrument above. Drumtrax's "Bass" is its kick.</div>` : '';
  const rad = pl.radius ? `<div class="radius">
      <div><b>${n(pl.radius.covered)}</b> files covered · <b>${n(pl.radius.changed)}</b> change · <b>${n(pl.radius.filled)}</b> filled in${pl.radius.moved ? ` · <span class="mv"><b>${n(pl.radius.moved)}</b> currently resolve elsewhere</span>` : ''}</div>
      ${(pl.radius.changes || []).slice(0, 6).map(c => `<div>${n(c.count)} × ${esc(c.from)} → ${esc(c.to)}<div class="t" style="color:var(--fg-faint);padding-left:12px">${c.examples.map(e => esc(e.split('/').pop())).join(' · ')}</div></div>`).join('')}
      ${pl.radius.changed === 0 ? '<div class="t" style="color:var(--fg-faint)">nothing would move — the layer already says this</div>' : ''}
      <div class="t" style="color:var(--fg-faint)">→ annotations.local/${esc(pl.radius.target.file)}${pl.radius.target.new_vendor ? ' (new vendor)' : pl.radius.target.new_pack ? ' (new pack)' : ''}</div>
    </div>` : '';
  return `<div class="pl-form">
    <label>correct</label>
    <div class="seg">${facetSeg}</div>
    <label>${facet === 'alias' ? 'means' : 'is'}</label>
    ${value}
    ${word}
    ${mode}
    <label>covers</label>
    <input type="text" id="pl-path" value="${esc(f.path)}" title="the folder, or a glob within the pack (WAV/Textures/Chop *.wav)">
    <label>note — the evidence</label>
    <input type="text" id="pl-note" value="${esc(f.note)}" placeholder="all 143 are chops; the folder name lies">
    <div style="display:flex;align-items:center;gap:6px;font:400 11px var(--sans);color:var(--fg-dim)"><input type="checkbox" id="pl-local" ${f.local ? 'checked' : ''}> my opinion only — keep it out of the export</div>
    ${rad}
    <div class="pl-btns">
      <span class="pl-btn" data-act="pl-preview">preview — what moves</span>
      <span class="pl-btn go" data-act="pl-apply">${pl.radius ? 'apply' : 'apply (previews first)'}</span>
      ${pl.sel ? '<span class="pl-btn" data-act="pl-ack" title="reviewed, leave it as-is">leave it</span>' : ''}
      <span class="pl-btn warn" data-act="pl-report" title="log it as a parser bug — no annotation written">this is the parser</span>
    </div>
    ${pl.msg ? `<div style="font:500 11px var(--mono);color:var(--amber)">${esc(pl.msg)}</div>` : ''}
  </div>`;
}

// The plan's verdict and its exits: fit, issues, materialize or migrate.
function renderVerdict() {
  const pf = S.pf, p = pf && pf.plan;
  if (!p) return S.pfBusy ? `<div style="font:500 11px var(--mono);color:var(--fg-faint)">${esc(pfProgressLabel())}</div>` : '';
  const fits = p.fits, warnings = p.warnings || [], errors = p.errors || [];
  const fitLabel = !fits ? "WON'T FIT" : (warnings.length || errors.length) ? `FITS · ${warnings.length + errors.length} issues` : 'FITS · clean';
  const fitColor = !fits ? 'var(--err)' : (warnings.length || errors.length) ? 'var(--warn)' : 'var(--green)';
  const issues = [
    ...errors.map(t => `<div class="issue err" data-act="issue-toggle" title="click to expand"><span class="k">ERROR</span><span class="t">${esc(t)}</span></div>`),
    ...warnings.map(t => `<div class="issue warn" data-act="issue-toggle" title="click to expand"><span class="k">WARN</span><span class="t">${esc(t)}</span></div>`),
  ].join('');
  return `<div style="display:flex;align-items:baseline;gap:10px">
      <span style="font:600 12.5px var(--sans)">Verdict</span>
      <span style="font:400 11px var(--mono);color:var(--fg-faint)">${esc(pf.storage)} · ${fmtB(p.total_on_disk ?? 0)} of ${fmtB(p.usable_bytes ?? 0)} usable</span>
      <div style="flex:1"></div>
      <span style="font:700 12px var(--mono);color:${fitColor}">${fitLabel}</span>
    </div>
    ${issues ? `<div class="issues" style="max-height:160px;overflow:auto">${issues}</div>` : ''}
    <div class="mat-btn ${!fits || errors.length ? 'blocked' : ''}" style="margin:0" data-act="go-run">MATERIALIZE — ${n(pf.files ?? 0)} FILES</div>
    ${pf.migrate ? `<div class="mat-btn ${errors.length ? 'blocked' : ''}" style="margin:0" data-act="go-migrate">MIGRATE — MOVE ${n(pf.migrate.moves + pf.migrate.companions)} FILES INTO THE NEW LAYOUT</div>
    <div style="font:400 10px var(--mono);color:var(--fg-faint);text-align:center">renames the last materialize in place — nothing re-rendered, no duplicates, emptied folders removed</div>` : ''}
    <div style="font:400 10px var(--mono);color:var(--fg-faint);text-align:center">writes to the recipe's target — exactly what the plan shows</div>`;
}

function renderPlan() {
  const pl = S.pl;
  if (!S.view) return `<div class="run-wrap"><div style="font:400 12px var(--sans);color:var(--fg-faint)">Pick a recipe first — the plan is built from one.</div></div>`;
  const need = !S.pf ? true : (pl.tab === 'queues' ? !pl.q : !pl.tree);
  if (need && !pl.busy && !S.pfBusy && !(S.pf && S.pf.error)) setTimeout(loadPlanReview, 0);
  const counts = (pl.q && pl.q.kinds) || {};
  const plan = (S.pf && S.pf.plan) || {};
  const kindChips = ['', 'uncategorized', 'unsorted', 'general'].map(k => {
    const c = k ? (plan[k] || 0) : ((plan.unsorted || 0) + (plan.uncategorized || 0) + (plan.general || 0));
    return `<span class="chip ${pl.kind === k ? 'on' : ''}" data-act="pl-kind" data-k="${k}" style="${pl.kind === k ? 'color:var(--fg);border-color:var(--bord-hover)' : ''}">${k ? KIND_LABEL[k] : 'all'} · ${n(c)}</span>`;
  }).join('');
  const head = `<div style="display:flex;align-items:center;gap:10px;margin-bottom:10px;flex-wrap:wrap">
      <span style="font:600 14px var(--sans)">Plan</span>
      <span style="font:500 11px var(--mono);color:var(--fg-faint)">${esc(S.view)} · ${n(S.pf ? S.pf.files || 0 : 0)} files${S.pfBusy ? ' · ' + esc(pfProgressLabel()) : ''}</span>
      <div style="flex:1"></div>
      <div class="seg"><span class="${pl.tab === 'queues' ? 'on' : ''}" data-act="pl-tab" data-k="queues">Queues</span><span class="${pl.tab === 'tree' ? 'on' : ''}" data-act="pl-tab" data-k="tree">Tree</span></div>
      ${pl.local ? `<span class="chip" data-act="pl-export" title="zip of annotations.local minus your local-only entries — drop it in the channel">local layer · ${n((pl.local.entries || []).length)} entries · export</span>` : ''}
    </div>
    ${pl.tab === 'queues' ? `<div style="display:flex;gap:6px;margin-bottom:8px;flex-wrap:wrap">${kindChips}</div>` : ''}`;

  let list = '';
  if (pl.tab === 'queues') {
    const rows = (pl.q && pl.q.rows) || [];
    list = rows.length ? rows.map((r, i) => `<div class="pl-row ${pl.sel === r ? 'on' : ''}" data-act="pl-row" data-i="${i}">
        <div><div class="f" title="${esc(r.folder)}">${esc(r.folder)}</div><div class="ex">${r.instrument ? esc(r.instrument) + ' · ' : ''}${r.category ? esc(r.category) + ' · ' : ''}${r.examples.map(esc).join(' · ')}</div></div>
        <div class="n">${n(r.count)}</div>
        <div><span class="pl-tag ${r.kind}">${KIND_LABEL[r.kind] || r.kind}</span></div>
      </div>`).join('') + (pl.q.total_rows > rows.length ? `<div style="font:400 10.5px var(--mono);color:var(--fg-faint);padding:8px 10px">… ${n(pl.q.total_rows - rows.length)} more folders — decide these first</div>` : '')
      : `<div style="font:400 11.5px var(--sans);color:var(--fg-faint);padding:24px 10px">${(S.pf && S.pf.error) ? 'plan failed: ' + esc(S.pf.error) : (pl.q && pl.q.error) ? esc(pl.q.error) : (pl.busy || S.pfBusy) ? 'building the plan…' : 'nothing waiting — every file the layout reads has a place'}</div>`;
  } else {
    const t = pl.tree;
    const parts = pl.prefix ? pl.prefix.split('/') : [];
    const crumb = `<div class="crumb"><span data-act="pl-crumb" data-p="">${esc(S.view)}</span>${parts.map((p, i) => ` / <span data-act="pl-crumb" data-p="${esc(parts.slice(0, i + 1).join('/'))}">${esc(p)}</span>`).join('')}${t ? ` <span style="color:var(--fg-faint)">· ${n(t.total)} files</span>` : ''}</div>`;
    list = crumb + (t ? (t.dirs.map(d => `<div class="pl-dir" data-act="pl-dir" data-name="${esc(d.name)}"><span>${esc(d.name)}/</span><span style="color:var(--fg-faint)">${n(d.count)}</span></div>`).join('')
      + t.files.map((f, i) => `<div class="pl-file ${pl.file === f ? 'on' : ''}" data-act="pl-file" data-i="${i}">
          <span class="play-btn" data-act="pl-play" data-path="${esc(f.source_path)}" data-loc="${esc(f.location)}">${S.player && S.player.path === f.source_path && S.player.playing ? '❚❚' : '▶'}</span>
          <span class="nm" title="${esc(f.source_path)}">${esc(f.name)}</span>
          <span style="color:var(--fg-faint)">${esc(f.instrument || '—')}</span><span style="color:var(--fg-faint)">${esc(f.category || '—')}</span>
        </div>`).join('') + (t.total > t.dirs.reduce((a, d) => a + d.count, 0) + t.files.length ? `<div style="font:400 10.5px var(--mono);color:var(--fg-faint);padding:8px 4px">… more files at this level</div>` : ''))
      : `<div style="font:400 11.5px var(--sans);color:var(--fg-faint);padding:24px 10px">building the plan…</div>`);
  }

  let panel = '';
  if (pl.sel) {
    const r = pl.sel;
    const files = pl.files ? pl.files.files : [];
    panel = `<div style="font:600 12px var(--sans)">${esc(r.folder.split('/').pop())} <span style="font:400 10.5px var(--mono);color:var(--fg-faint)">· ${esc(r.pack_path)} · ${n(r.count)} files</span></div>
      <div style="font:400 11px var(--sans);color:var(--fg-dim)">${esc(KIND_ASK[r.kind] || '')}</div>
      ${whyPanel({ category: r.category, instrument: r.instrument, family: r.family, why: r.why })}
      <div style="max-height:180px;overflow:auto;border:1px solid var(--bord);border-radius:5px">${files.map((f, i) => `<div class="pl-file ${pl.file === f ? 'on' : ''}" data-act="pl-qfile" data-i="${i}">
          <span class="play-btn" data-act="pl-play" data-path="${esc(f.source_path)}" data-loc="${esc(f.location)}">${S.player && S.player.path === f.source_path && S.player.playing ? '❚❚' : '▶'}</span>
          <span class="nm">${esc(f.name)}</span><span style="color:var(--fg-faint)">${esc(f.instrument || '—')}</span><span style="color:var(--fg-faint)">${esc(f.category || '—')}</span>
        </div>`).join('') || `<div style="padding:8px;font:400 10.5px var(--mono);color:var(--fg-faint)">${pl.files ? 'no files' : 'loading…'}</div>`}</div>
      ${pl.file ? whyPanel(pl.file) : ''}
      ${renderPlanForm()}`;
  } else if (pl.file) {
    const f = pl.file;
    panel = `<div style="font:600 12px var(--sans)">${esc(f.name)}</div>
      <div style="font:400 10.5px var(--mono);color:var(--fg-faint)">${esc(f.source_path)}<br>→ ${esc(f.out_path)}</div>
      ${whyPanel(f)}
      <div style="font:400 11px var(--sans);color:var(--fg-dim)">Wrong? The why says which level answered. Fix that level: the folder (a pin), a word that means something else in this pack (word means), or report it as the parser's mistake.</div>
      ${renderPlanForm()}`;
  } else {
    panel = `${renderVerdict()}
      <div style="font:400 11.5px var(--sans);color:var(--fg-faint)">${pl.tab === 'queues' ? 'Pick a folder. One decision per folder — the files inside are there to listen to, not to label one by one.' : 'Walk the tree as it will be written. Click a file to see why it landed there; confident misfiles live here, no queue holds them.'}</div>
      ${pl.msg ? `<div style="font:500 11px var(--mono);color:var(--green)">${esc(pl.msg)}</div>` : ''}
      ${pl.local && pl.local.entries && pl.local.entries.length ? `<div style="font:600 10px var(--sans);color:var(--fg-faint);letter-spacing:.05em;text-transform:uppercase;margin-top:6px">your local layer</div>
        ${pl.local.entries.slice(0, 12).map(e => `<div style="font:400 10.5px var(--mono);color:var(--fg-dim)">${esc(e.vendor)}/${esc(e.pack)} · ${e.kind === 'dir' ? esc(e.entry.path || '') : 'word ' + esc((e.entry.aliases || []).join(', '))} → ${esc(e.entry.category || e.entry.default_category || e.entry.instrument || e.entry.default_instrument || e.entry.role || e.entry.id || '')}${e.entry.local ? ' <span style="color:var(--fg-faint)">(local only)</span>' : ''}</div>`).join('')}
        <div style="font:400 10.5px var(--sans);color:var(--fg-faint)">${esc(pl.local.dir)}</div>
        ${renderReconcile()}` : ''}`;
  }
  return `<div class="pl-wrap">${head ? `<div class="pl-list">${head}${list}</div>` : ''}<div class="pl-panel">${panel}</div></div>`;
}

/* ---------- events ---------- */

function wire() {
  $app.querySelectorAll('[data-act]').forEach(el => {
    el.addEventListener('click', (e) => {
      const act = el.dataset.act;
      if (act === 'tab') {
        const k = el.dataset.k;
        if (!stepAllowed(k)) { if (k === 'plan') { S.screen = 'recipe'; render(); } return; }
        stopPlayback(); S.packOpen = null; S.pd = null; S.screen = k; if (S.screen === 'cards') { S.locks = []; } render();
      }
      if (act === 'go-recipe') { stopPlayback(); S.packOpen = null; S.pd = null; S.screen = 'recipe'; render(); }
      if (act === 'back-plan') { S.screen = 'plan'; S.pf = null; S.pl.q = null; S.pl.tree = null; render(); }
      if (act === 'issue-toggle') { if (!String(window.getSelection())) el.classList.toggle('open'); } // a click that selected text is a copy, not a toggle
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
      if (act === 'loc') { S.locFilter = (S.locFilter === el.dataset.l) ? '' : el.dataset.l; render(); }
      if (act === 'disc-on') { S.discover = true; stopPlayback(); S.packOpen = null; S.pd = null; if (!S.disc) loadDiscover(); else render(); }
      if (act === 'disc-off') { S.discover = false; render(); }
      if (act === 'obtainable') { S.obtainable = !S.obtainable; render(); }
      if (act === 'f-inst') { S.fInst = (S.fInst === el.dataset.v) ? '' : el.dataset.v; applyFilters(); }
      if (act === 'clear-filters') {
        S.fInst = S.fKey = S.fBpm = S.fCat = '';
        S.samples = null; stopPlayback(); render();
      }
      if (act === 'play-sample') {
        const p = el.dataset.path;
        playFile(p, p.split('/').pop(), 0, el.dataset.loc);
      }
      if (act === 'open-pack') {
        if (e.target.closest('a')) return; // product link stays a link
        const row = S.packs.find(x => x.location === el.dataset.loc && x.dir === el.dataset.dir);
        if (row) openPack(row);
      }
      if (act === 'close-pack') { stopPlayback(); S.packOpen = null; S.pd = null; render(); }
      if (act === 'pd-folder') { S.pdFolder = el.dataset.f; loadPdFolder().then(render); }
      if (act === 'scan') startScan(el.dataset.l);
      if (act === 'ann-update') updateAnnotations();
      if (act === 'upd-apply') applyUpdate();
      if (act === 'dev-new') { S.devForm = { bit_depth: 16, sample_rate: 44100, channels: 'stereo', mode: 'card', layout: 'mirror', sanitize: true }; S.toast=''; render(); }
      if (act === 'dev-cancel') { S.devForm = null; render(); }
      if (act === 'dev-save') {
        const g = id => document.getElementById(id).value;
        const body = { name: g('dv-name'), bit_depth: +g('dv-depth'), sample_rate: +g('dv-rate'),
          channels: g('dv-ch'), mode: g('dv-mode'), layout: g('dv-layout'), filesystem: g('dv-fs'),
          max_duration_seconds: parseFloat(g('dv-dur')) || 0, max_files_per_dir: parseInt(g('dv-maxfiles')) || 0,
          max_filename_length: parseInt(g('dv-maxname')) || 0, sanitize: document.getElementById('dv-san').checked,
          display_length: parseInt(g('dv-display')) || 0,
          companions: document.getElementById('dv-comp').checked,
          rename: (document.getElementById('dv-rename').checked && parseInt(g('dv-display')) > 0) ? 'distinguishing-first' : '' };
        api('/api/device', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) })
          .then(async r => { if (r.error) { S.toast = r.error; render(); return; }
            S.devForm = null; S.devices = await api('/api/devices') || []; loadSources(); });
      }
      if (act === 'sto-new') { S.stoForm = { reserve: '10%', cluster_bytes: 32768 }; S.toast=''; render(); }
      if (act === 'sto-cancel') { S.stoForm = null; render(); }
      if (act === 'sto-save') {
        const g = id => document.getElementById(id).value;
        const body = { name: g('st-name'), capacity_bytes: parseInt(g('st-cap')) || 0,
          reserve: g('st-reserve'), cluster_bytes: parseInt(g('st-cluster')) || 0, kind: 'filesystem' };
        api('/api/storage', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) })
          .then(r => { if (r.error) { S.toast = r.error; render(); return; } S.stoForm = null; loadSources(); });
      }
      if (act === 'recipe-new') { S.newRecipe = { device: (S.devices[0]||{}).name || '', storage: (S.storages[0]||{}).name || '' }; render(); }
      if (act === 'recipe-cancel') { S.newRecipe = null; render(); }
      if (act === 'recipe-create') {
        const g = id => document.getElementById(id).value;
        viewAction({ action:'create', name: g('nr-name'), device: g('nr-device'), storage: g('nr-storage'), target: g('nr-target') })
          .then(ok => { if (ok) { S.newRecipe = null; S.view = document.getElementById('nr-name')?.value || S.view; S.pf = null; loadPreflight(); } });
      }
      if (act === 'rule-remove') {
        const i = +el.dataset.i;
        viewAction({ action:'remove-rule', name: S.view, index: i }).then(ok => { if (ok) { S.disabled = new Set(); loadPreflight(); } });
      }
      if (act === 'set-target') {
        const cur = (S.views.find(v=>v.name===S.view)||{}).target || '';
        openDirPicker('Materialize target for ' + S.view, cur, val => viewAction({ action:'set-target', name: S.view, target: val }).then(ok => ok && loadPreflight()));
      }
      if (act === 'nr-browse') {
        const box = document.getElementById('nr-target');
        openDirPicker('Target folder for the new recipe', box.value, val => { S.newRecipe.target = val; render(); document.getElementById('nr-target').value = val; });
      }
      if (act === 'rename-start') { S.renaming = true; render(); document.getElementById('rn-name')?.select(); }
      if (act === 'lay-cancel') { S.layoutEdit = false; render(); }
      if (act === 'lay-save') {
        const tpl = (document.getElementById('lay-tpl').value || '').trim();
        S.layoutEdit = false;
        setLayout(tpl); // "" = back to mirror; a bad template comes back as a toast from the server
      }
      if (act === 'rename-cancel') { S.renaming = false; render(); }
      if (act === 'rename-save') {
        const nn = document.getElementById('rn-name').value.trim();
        if (!nn || nn === S.view) { S.renaming = false; render(); return; }
        viewAction({ action:'rename', name: S.view, new_name: nn }).then(ok => { if (ok) { S.view = nn; S.renaming = false; S.pf = null; S.toast = `renamed to ${nn}`; loadPreflight(); setTimeout(()=>{S.toast='';render();}, 3000); } });
      }
      if (act === 'dp-cancel') { S.dirPick = null; render(); }
      if (act === 'dp-go') { dirPickGo(el.dataset.path); }
      if (act === 'dp-up') { if (S.dirPick?.data?.parent) dirPickGo(S.dirPick.data.parent); }
      if (act === 'dp-type') { dirPickGo(document.getElementById('dp-path').value); }
      if (act === 'dp-pick') {
        const d = S.dirPick; const val = (document.getElementById('dp-path').value || d.path || '').trim();
        S.dirPick = null; if (val) d.onPick(val); else render();
      }
      if (act === 'add-to') {
        S.addTo = { location: el.dataset.loc, glob: el.dataset.glob, label: el.dataset.label, as: el.dataset.as || '' };
        render();
      }
      if (act === 'add-group') {
        const g = el.dataset.g, r = groupRule(g);
        if (r) S.addTo = r;
        else {
          const packs = S.packs.filter(p => packGroup(p) === g);
          S.addTo = { label: `all of ${g} (${packs.length} packs)`, location: packs.map(p => p.location).join(', '), glob: `${packs.length} rules, one per pack`,
            rules: packs.map(p => ({ location: p.location, glob: p.dir + '/**', as: p.provider ? p.location.toUpperCase() + '/' + p.dir : '', label: p.name })) };
        }
        render();
      }
      if (act === 'add-to-cancel') { S.addTo = null; render(); }
      if (act === 'add-to-save') {
        const v = document.getElementById('at-view').value;
        const a = S.addTo;
        const rules = a.rules || [a];
        const replace = !!document.getElementById('at-replace')?.checked;
        (async () => { for (const r of rules) { if (!await viewAction({ action:'add-rule', name: v, location: r.location, glob: r.glob, as: r.as, replace_location: replace, note: 'added from the library: ' + r.label })) return false; } return true; })()
          .then(ok => { if (ok) { S.toast = replace ? `${v}: ${a.location} is now one rule` : `added to ${v}`; S.addTo = null; if (S.view === v) { S.pf = null; loadPreflight(); } else render(); setTimeout(()=>{S.toast='';render();}, 3000); } });
      }
      if (act === 'new-source') { S.addForm = { name: '', type: 'local', root: '', rescan: 'manual' }; S.toast = ''; render(); }
      if (act === 'cancel-add') { S.addForm = null; S.toast = ''; render(); }
      if (act === 'add-sugg') {
        const g = S.suggestions[+el.dataset.i];
        addLocation({ name: g.name, type: 'local', root: g.root, vendor: g.vendor || '', rescan: g.rescan, scan: true });
      }
      if (act === 'save-add') {
        addLocation({
          name: document.getElementById('af-name').value,
          type: document.getElementById('af-type').value,
          root: document.getElementById('af-root').value,
          host: document.getElementById('af-host').value,
          rescan: document.getElementById('af-rescan').value,
          scan: true,
        });
      }
      if (act === 'pd-up') {
        const i = S.pdFolder.lastIndexOf('/');
        S.pdFolder = i > 0 ? S.pdFolder.slice(0, i) : '';
        loadPdFolder().then(render);
      }
      if (act === 'play') { playFile(el.dataset.p, el.dataset.n, +el.dataset.d); }
      if (act === 'toggle-play') { if (S.player) playFile(S.player.path, S.player.name, S.player.dur); }
      if (act === 'grp') {
        const g = (S.rModel?.groups || []).find(x => x.key === el.dataset.g);
        if (g) recipeEdit(g.state === 'all' ? uncheckGroup(g) : checkGroup(g),
          g.state === 'all' ? `${g.key} removed from ${S.view}` : `all of ${g.key} → ${S.view}`);
      }
      if (act === 'grp-open') {
        const k = el.dataset.g;
        S.rOpen.has(k) ? S.rOpen.delete(k) : S.rOpen.add(k);
        render();
      }
      if (act === 'pk') {
        const g = (S.rModel?.groups || []).find(x => x.key === el.dataset.g);
        const e = g && g.packs[+el.dataset.j];
        if (e) recipeEdit(togglePack(e));
      }
      if (act === 'collapse') {
        const g = (S.rModel?.groups || []).find(x => x.key === el.dataset.g);
        if (g) recipeEdit(collapseGroup(g), `${g.key} is now one rule`);
      }
      if (act === 'tidy') {
        const gs = (S.rModel?.groups || []).filter(x => x.collapsible);
        recipeEdit((async () => { for (const g of gs) if (!await collapseGroup(g)) return false; return true; })(),
          `collapsed ${gs.length} ${gs.length === 1 ? 'vendor' : 'vendors'} — same selection, fewer rules`);
      }
      if (act === 'go-run') { if (!el.classList.contains('blocked') && runAllowed()) { S.screen = 'run'; render(); } }
      if (act === 'go-plan') { if (!S.view) return; S.screen = 'plan'; S.pl.q = null; S.pl.tree = null; render(); }
      if (act === 'pl-tab') { S.pl.tab = el.dataset.k; S.pl.sel = null; S.pl.file = null; S.pl.form = null; S.pl.q = null; S.pl.tree = null; render(); }
      if (act === 'pl-kind') { S.pl.kind = el.dataset.k; S.pl.q = null; S.pl.sel = null; S.pl.form = null; render(); }
      if (act === 'pl-row') { openQueueRow(+el.dataset.i); }
      if (act === 'pl-qfile') { e.stopPropagation(); S.pl.file = S.pl.files.files[+el.dataset.i]; render(); }
      if (act === 'pl-file') { openTreeFile(S.pl.tree.files[+el.dataset.i]); }
      if (act === 'pl-play') { e.stopPropagation(); playFile(el.dataset.path, el.dataset.path.split('/').pop(), 0, el.dataset.loc); }
      if (act === 'pl-dir') { S.pl.prefix = S.pl.prefix ? S.pl.prefix + '/' + el.dataset.name : el.dataset.name; S.pl.tree = null; S.pl.file = null; S.pl.form = null; render(); }
      if (act === 'pl-crumb') { S.pl.prefix = el.dataset.p; S.pl.tree = null; S.pl.file = null; S.pl.form = null; render(); }
      if (act === 'pl-facet') { readForm(); S.pl.form.facet = el.dataset.k; S.pl.form.value = ''; S.pl.radius = null; if (el.dataset.k === 'alias' && S.pl.file && S.pl.file.why && S.pl.file.why.instrument) S.pl.form.word = S.pl.file.why.instrument.word || ''; if (el.dataset.k === 'alias') S.pl.form.path = (S.pl.file && S.pl.file.pack_path) || (S.pl.sel && S.pl.sel.pack_path) || S.pl.form.path; render(); }
      if (act === 'pl-mode') { readForm(); S.pl.form.mode = el.dataset.k; S.pl.radius = null; render(); }
      if (act === 'pl-value') { readForm(); S.pl.form.value = el.dataset.v; S.pl.radius = null; render(); }
      if (act === 'pl-preview') { planCorrect(true); }
      if (act === 'pl-apply') { if (S.pl.radius) planCorrect(false); else planCorrect(true).then(() => { if (S.pl.radius && !S.pl.msg) planCorrect(false); }); }
      if (act === 'pl-ack') { planAck(); }
      if (act === 'pl-report') { planReport(); }
      if (act === 'pl-export') { window.open('/api/local/export', '_blank'); }
      if (act === 'pl-reconcile') { planReconcile(); }
      if (act === 'pl-drop') { const v = S.pl.rec.verdicts[+el.dataset.i]; planDrop([{ file: v.file, vendor: v.vendor, pack: v.pack, kind: v.kind, entry: v.entry }]); }
      if (act === 'pl-drop-all') { planDrop(S.pl.rec.verdicts.filter(v => v.redundant).map(v => ({ file: v.file, vendor: v.vendor, pack: v.pack, kind: v.kind, entry: v.entry }))); }
      if (act === 'go-migrate') { if (!el.classList.contains('blocked')) { S.screen = 'run'; startMigrate(); } }
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
  $app.querySelectorAll('[data-blurb]').forEach(el => {
    const u = el.dataset.blurb;
    if (!u) return;
    el.addEventListener('mouseenter', async () => {
      if (el.title) return;
      if (!blurbCache[u]) { try { blurbCache[u] = await api('/api/blurb?u=' + encodeURIComponent(u)); } catch (e) { blurbCache[u] = {}; } }
      if (blurbCache[u].description) el.title = blurbCache[u].description;
    }, { once: false });
  });
  $app.querySelectorAll('[data-act="rescan"]').forEach(sel => {
    sel.addEventListener('change', async () => {
      await api('/api/locations', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ update: sel.dataset.l, rescan: sel.value }) });
      loadSources();
    });
  });
  const dvPreset = document.getElementById('dv-preset');
  if (dvPreset) dvPreset.addEventListener('change', () => {
    const p = S.presets.find(x => x.id === dvPreset.value);
    if (!p) return;
    const keep = document.getElementById('dv-name').value;
    S.devForm = Object.assign({}, p, { name: keep || p.id, preset: p.id });
    render();
  });
  const stVol = document.getElementById('st-vol');
  if (stVol) stVol.addEventListener('change', () => {
    const v = S.volumes.find(x => x.path === stVol.value);
    if (!v) return;
    S.stoForm = Object.assign({}, S.stoForm, { vol: v.path, capacity_bytes: v.capacity_bytes,
      name: (document.getElementById('st-name').value || v.name.toLowerCase().replace(/[^a-z0-9]+/g,'-')) });
    render();
  });
  const afType = document.getElementById('af-type');
  if (afType) afType.addEventListener('change', () => { S.addForm.type = afType.value; S.addForm.name = document.getElementById('af-name').value; S.addForm.root = document.getElementById('af-root').value; render(); });
  const search = document.getElementById('search');
  if (search) {
    search.addEventListener('input', () => {
      S.search = search.value;
      if (sampleMode()) { clearTimeout(searchTimer); searchTimer = setTimeout(loadSamples, 220); }
      renderPreservingSearch();
    });
  }
  const rfilter = document.getElementById('rfilter');
  if (rfilter) {
    rfilter.addEventListener('input', () => {
      S.rFilter = rfilter.value;
      render();
      const el = document.getElementById('rfilter');
      if (el) { el.focus(); el.setSelectionRange(el.value.length, el.value.length); }
    });
  }
  // cross-pack filters: selects apply immediately, text fields debounce
  const bind = (id, key, evt) => {
    const el = document.getElementById(id);
    if (!el) return;
    el.addEventListener(evt, () => {
      S[key] = el.value.trim();
      if (evt === 'input') { clearTimeout(searchTimer); searchTimer = setTimeout(applyFilters, 300); }
      else applyFilters();
    });
  };
  bind('f-inst', 'fInst', 'change');
  bind('f-cat', 'fCat', 'change');
  bind('f-key', 'fKey', 'input');
  bind('f-bpm', 'fBpm', 'input');
  const pv = document.getElementById('pl-value');
  if (pv) pv.addEventListener('change', () => { readForm(); S.pl.radius = null; });
  const vp = document.getElementById('view-pick');
  if (vp) vp.addEventListener('change', () => { S.view = vp.value; S.disabled = new Set(); S.pf = null; loadPreflight(); });
  const lp = document.getElementById('layout-pick');
  if (lp) lp.addEventListener('change', () => {
    if (lp.value !== '__custom') { setLayout(lp.value); return; }
    S.layoutEdit = true; render(); document.getElementById('lay-tpl')?.select();
  });
}

let searchTimer = null;

// applyFilters keeps focus where the user was typing — the whole screen
// re-renders, so re-find the field and restore the caret.
function applyFilters() {
  const active = document.activeElement?.id;
  const caret = document.activeElement?.selectionStart;
  if (!sampleMode()) { S.samples = null; render(); return; }
  loadSamples().then(() => {
    if (!active) return;
    const el = document.getElementById(active);
    if (el) { el.focus(); if (caret != null && el.setSelectionRange) el.setSelectionRange(caret, caret); }
  });
}

function renderPreservingSearch() {
  render();
  const s = document.getElementById('search');
  if (s) { s.focus(); s.setSelectionRange(s.value.length, s.value.length); }
}

window.addEventListener('keydown', (e) => {
  if (e.target && /INPUT|TEXTAREA|SELECT/.test(e.target.tagName)) {
    if (e.key === 'Escape') e.target.blur();
    if (e.key === 'Enter' && e.target.id === 'dp-path') dirPickGo(e.target.value);
    if (e.key === 'Enter' && e.target.id === 'rn-name') document.querySelector('[data-act="rename-save"]')?.click();
    return;
  }
  const map = { 1: 'library', 2: 'recipe', 3: 'plan', 4: 'run', 5: 'cards', 6: 'sources' };
  if (e.key === 'Escape' && S.packOpen) { stopPlayback(); S.packOpen = null; S.pd = null; render(); return; }
  if (S.packOpen && (e.key === 'ArrowDown' || e.key === 'ArrowUp') && S.pd?.files?.length) {
    e.preventDefault();
    const files = S.pd.files.filter(f => f.format);
    if (!files.length) return;
    let i = S.player ? files.findIndex(f => f.path === S.player.path) : -1;
    const j = e.key === 'ArrowDown' ? Math.min(i + 1, files.length - 1) : Math.max(i - 1, 0);
    if (j === i) return; // at the edge of the list — keep playing
    const f = files[j];
    playFile(f.path, f.name, f.duration || 0);
    return;
  }
  if (map[e.key] && stepAllowed(map[e.key])) { S.screen = map[e.key]; if (S.screen === 'cards') S.locks = []; render(); }
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
