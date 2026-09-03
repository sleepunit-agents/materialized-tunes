# Handoff: Materialized Tunes UI redesign

## Overview

A shell-level redesign of the mtunes desktop UI (Wails v2, Go backend, vanilla-JS frontend in `internal/ui/assets/`). The goal was to turn a working engineering sample into a product: replace the 1-2-3-4 stepper with three modes, give filters and navigation a permanent home on the left, make every create/edit a dialog, fold Plan into a global Fix inbox, and make Materialize a proper Select → Write → History story.

Nothing about the backend model changes. Every screen here is fed by an endpoint that already exists in `app.js`.

## About the design files

`mtunes redesign.dc.html` is a **design reference built in HTML** — an options board, not production code. Do not copy its markup. Recreate the screens inside the existing frontend (`index.html` for CSS, `app.js` for state + render functions), keeping the current architecture: one `S` state object, string-template `render*()` functions, `data-act` event delegation in `wire()`, scroll/focus preservation in `renderInner()`.

Open the file and pan/zoom. Turns are stacked newest-first; option ids (`2b`, `6a`, …) are referenced below.

## Fidelity

**High-fidelity for layout, hierarchy, copy, and color.** Sizes in this doc are the ones used in the mocks; treat them as targets, not pixel law — the existing 1100px minimum window is still the floor. Pack art in the mocks is placeholder tiles; keep the real `artBox()`.

## Decisions taken (read these first)

1. **Three modes, not steps.** Library · Discover · Materialize. Setup is a gear. The stepper (`tabbar()` with `stepAllowed()`) is removed.
2. **Left side = icon rail (52px) + contextual column (216px).** The rail never hides. The column's contents depend on the mode (filters / recipes / setup index / folder tree) and can collapse to nothing (⌘\).
3. **Frameless custom title bar** on all platforms (Wails `Frameless: true`, `--wails-draggable: drag` on the bar, custom min/max/close calling `runtime.WindowMinimise/ToggleMaximise/Quit`). Title reads "Materialized Tunes". Workspace path moves to Setup.
4. **Review/Plan is gone as a recipe step.** It is the **Fix** inbox — global, with a "this recipe" filter. Pre-flight shows the unsorted count as a fact with a link to Fix. Writing never blocks on it.
5. **Recipe = Select · Write · History tabs.** The old `cards` screen becomes the History tab.
6. **Every create/edit is a dialog** (device, storage, source, recipe). One dialog shape.
7. **Annotations stay local.** No in-app submit. "Export file…" downloads the local layer for a manual issue/PR to the registry repo.
8. **Grid and list views** everywhere packs are listed. Same data in both.
9. **Density one notch airier**: 12–13px body, 56px art in grid, 36px in list, row padding 7px.

## Screens

### Shell — `2b`, `9a`, `9b`

Replaces `titlebar()`, `tabbar()`, `statusbar()`.

Layout: CSS grid `52px 216px 1fr` × `40px 1fr 28px`.

- **Title bar (40px, #0a0c0d, border-bottom #1e2226)**: amber 16px square logo, "Materialized Tunes" (700 12px Archivo, letter-spacing .04em), spacer, search field (300px, "Search everything", ⌘K), spacer, window controls (3 × 40px hit areas: — ▢ ✕). Whole bar is `--wails-draggable: drag`; controls are `no-drag`. Drop the `body.in-wails-mac` special case only if you keep macOS traffic lights; otherwise draw all three.
- **Rail (52px, #0a0c0d)**: 40×40 buttons, radius 8, active `#1e2328`. Icon + 8px uppercase label: LIB, DISC, MAT (amber dot top-right when any recipe is stale), FIX (amber count, e.g. "8.1k"; green ✓ when zero). Bottom: **lens** box (teal border/tint when on, device short label; "OFF" otherwise) and ⚙ Setup. Keyboard: ⌘1–4 modes, L cycles lens.
- **Contextual column (216px, #0d0f11)**: header row (13px 600 title, right-aligned "clear"/"+"/"‹" collapse). Collapsible with ⌘\. When collapsed, the content header shows a `Filters ›` pill with an active-count badge and the active filters as removable chips (`9a`).
- **Status bar (28px, #0a0c0d)**: shortcut hints left (11px mono #444c53), context right (annotations sha, build).
- **1100px** (`9b`): column narrows to 196, list drops to 5 columns (art, pack, vendor, "lens files / all", lens size).

State: add `S.mode` (`library|discover|materialize|fix|setup`), `S.colOpen` (bool, persisted), keep `S.lens`.

### Library — list `2b`, grid `4a`

Maps to `renderLibrary()`. The filter column replaces `filterBar()` and the vendor chip row in `screen-head`.

Column: LENS box (device, one line of constraints, ▾ opens the picker), VENDOR list with counts (from `packGroup`), CATEGORY (one-shots/loops/multisamples/**Unsorted** in amber with the Fix count), INSTRUMENT (from `INSTRUMENTS`, "N more…"), KEY · BPM inputs. Selecting a facet filters in place; the existing `S.locFilter/fInst/fCat/fKey/fBpm` carry over. Sample mode (`renderSamples`) becomes the **Packs / Samples** switch in the content header (`2c` shows it) rather than an implicit mode.

Header: "Library" 600 20px, "334 packs" mono #5c666f, lens summary in teal (`n(eligible) files · fmtB(converted) after transcode`), sort menu, Grid/List segmented.

List row (grid `44px 1fr 150px 100px 100px 120px 100px 40px`, gap 12, padding 7px 20px, border-bottom #16191c): art 36px radius 5 · name 600 13px + badge · vendor 12px #9aa3ab · files · size (both mono #5c666f) · **lens files · lens size** (mono #c8ccd0, header labels teal) · ▸. Lens columns show `—` when lens is off, or hide.

Grid card (`4a`, `repeat(auto-fill, minmax(280px,1fr))`, #191d20, border #23272b, radius 8, padding 12): art 44px + name/vendor · row `lf / f` … `ls` (teal) · badge … source size. Never put anything on the card that isn't in the row.

Badge (`badgeFor`): 600 9px, 1px border, radius 3, padding 1px 5px; complete #57c48a, partial/percent #e0b64f, plain folder #5c666f.

### Pack detail — `4c`

Maps to `renderPackDetail()`. The **folder tree moves into the contextual column** (replaces `.pd-grid`'s 210px left pane): "‹ Back to Library", "Folders · count", indented rows (14px per level, active #1e2328), SOURCE box at the bottom (location · dir, size, "matched by content hash").

Header (padding 18px 20px 14px): art 84px radius 8 · name 600 20px + badge · vendor · vendor link (teal) · description (12px/1.5, max 640px) · numbers line: files, size, teal lens line, amber "N folders need a decision". Right: **Add to recipe ▾** (amber primary, opens `11c`), **Fix N folders** (secondary → Fix filtered to this pack).

Breadcrumb row + "Filter files in this pack" input. File table (grid `24px 1fr 130px 90px 40px 44px 60px 96px 96px`): ▶ · file (mono 12px) · instrument chip · category chip · key · bpm · length · source format · ON <device> (teal header). Chips: 500 11px, border 1px, radius 4, padding 2px 7px; **amber border/text when the value is unknown/unsure** — clicking opens the fix chip picker inline (same control as Fix, `POST /api/correct`). Ride-along rows (.adg) show "skipped" in the lens column.

Player (docked, 58px, #0a0c0d): 30px teal round play/pause, name + "pos / dur · key · format", waveform (`#wavefill` keeps working), **"Hear it as <device>"** toggle → `/api/preview` with `device=` (backend: transcode preview; if not available yet, hide the toggle). Keyboard: space play, ↑↓ next file.

### Discover — list `3a`, grid `4b`

Maps to `renderDiscover()` / `discCard()`. Column facets: SHOW (Obtainable ✓ / Already in library ✓ / Recognized, no link — replaces the `obtainable` chip and the "RECOGNIZED, NOT SOURCED" tail), PRICE, LICENSE (`LICENSE_BADGE`), VENDOR. Footer note in the column carries the "everything here is in the registry…" copy.

Row (grid `44px 1fr 150px 70px 130px 150px 100px`): art · name · vendor · price badge (free green / paid grey) · license (mono) · "vendor lists N samples" · **Get ↗** (teal outline) or "in library" (grey). `discHints().skip` rows render at opacity .5, never hidden. Grid card: art + name/vendor, then price · license … Get.

### Materialize — Select `6a`, Write `7a` `7b`, History `7c`

Column = recipe list: each row name 600 12.5px, state dot (green in sync / amber stale / grey never written), sub-line `device → storage · size`. "+" opens New recipe (`3d`). "History · N locks" at the bottom lists all locks across recipes.

Content header: recipe name 600 20px, device chip (teal), storage chip, tabs **Select · Write · History** (segmented). Under it one mono line: target · layout · ride-alongs · **Edit** (opens the recipe dialog).

**Select** (`renderRecipe()` + `recipeGroups()`): filter input, "7 vendors in · 2 packs excluded", All/None. Vendor row: 16px checkbox (amber; half-fill gradient for partial), name (140px), sub (mono #5c666f), **per-vendor size bar** (90×6, amber), `files · size`, ▸. Expanded packs indent 48px with strikethrough for cuts and "· only WAV/Acid Loops" for subtree rules.

Pre-flight panel (360px, #0f1113, padding 18): "Fit on <storage>" + FITS/OVER/EMPTY tag · meter 28px (**stacked by vendor** in amber shades, red overflow segment, hatched reserve on the right) · legend · vendor size breakdown box (top 3 + "N more") · three summary rows: warnings count, errors count, **"N folders land in _Unsorted" with a `Fix ↗` link** · primary **Write · N files** (amber; disabled grey "pick something first" when empty) · one-line note that unsorted files still write under `_Unsorted/`.

**Write** (`renderRun()`; `/api/run`, `/api/materialize`, `/api/migrate`): centered column max 860px. Running (`7a`): title "Writing to <target>" + ETA; 18px bar (amber fill, thin #e0b64f sliver for skipped); `count of total · GiB of GiB · files/s`; **five counters** in cards: rendered, reused (already present), retrying, skipped, failed; phase strip pull → transcode → write (active amber); log pane 220px (#0a0c0d, mono 11.5px/1.7, retry/skip lines amber); Pause / Stop (red outline) + "Stopping keeps what's written. The lock is recorded only when the run finishes."
Finished (`7b`): green DONE band "143,790 of 143,804 written · 14 skipped · lock recorded", four counters, **skipped panel** with per-row reason + class and two actions (Retry these / Open in Fix), LOCK row with path, buttons Verify against the lock (primary) / History / Open folder. Migrate uses the same layout with verb "Renaming into the new layout".

**History** (`7c`, was `renderCards()`; `/api/locks`, `/api/diff`): DRIFT band (amber) "folder matches lock X, recipe has changed since — N files would change (+a −b)" with Show diff / **Write update**. Rows (grid `14px 120px 70px 1fr 170px 140px`): dot (green = on card), when, kind (write amber / migrate grey / restore teal), what, lock id, Diff + Restore buttons. Selected row expands a 3-column box: selected lock, vs previous (+/−/~ moved), recipe at the time.

### Fix — `6b`, tree `11d`, empty `10b`

Replaces `renderPlan()` + `renderPlanForm()` + `renderVerdict()`; data from `/api/plan/queues`, `/api/plan/tree`, `/api/plan/folder`, `/api/correct`, `/api/ack`, `/api/report`, `/api/local*`. Scope defaults to the whole library; a "this recipe" filter uses `view=`.

Column: QUEUES (Everything / Family only / No instrument / Loop or one-shot? with counts), BY PACK (top packs with counts, "N more…"), bottom **YOUR ANNOTATIONS** box: "71 decisions · local only", file name + size, **Export file…** (`/api/local/export`), note "Want them in the shared registry? Export and attach to a GitHub issue or PR.", Reconcile / Withdraw… links.

Content (grid `1fr 440px` × `1fr 58px`): 
- Left: title + count, Folders/Tree toggle, sort, one explanatory sentence, table (folder path mono 12.5px + pack line, files count, "we know" guess). Tree view (`11d`) indents vendor › pack › folder with rolled-up amber counts and a "Decide here" affordance on every level.
- Right panel (#0f1113): folder name + "1 / 8,128", path, **provenance chips** (`registry: keys` · `filenames: nothing` · `local: undecided` amber). LISTEN table (▶ · file · key · len), **sorted by pitch for multisamples, length for one-shots, BPM for loops**; selected row #1e2328 with a filled teal play button. Then the form: kind segmented (Instrument / Category / Word means), value **chips** (lexicon from `/api/lexicon`, selected amber tint, "Other…" dashed), radius segmented (This folder / Whole pack / Word "X"), optional note input. Footer: Skip · Parser bug (red) … **Apply → next** (amber).
- Player bar spans both columns (same component as pack detail) with Loop and **Auto-advance** toggles.

Keyboard: ↑↓ next file (plays), ⇧↑↓ next folder, space play/pause, ⏎ apply, ←→ fold (tree).

Empty (`10b`): queues at 0, green check, "Nothing to fix", three stat cards, Export / Reconcile buttons.

### Setup — `3b`; dialogs `3c` `3d` `8a` `8b`

Replaces `renderSources()` (+ `renderAnnotations/Devices/Storages` sections). Column is a section index: Sources, Devices, Cards & storage, Annotations (green dot = up to date), Workspace, Updates (amber sha when available — this replaces the `upd-apply` chip in the old tab bar). WORKSPACE box at the bottom shows path + version.

Each section: title 600 20px + mono count, right-aligned secondary (Rescan all) and **one amber "+ Add …"**; one sentence of purpose under the title (12px #9aa3ab, max 640px); rows as cards (#191d20, radius 8, padding 12px 14px). Source row: name + tags (local / vendor-dirs / splice) · path (mono) · "N files · scanned <relative time>" · schedule select · Rescan · ···. Suggested source: dashed border, amber "found on this machine" tag, Add as source / Ignore. Device row: name · preset base · format · fs · use count · Edit. Storage row: name · kind · capacity · reserve · state dot + text · Edit.

**Dialog shape** (all of `3c 3d 8a 8b`, replaces the inline `devForm/stoForm/newRecipe/addForm/recipeForm` blocks): veil rgba(0,0,0,.45) over the page; box 560px, #16191c, border #2f353b, radius 10, shadow `0 30px 80px rgba(0,0,0,.7)`. Header: title 600 16px + one-line purpose 12px #9aa3ab + ✕. Body: fields with 10.5px uppercase labels (letter-spacing .08em, #5c666f), 16px gaps; inputs #0f1113 with #2a2f34 border (teal when focused), mono 13px; **segmented controls for any ≤4-way choice**; picker cards (radius 6, amber tint when selected) for presets/layouts/volumes. Footer (#131619): summary sentence left (mono-ish 11.5px #5c666f, ellipsis), Cancel, one amber primary.
- Add device (`3c`): START FROM preset grid (`/api/presets`), NAME, BIT DEPTH / SAMPLE RATE / CHANNELS / FILESYSTEM segments, NAMING RULES (max filename, files per folder, strip-chars checkbox). `POST /api/device`.
- New recipe (`3d`): NAME, DEVICE + WRITE TO selects showing constraints/capacity inline, TARGET FOLDER + Browse (existing `dirPicker`), LAYOUT picker with example paths, ride-alongs checkbox. Footer "Empty recipe · 0 of 26.7 GiB · pick packs next"; primary "Create & select packs" → `POST /api/view` then Select tab. Recipe Edit reuses it.
- Add source (`8a`): FOLDER + Browse, NAME, WHERE (This machine / SSH host), layout picker (Vendor folders / One vendor / Splice), VENDOR, RESCAN, **PEEK** row (quick count via `/api/dirs` or a new lightweight endpoint), footer "Scans now · N files · ~40s", primary "Add & scan" (`POST /api/locations` + `POST /api/scan`).
- Add storage (`8b`): NAME, KIND (Card / drive · Folder quota · +Drive), VOLUME radio list from `/api/volumes` (label · fs · capacity, "Other path…"), RESERVE slider with live usable figure, "Stamp the card" checkbox. `POST /api/storage`.

### First run — `10a`

When `S.locations` is empty, Library renders: rail + full-width content, no column. Centered: "Point it at your samples" 600 24px + two-line explanation; FOUND ON THIS MACHINE list from `/api/suggestions` (name + kind tag, path, count/size, amber Add); dashed "Somewhere else — a folder or an SSH host … Choose…"; a 1-2-3 orientation line (Add a source → Describe a device → Write a recipe to a card), first step amber.

### Small parts — `11a` ⌘K, `11b` lens picker, `11c` add-to-recipe

- **⌘K** (`11a`): 640px palette at top 120px; input row with "through <lens>" chip; results grouped by kind gutter (PACK / SAMPLE / ACTION, 9px letter-spaced), 28px art, name + sub; footer hints ↑↓ ⏎ space (preview) ⇧⏎ (add to open recipe). Sources: `/api/packs` (client filter), `/api/samples?q=`, a static action list.
- **Lens picker** (`11b`): anchored above the rail's lens box, 300px; "off" + each device with constraints and the through-lens totals; "Manage devices" footer; L cycles. Replaces `.lens-menu` (drop the ★ owned flag or keep it as a secondary control).
- **Add to recipe ▾** (`11c`): 360px popover from the pack header button; one row per recipe with checkbox, `device → storage`, and **fit consequence** ("+2.1 GiB · fits" green / "over by 3.7 GiB" red / quota message). Needs a dry-run: `POST /api/plan` with the candidate rule appended, or a cheaper size estimate from `converted_bytes`. Footer: "New recipe with this pack". Replaces `addToPicker()`.

## Interactions & behavior summary

- Mode switch: ⌘1–4, rail click. Column content swaps per mode; `S.colOpen` persists per mode.
- Lens: global; L cycles off → devices; every count/size re-renders through it (`loadPacks` with `device=`). Recipe screens force the lens to the recipe's device.
- Grid/List: per-mode preference, persisted.
- Fix: applying a decision `POST /api/correct` then advances to the next folder and starts playing its first file if Auto-advance is on.
- Write: poll `/api/run` at 500ms while running; lock recorded only on `done`.
- Dialogs: Esc cancels, ⏎ submits, focus trapped.
- Frameless: double-click title bar toggles maximize; Windows snap works via `--wails-draggable`.

## State additions

`mode`, `colOpen`, `viewMode: 'grid'|'list'`, `dialog: {kind, data} | null` (replaces `devForm/stoForm/newRecipe/addForm/recipeForm/addTo`), `recipeTab: 'select'|'write'|'history'`, `fix: {scope: 'all'|view, queue, tree, sel, files, cursor, autoAdvance}` (evolves `S.pl`), `cmdk: {open, q, results}`.

## Design tokens (unchanged from `index.html`, listed for reference)

Backgrounds #0a0c0d (bars) · #0d0f11 (column) · #0f1113 (panels) · #111315 (page) · #131619 · #191d20 (cards) · #1b1f23 · #1e2328 (selected).  
Borders #16191c (row hairline) · #1e2226 · #23272b · #2a2f34 · #2f353b (dialog) · #3a4046.  
Text #e8eaed · #c8ccd0 (numbers) · #9aa3ab (dim) · #5c666f (faint) · #444c53 (ghost) · #7d868e (log).  
Accents amber #f0a03c / #d8862a (write, primary), teal #3dc4cf (lens, play), green #57c48a, warn #e0b64f, error #e5604f.  
Type: Archivo (UI), JetBrains Mono (paths, numbers, ids). Sizes used: 8/9/10/10.5/11/11.5/12/12.5/13/14/15/16/18/20/22/24. Radii 3 (badges) · 4 (chips) · 5 (small buttons) · 6 (inputs, buttons, meters) · 8 (cards, rail buttons) · 10 (dialogs).

## Files

- `mtunes redesign.dc.html` — the options board (all turns; `2b` and everything from turn 3 on is the chosen direction).
- `github.md` — repo association and screen map.

## Not designed yet

Restore confirmation, Diff view, Edit device (danger zone), SSH source variant, focus/hover states, a light theme. Build them in the same shell using the dialog and table patterns above.
