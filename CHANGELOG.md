# Changelog

The dated history of materialized-tunes. SPEC.md describes the tool as it
is *now*; this file is where the "decided 2026-07-17" / "shipped
2026-08-16" trail lives, with the physical observations that drove each
change, because *why* a constraint exists is as durable as the constraint.
Newest first. Versions are milestones, not releases — there is one binary
and it is whatever `main` builds.

## v0.9.41 — 2026-09-02 (the desktop app hands files over through a save dialog)

Jonathan: "dump opened a browser window to
http://wails.localhost/api/plan/dump?view=Samples which obviously no
work." The two "hand me a file" chips on the Plan screen — *dump* and the
local-layer *export* — were `window.open` on an endpoint answering
`Content-Disposition: attachment`. A browser turns that into a download.
The Wails webview has no download path of its own: it hands a new window
to the OS browser, and `wails.localhost` is an origin that only exists
inside the app.

**The desktop shell binds a `Desktop` object** (`window.go.main.Desktop`)
with `SaveDump(view)` and `SaveLocalExport()`: Go builds the same bytes
the endpoints serve (`ui.Server.DumpText`, `ui.Server.LocalExport` —
factored out of the handlers), opens a native save dialog defaulting to
`~/Downloads` and the file name the endpoint would have used, writes the
file, and the Plan screen says where it went. Cancel is silent. The
browser build still gets the download; the endpoints are unchanged.
`ui.New(ws)` + `Server.Handler()` replace the one-shot `ui.Handler` so
the shell can hold the server.

## v0.9.40 — 2026-09-02 (dump: what isn't matched and why, all of it)

Jonathan: "we should add a dump button or something on the plan page …
if you had the full list of what isn't being matched and why right now,
you could hit a lot of these faster than me." The queues are a picker —
three hundred rows, four example names each, acked folders hidden —
because a person decides one folder at a time. The lexicon's maintainer
works the other way round: every silence at once, with the words that
failed to speak in front of them.

**`GET /api/plan/dump?view=`** hands over the whole decision surface as
one text file (`&format=json` for tools): the same grouping as the
queues — by source folder, companions left out, majority category and
instrument per folder with the tier that answered — but every folder,
acked ones included and marked, and every file in each with its own
category · instrument, its own why where it differs from the folder's,
and where it lands. The *dump · what isn't matched and why* chip on the
Plan screen downloads it; `mtunes plan dump <view>` prints it. Header
carries the view, build time, app version and annotations head, so a
dump says which lexicon it was read against. Not gated yet: a normal
user never needs it, and it moves behind a developer toggle when one
exists. `plan.BuildDump` + `Dump.WriteText`; TestReviewSurface asserts
the two folders, all three files, the rack folder absent, and the acked
folder kept and marked.

## v0.9.39 — 2026-09-02 (a rack is never a decision)

Jonathan, Dr Sample From Mars: "`Ableton/Dr Sample From Mars
Project/Presets/07. Textures` <- this is asking loop or one shot, but
everything is an adg". The ten Textures racks inherit their record from
the sixteen chops each references (§6, the two-thirds vote), and 144 of
the 160 chops under `WAV/07. Textures` carried no kind word — so the
racks inherited the silence, landed in `_Unsorted`, and the Plan queued
the *rack* folder with a "loop or one-shot?" it could never act on: a
`[[dir]] category` on a folder of documents changes nothing, because a
document has no harvested facts of its own to pin.

**Companions leave the decision surface.** `recount` skips them for the
unsorted / uncategorized / general counters (the "N need a decision"
badge), and `/api/plan/queues` skips them when grouping rows and when
picking a row's *why*. `Kind` still says where a document went, the tree
still shows it there, and the vendor-prep / blind-document warnings are
unchanged. The question is asked once, on the sample folder; the answer
carries every rack over those samples with it (TestReviewSurface: a
rack over the uncategorized Kicks is not a row, and follows the kicks to
One-Shots after the correction). sample-vendor-annotations gives the
Textures folder `default_category = "one-shots"` (ten SP-303 texture
kits of sixteen numbered chops; "01. Break" keeps loops by its own
word) — probed over the house listing, exactly 143 files move.

## v0.9.38 — 2026-09-02 (the format level is not always at pack root)

Jonathan, in Modular Creations From Mars: "under the loops, it has some
apple loops bullshit that isn't getting deduped I don't think. aifs
instead of wavs. we can just straight ignore that shit." The pack has no
`WAV` at its top — its `1. Modular Loops (120 BPM)` holds `WAV/`, `Apple
Loops/` (.aif) and `REX2/` renders of the same 143 loops — so the
vendor's top-level globs never saw a format tree, the 143 .aif planned
as canonical loops beside their .wav twins, and the vendor-prep skip
never fired at all for the pack (no rank-0 tree, nothing to swap in),
which is also why `4. Modular Instruments`' 294 Kontakt / Live copies
sat in the plan despite their `format-tree` role and Jonathan had to pin
`…/Samples/Imported` by hand. SPEC §19 row E had already named it: a
consumer bug, roles honoured only at pack root.

**`strip` now descends for the pack's own nested `[[dir]]` roles.** When
the segment after the pack dir is not a tree, each deeper prefix is
looked up in the pack map (`annotations.PackDirRoleAt` — whole-path,
case-folded, globs as the harvester allows): `canonical-audio` strips
that segment at rank 0, `format-tree` strips it at the vendor's rank for
the name (never 0), and the dir above keeps its place in the output path.
The vendor's globs are deliberately not read below the top — a `Presets`
folder inside a content tree is content until a human says otherwise.
With sample-vendor-annotations annotating the three dirs, the WAV tree is
canonical, the .aif and the Kontakt / Live trees are SFM's own host prep
and leave the plan where their stems survive under WAV, and the plan's
vendor-prep warning names any that don't.

## v0.9.37 — 2026-09-02 (camel case is words too)

Jonathan, from the leftover list after the fx/tops/clav passes:
"StringsLow and StringsLowGlide from sfm vp330 from mars are both just
genericly strings" — and the same folder held EnsembleMaleVibe1,
LovelyHorns, BirdOrgans, MetalocalypseLead, AurynPad, none of them read.

**The normalizer opened a boundary between a letter and a digit and not
between a lower-case letter and a capital**, so every camel-cased name
was one long word to the lexicon. The house listing carries 37,844 of
them: SFM names every VP330 / SK1 / Wasp / 2600 / SYS100M patch folder
that way (`StringsLowGlide`, `JazzOrgan`, `EnvelopeAcid`, `TheClap`),
its MPC preset trees glue the code to the machine (`SDJupiterEnv01`,
`KickDX-4`, `CowbellProto11`), Loopmasters writes `FAI_CrispShaker_13`.
`Normalize` now splits `aB` and `ABc` (a run of capitals stays one word
up to the last, which starts the next: `FMBell` → FM Bell, `SDSV` stays)
before lowercasing; all-caps and all-lower segments are untouched, so
`BD`, `SS`, `hihat` read exactly as before, and aliases — all lower case
— meet themselves.

Probed over the 165,490-file house listing with sample-vendor-annotations
at the same commit: 4,584 files move on this change alone, and read
through by folder every one is a name the vendor wrote — 722 ARP 2600
patches from `arp` (the maker's name, now avoided in the lexicon) to
bass, 291 Wasp `EnvelopeAcid` to acid, 259 SH-101 `HalfSub_tri` to sub,
249 pads, 218 leads, 210 SK1 `Vox` patches to vocal, 126 SK1 organs, 98
Jupiter Drums `SD…` presets to snare, 96 `KickDX` to kick. The residue
is under thirty files (`KeysUnlock` in Found Sounds reads keys; `It'sBeat`
is a 2600 patch that reads Drums/_General; `SDSxV` splits to SD Sx V and
its three tom multisamples read snare) — pins if they bug anyone.

Ships with sample-vendor-annotations' same-day pass (timbale / tabla /
udu / sidestick / snap / synth-vox; bell and pattern re-ranked; MR10 and
Dr Bohm loop folders): 8,679 files move in total, nothing outside the
intended folders.

## v0.9.36 — 2026-09-02 (fx is a family, not a category; a multisample is one name over many pitches)

Jonathan, on the Plan queue's "loop or one-shot?" picker: "it's offering
fx as a choice. loop, oneshot, multisample, fx. fx can be a loop or
oneshot right? or even a multisample." And on the multisample tier:
"if it's foo A#, foo C and foo D#, that's just one shots with flavors,
not a multisample."

**fx was two facets wearing one word.** Category says *how* a thing was
recorded — a hit, a phrase, a keyboard's worth of notes — and fx says
*what* it is, which is the `fx` family in `instruments.toml` and always
was. `categories.toml` carried an `fx` id as well, so any FX folder word
set the category to a non-answer that was nevertheless non-empty, and
every later tier stayed quiet. That is exactly SFM's grammar: each synth
pack has a `WAV/FX/` folder of chromatic patches (101 *BigSub1*,
*HoldAndSampleMe*; SYS100M, OB, Voyetra by the hundreds) and files racks
under `Presets/FX/` (DX100 *Tubular Bells* — every semitone C0–C7, three
takes each, the clearest multisample in the pack). All of it read
category=fx, landed in FX/_General/_Unsorted, and asked the picker the
question the folder had already answered. The category id is retired in
sample-vendor-annotations (as `kits` was on 08-30): the four vendors'
`[[category]] fx` rules go, the twelve pack `[[dir]] category = "fx"`
pins become `instrument = "fx"` pins (same statement, right facet), the
lint refuses a category id `categories.toml` doesn't know on vendor rules
and dir pins alike, and riser / downlifter / impact / transition /
spinback join the one-shots words (events by construction, like a chop
or a flam; *sweep* deliberately not — Junos ships a pad patch called
Sweep across the keyboard, and a word here beats the shape). Here,
`plan/layout.go` reads FX-ness from the family alone and treats an FX
file with no kind exactly like a flute with none; `/api/lexicon` needed
no change — the picker lists what `categories.toml` has, so it now
offers loops / one-shots / multisamples.

**The multisample tier now wants one name.** It asked for six noted
files, six distinct notes and noted files in the majority; a folder of
sixty differently-named bass hits that each carry the key they were
played at passed. Now one *name* — the stem before the note, letters
only, so `60_TBells_DX100_C3` and its `_0001` take are the same name —
must span six notes. Round-robins and velocity layers are dimensions
inside a multisample, not evidence against one; three flavours of a
patch never were one.

**Probed over the house listing (167,652 audio files, racks name-joined
to their WAV folders where the names agree):** 8,425 files move in
canonical trees, nothing outside the intended packs. fx → multisamples
6,779 (SFM WAV/FX patches; DX100 Tubular Bells to Keys/Bell/Multisamples;
S612 Synths/FX 287 under the parent's pin). fx → one-shots 867 (Emulator
08. FX, Found Sounds FX Textures, Polyend Heights and Collapse, Junos 06.
FX via a new pack default, 101's *Various One Shots* rack finally heard).
multisamples → one-shots 253: SFM Vinyl Synths, Tape Fragments, Trumpet
Fragments — the flavours, now with `default_category = "one-shots"` on
their WAV dirs. fx → nothing 353 (Databenders MISC, Blu Mar Ten FX, Origin
Sound SFX): the honest answer, and the queue's question now means it.
Plus 170 files in 101's *Keys and Pads* gain keys from a new folder
default (the 13 racks under Presets/Keys say the same per patch; Jonathan:
"just making that generic keys is probably right").

## v0.9.35 — 2026-09-02 (a drum loop is a break; the picker names each instrument once)

Jonathan, on v0.9.34, with the Plan queue for a new pack: a hundred files
named `NN_RA_Drum_Loop_124_{Full,NoKick,Stripped,Toploop}` sitting under
drums · loops — "all of those are clearly drum loops" — and `break` twice
in the instrument dropdown. Both are the same fact seen from two sides:
the lexicon carried no entry for the phrase *drum loop*, and it carries
some ids more than once.

**The phrase.** "Drum loop" is a break by its commonest name, and the
lexicon had never been told: `break` knew "breakbeat", "drum break",
"drum groove", "beat loop" — not the two words vendors actually write.
"Drum" alone is the drums catch-all, so every such file dissolved into
Drums/_General. The fix is annotation, not code (sample-vendor-annotations
`instruments.toml`): a third `break` entry carries `drum loop` /
`drum loops` / `drumloop`, and its *position* is the rule — below every
named piece and the fill (`FF_CP_124_drum_loop_venice_shaker` stays a
shaker, `Drum Loop Fill 03` a fill), below percussion's catch-all (a
Percussion folder inside a Drum Loops tree still says more than the
tree; `_Perc` on a drum-loop stem is its percussion layer), and above
`drums`, the only word it displaces. Probed with the real harvester over
the 165,490-file house listing: 1,806 files move, every one of them
drums · loops → break · loops (Zero-G Jungle Warfare 2/3 `Drum Loops/`,
Loopmasters and Sample Magic inside the Octatrack factory content, SFM
CR78 and SK1 `Drum Loops 120 BPM/`); nothing else shifts. The 64
`drum loop` paths that stay put are SFM Modular Drum Loops' `06. Tops/`
— "tops" is a layer, kept out of the lexicon on purpose (v0.9.13).

**The picker.** The lexicon repeats an id to rank a second set of words
lower (`break` now three times: "breakbeat" high, "drum loop" just above
the catch-all, "beat"/"groove" last). `/api/lexicon` handed the pickers
every entry, so the dropdown listed break as many times as the file did.
It now offers each id once, under its first entry, the way categories
already were. A `[[dir]]` pin or a correction naming `break` was never
affected — only the menu.

Tests: `TestDrumLoopIsABreak` (phrase alone, glued, folder alone, piece
in the same segment, piece on the stem vs the folder, one-shot gate,
bare "drum" still the catch-all); `TestLexiconOffersEachIdOnce`. The
existing `AU_PC_94_drum_loop_full_cp` case now expects break — the
assertion it makes (a trailing genre/pack code is not a clap) is
unchanged.

## v0.9.34 — 2026-09-02 (a bare date on an entry no longer takes the vendor down)

Jonathan, on v0.9.33, with a screenshot of Setup asking what
`WAV/3PulseWaves` holds: "I thought we saw this one had an adg or
whatever that told us where it went?" It does — the document tier
(v0.9.24) read `Presets/Basics/3PulseWaves.adg` — but "Basics" names no
instrument in the lexicon, so the rack was heard and said nothing. The
fix is annotation, not code: the Samples From Mars vendor file now reads
`basics` / `basic waveforms` as the catch-all synth (MS10 and WASP file
their raw pulse/saw/square racks under `Presets/Basics/`, Soviet Synths
under `Presets/06. Basic Waveforms/`; 18 racks; "Basic Sub" is
whole-word safe). Those multisamples land in `Synth/Multisamples/<pack>/`
after the next annotations pull and rescan.

Writing that entry surfaced a real defect: the upstream lint (L7)
demanded a bare TOML date on `observed`, while SCHEMA's example, the
correction tool's writes and this decoder all took a string — and a
bare date failed the decode of the **whole vendor file**, every
classification under it with it. Both sides now take either form:

- **`observed` on `[[dir]]` / `[[instrument]]` decodes from a TOML
  string or a TOML date** (`annotations.Date`); the corrections tool
  keeps writing the quoted form. Test covers both, and refuses an
  integer.
- lint L7 upstream accepts `"YYYY-MM-DD"` as well as the bare date.

## v0.9.33 — 2026-09-02 (a plan that vanishes underneath the Plan screen is reloaded, not a crash)

Jonathan, on v0.9.31, with a screenshot: the Plan screen dead under a
banner — `TypeError: Cannot read properties of undefined (reading 'map')`
in `renderPlan`, thrown from the update chip's five-minute poll.

The mechanism, reproduced headlessly: the server drops every built plan
the moment the files under it move (`freshInputs` — a rescan, an
annotation write, the launch re-derive that v0.9.30 stopped from doing it
seven times), and the plan's sub-endpoints then answer 409 `no plan
built for "v" yet` until the next `POST /api/plan`. `openQueueRow` stored
that answer as if it were the folder listing — `pl.files = {error}` — and
the next render of any kind, here the update poll, reached
`pl.files.files.map` and died. The Plan screen stayed dead until F5
because nothing ever cleared the bad value.

Now (`app.js`):

- **a 409 from queues / tree / folder means "rebuild", not "render this".**
  `planGone` forgets the pre-flight and everything drawn from the old
  plan; the next render sees nothing loaded and builds a fresh one, the
  same path the screen takes on first visit. Bounded to three consecutive
  rebuilds so a workspace that keeps moving underneath shows the message
  instead of spinning.
- **an answer without a list is drawn as an empty list with the reason.**
  Folder and tree renders take `files`/`dirs` only when they are arrays;
  a row or file click on a listing that is no longer there is ignored
  rather than indexed.
- **a folder answer that arrives after the user moved on is dropped**
  (`pl.sel` changed while it loaded).

Verified in a headless browser against a fixture: build plan → move an
annotation file → plan another recipe (evicts the first) → click a queue
row. Old build: the banner in the screenshot, stuck. New build: no page
error, the queue re-plans, the second click lists the folder's two files.

## v0.9.32 — 2026-09-02 (covers are thumbnails, served from disk; the slot is never empty)

"It takes a surprisingly long time to load in the album images for the
library. We should skeleton them first so it's less jarring, but also — it
shouldn't take a threadripper pro any time to display a bunch of
thumbnails."

Three things were true of `/api/art`, all of them per image, per render
(and every state change is a render):

- **it re-loaded the annotations to allow the request.** Before serving a
  vendor image it rebuilt the allow-list — every vendor TOML in every
  annotation root plus every resolver JSON — to check the URL was one it
  knew. Against the real annotations checkout (12 vendors, 510 files) that
  was ~100 ms of TOML parsing per cover, warm; a grid of forty covers was
  four seconds of it.
- **it served the full-size image.** A 46 px slot in the grid was handed
  the vendor's product shot as-is; for art shipped inside a pack, the
  cataloged file itself. A 150-pack fixture with 1600 px covers moved
  182 MB into the browser per first paint, and the browser decoded all of
  it — that is the threadripper's time.
- **art inside a pack was hashed every time.** Catalog-scheme refs went
  through `localCopy`, which SHA-256s a local source on every call — the
  right check before a preview plays or a file is copied, and a full read
  of the archive drive per cover per render here.

Now (`internal/ui/art.go`):

- **one thumbnail per image**, long edge ≤ 192 px (an 88 px pack-detail
  slot at 2× DPR), built once into `annotations-cache/img/thumb/` and served
  straight from there with an immutable cache header. A hit touches
  nothing else — not the source file, not the annotations, one catalog map
  lookup. Catalog refs key on the file's own hash (a changed file is a new
  thumbnail); vendor URLs on the URL. JPEG stays JPEG, PNG/GIF become PNG
  so logos keep their transparency, WebP passes through, anything already
  ≤ 192 px is served untouched. Box-filter downsample, standard library
  only.
- **the allow-list is memoized.** A known URL answers at once; an unknown
  one rebuilds the list at most every 2 s, so a freshly resolved pack
  shows its cover within a beat and a grid of unknowns can't become a
  grid of full loads. `/api/blurb` uses the same memo.
- **the slot is never empty.** The hue gradient and initial that
  un-annotated packs already got are now the placeholder under every
  cover, painted with the grid; the thumbnail fades in over it when it
  lands, and a broken image just leaves the placeholder. After a rebuild
  of the page, covers the browser already holds are marked shown in the
  same frame — only a genuinely new cover ever fades.

Measured on the 150-pack fixture, old build → new: bytes per first paint
182 MB → 0.5 MB; warm server time per cover 3–7 ms (catalog, local NVMe)
and ~100 ms (vendor URL, annotations reload) → ~1 ms either way; first
build of a 1600 px cover ~130 ms, once. Headless Chromium: 150 slots
with background and initial at first paint (old: 0), all 150 covers
visible immediately after a re-render, a 404 cover leaves the gradient
and initial in place.

## v0.9.31 — 2026-09-02 (the recipe head is locked; edit is a form; racks are a recipe's call)

"I still don't see how to edit a recipe. I can change a bunch of
settings right on the page, but nothing related to the companion files.
Also that UX is kind of clunky — the recipe should be more or less locked
unless I hit edit, and then a form with these options."

Two things were true. The Recipe head was a strip of live selects —
device, storage, a target tag, a layout picker, rename — each writing the
TOML the moment it changed, on the way to the vendor list. And the
Ableton-documents knob was not on the recipe at all: `[companions]` lived
only on the device profile, so "this recipe carries the racks, that one
doesn't" meant two device profiles for one Push. `limit`, `format_tree`,
`dedup`, `cuts` and `vendor_prep` had no UI whatsoever.

- **A recipe can carry `[companions]`** (SPEC §6, §4.4) and override the
  device's block whole: absent = the device decides, `types = []` = drop
  them for this recipe, otherwise this recipe's types / anchor / User
  Library prefix. Applied once where the plan loads the device
  (`plan.applyRecipeOverrides`), so materialize, the lock and migrate
  see one effective device. Validation is one definition,
  `profile.Companions.Normalize`, shared by the device loader and the
  recipe loader.
- **The head is read-only.** One line: device · storage · target ·
  layout · racks, plus limit / format tree / dedup / cuts / vendor prep
  only when off their default. Nothing on it is clickable.
- **`edit` opens one form** over every key the file has — name (a
  rename), device, storage, target with browse, layout as preset or
  custom template, limit, format tree, dedup, cuts, vendor prep, and
  Ableton racks as *device default (says what the device does)* / *ride
  along* (types, path anchor, User Library subfolder) / *drop them*.
  **save recipe** is one write, `set-options`: profiles must exist,
  enums must be the loader's, a racks override must normalize — refused
  whole with the reason as the toast and the form left open with what
  was typed. Enter in any field saves; cancel drops the edit.
- **What it writes:** each scalar in place or removed at its default (a
  recipe never carries a `limit = 0` nobody wrote), the `[companions]`
  table replaced whole and placed ahead of the first `[[include]]`;
  rules, excludes and hand comments untouched (verified on a commented
  recipe). `/api/views` now returns the whole head so the form opens
  from the file, not from memory.
- The vendor rows stay live. They are the picker (§15.1); a click there
  is the selection changing, not the recipe's identity.

## v0.9.30 — 2026-09-02 (a catalog is decoded once, not per screen)

"Library is quick on launch now, but if I go to recipe or plan or
anything it's loading catalog 4/7 or whatever. The SFM one."

Every reader of a location decoded its catalog JSONL from disk on every
call: the launch summary, the library, a plan's inputs, a harvest, the
sample lists. The archive drive's catalog carries every Live set's
sample refs, so it is the one that takes seconds to decode — and the
Plan screen was decoding it on every visit. Two things compounded: the
plan cache keys on a stamp of every file it reads (catalogs, the meta
caches, the annotations), and the launch re-derive (v0.9.27) rewrites
one meta cache per location as it walks them, even when the bytes come
out identical. Each rewrite moved the stamp, each moved stamp dropped
the cached plan, each dropped plan decoded all seven catalogs again —
and the harvest was decoding the same catalogs itself in parallel.

- **One decode per catalog file version, process-wide.** `catalog.Load`
  keeps the decoded map and hands the same map to every reader, checked
  against the file's size and mtime on each ask, dropped by `Write`.
  Concurrent first readers of a cold catalog wait for one decode instead
  of each running their own. Everything that reads a location — summary,
  packs, samples, plan inputs, harvest, the explainer, the resolver — got
  faster without changing. The map is shared, so readers treat it as
  read-only (none mutated it before; the contract is now written down).
- **A re-derive that changed nothing leaves the file alone.** The
  harvest writes its meta cache to a scratch file and compares before
  renaming over the old one; identical bytes, same file, same mtime. The
  `.format` build stamp is rewritten only when it differs. A launch on a
  new build now moves the plan stamp once (the stamp) plus once per
  location whose classifications actually changed — not seven times
  regardless.

Plan and Recipe after this: first visit after launch decodes what the
launch re-harvest hasn't already (usually nothing); every visit after
answers from the cached artifact. "loading catalogs" appears when a
catalog on disk really changed — a rescan — and nowhere else.

## v0.9.29 — 2026-09-02 (a redraw no longer throws away where you were)

"Lots of shit scrolls back to the top for no sane reason. Like if I click
play on a sample when cataloging stuff, that list scrolls back to the
top. But specifically right now, as I try to scroll down my library, I
keep getting tossed to the top. It eventually stopped, but it took
fucking forever."

One cause under every instance: the frontend has always redrawn the
whole page from a string (`#app.innerHTML = …`) on every state change,
and a rebuilt scroller starts at scroll position zero. A play click
redraws. A toast timing out redraws. Setup redraws every 0.7 s while any
scan runs — an archive-drive rescan is minutes of being yanked to the
top every 0.7 s, which is the "took forever" and the "eventually
stopped". Nothing about the redraw was the user's doing, so nothing
about it should cost the user their place.

- **A render keeps every scroller where it was.** Before the rebuild the
  live scroll position of the main pane and every inner list (plan
  queues, file lists, issue lists) is recorded under a key that survives
  the rebuild; after it, put back. Scroll events on the way (captured at
  `#app`) keep the same map current, so leaving a view and coming back
  lands where you left — pack detail → Library returns to the same row.
  Only a *different* view starts at the top: the main pane carries its
  view in `data-view` (library / samples / discover / a pack and folder /
  each screen), so Library, a pack, and Setup each keep their own place.
- **A render keeps the focused field and its caret.** Typing in the
  correction form, the search box, the recipe filter while a poll lands
  no longer loses the cursor. `applyFilters` and `renderPreservingSearch`
  did this by hand for two fields; it is now every field.
- **Setup redraws during a scan only when a row actually moved**, and a
  scan poll that fails (server busy under a big harvest) keeps polling
  instead of dying with a banner.

Verified headlessly against a 160-pack fixture: Library at 1500 px stays
at 1500 across a render and across a ten-render storm while scrolling
(old build: 0); search keeps focus and caret at 2; Setup opens at the
top and Library returns to 900; a 120-file pack scrolled to the bottom
stays at the bottom after a play click (old build: top of the list).

## v0.9.28 — 2026-09-02 (a big Live set no longer blanks the app; every build keeps a log)

Jonathan rescanned his archive drive on v0.9.27 — it finished — and the
next launch was "the chrome and nothing inside". "I think we may need
some kind of debug build or something for me to get you some logs."

What happened: since v0.9.23 a Live document's catalog entry carries
every sample it references, on one JSONL line. The catalog reader was a
`bufio.Scanner` with a 1 MiB line cap, and an archive-drive `.als` with
a few thousand refs runs past it. One such line made `catalog.Load`
answer `bufio.Scanner: token too long` for the **whole location** —
every reader of that catalog failed, `/api/packs` answered 500, the
summary silently skipped the location (0 files), and `boot()` in the
frontend had no error path, so `render()` never ran. Reproduced
headlessly against a fixture with a 1.6 MB set entry: the old build
draws an empty `#app`; this one draws the library.

- **The catalog reader has no line cap.** `catalog.Load` streams with
  `json.Decoder`; `loadCatalogCount` (the Setup row) reads lines with
  `ReadBytes`. Test: a 6,000-ref document round-trips.
- **A document lists each distinct ref once.** A set that plays one kick
  in forty clips referenced it forty times; `ParseDoc` dedupes by key,
  first-seen order. Smaller lines, same plan vote.
- **There is no debug build — every build logs.** `<workspace>/logs/mtunes.log`
  (rolls to `.1` past 4 MiB): launch line (version, commit, OS, Go,
  workspace), every scan's result or error, launch re-derivation, every
  API request that fails, takes over a second, or panics (with stack).
  A panic in a handler answers 500 with the message instead of dropping
  the connection; background goroutines (launch re-harvest, scan, plan,
  materialize, migrate, self-update, auto-scan) run under a recover that
  logs instead of killing the process. Setup's rules card names the
  file. When the workspace itself won't load, `mtunes-desktop-error.log`
  lands beside the exe. `mtunes ui` tees the same log to stderr.
- **The window is never blank.** `boot()` draws the shell first, gathers
  each launch request separately (`Promise.allSettled`), and renders
  whatever answered. What didn't — a catalog that failed to load, a
  request that errored — is a red banner above the library naming the
  endpoint, the error, and the log path. A render that throws, or any
  uncaught error, lands in a fixed banner rather than a white page.
  `/api/summary` carries `problems` (catalogs it could not read) and
  `log`.

## v0.9.27 — 2026-09-02 (a new build re-derives on launch; rescan all; the recipe's device and storage are editable)

Jonathan, on the latest build with the latest annotations: "we should
add a rescan button or something because right now there's no real way
to guarantee it other than closing the app. I also don't see a way to
edit the existing recipe."

The rescan button existed — per location, on Setup — and the closing
the app was doing something real that nothing else did: the launch
re-harvest. But that fired only when the annotations moved or the meta
*record format* changed, and the document tier (v0.9.24) changed what
harvest derives without touching the format. So a self-update relaunch
kept serving the previous build's classifications until the user
rescanned, and nothing on screen said so. Three things:

- **The meta cache is stamped with the build that wrote it.** `MetaFresh`
  now means "this format AND this build" — every new build re-derives
  every location's classifications on launch (string ops over the
  catalog; cheaper than a user wondering). Caches from before the stamp
  read as stale once, on the first launch of this build.
- **`rescan all` on Setup.** One click: annotations pulled now (no
  throttle), every location scanned — Live documents read where they
  haven't been, trees re-harvested. Progress per row as before. The
  CLASSIFICATION RULES card now states which build derived what's on
  disk (`classifications derived by this build` / `…by build X, not this
  one — rescan all` / `…refreshing under this build…`), from
  `/api/annotations` (`meta_build`, `reharvesting`).
- **The recipe head's device and storage are selects**, not tags —
  `set-device` / `set-storage` view actions rewrite the one key and refuse
  a profile that isn't on Setup. With target, layout, rename, the vendor
  rows and the pack excludes, every field of a recipe is now editable
  from the screen; the TOML's comments and hand edits survive each.

## v0.9.26 — 2026-09-01 (Setup says whether the racks were read)

Jonathan's screenshot: SuperPulse on Plan, `instrument — · nothing
spoke`, exactly the state the v0.9.24 document tier is meant to end —
and nothing in the UI to say whether it had run. Scan counted the Live
documents it read but only the CLI printed the number. Now each location
row on Setup reads `N files · M Live documents · scanned …`, counted
from the catalog itself (entries carrying `doc`, not `doc_err`), so
"did the racks land" is a glance, not a why-line; the UI scan result
carries the same count. If the row says no documents after a rescan,
the tree has no `.adg`/`.adv`/`.als` the scan could open — and if it
says documents but SuperPulse still says nothing spoke, the refs didn't
resolve, which is the case I asked to see.

## v0.9.25 — 2026-09-01 (turn companions on from the UI)

Jonathan: "do I have the ability to turn this stuff on the ui?" Partly,
and the partly was ours: the device form had the **Ableton racks**
checkbox but was create-only — no way to open the Push profile he
already had — and it wrote `user_library_prefix = "Samples"` unasked,
which is only right when the recipe target is literally `<User
Library>/Samples`. Now `/api/devices` hands each device back in form
shape, the list has **edit**, saving an existing device rewrites its
`.toml` from the form (with overwrite; the header and the form say so),
and the subfolder is a field beside the checkbox — normalised
(backslashes, stray slashes), `..` refused, empty means `Samples`.

Same conversation, two things worth writing down that were already true:
the harvest runs at **scan** over the whole location — the recipe and
materialize never gate it, so the Library step's instrument facets are
the tagging whether or not a file ever ships; and the `**/Ableton*/**`
exclude in the example recipe decides only whether racks *ride along*,
never what gets classified. The v0.9.24 document tier does need one
**rescan** of the location, because scan is what reads the racks' refs.

## v0.9.24 — 2026-09-01 (a rack's folder labels its samples)

Jonathan, on learning that Samples From Mars files `SuperPulse.adg`
under `Presets/Leads/`: "that's something we should comb for our thing
too. if there are directories that give us clear hints like that for
vendors, we should take them." They do, and the v0.9.23 catalog already
holds the join: a document's refs. The harvest gains a **document tier**
— the catalog's Live documents are inverted (sample → the documents
pointing at it, resolved in the `ableton.Resolver` order), and a
document's folders are read through the same pack / vendor / lexicon
rules a sample's get, with the format tree (`Ableton Live/`) dropped as
a format, not a label. It ranks after every word on the sample's own
path (a `Bass/` folder on the wav beats a rack folder; pins and local
corrections beat both) and before pack-name echoes, the multisample
shape and folder defaults. Documents that disagree about one file say
nothing, and the *why* names both. `Source` carries `doc`; the Plan
step's *why* reads `folder of a Live document referencing the file
"leads" on "Leads" in ".../Presets/Leads/SuperPulse.adg"`.

Measured off the house archive's listing (name-join, so ±): 1,463 of
SFM's 1,692 racks sit in a role folder; of the ~28k SFM samples with no
role word anywhere on their own path, ~15.5k get one from a rack —
leads 4.4k, bass 2.9k, keys & pads 2.8k, FX 2k, pads 0.8k — the synth
packs (SH5, Wasp, SYS100M, Mini, 2600, DX100, MS10, Voyetra, VP330)
whose `WAV/` tree is patch names. In this archive SFM is the only vendor
shipping presets at all; the tier reads Live documents because their
refs are already parsed. A vendor shipping Kontakt/EXS only would need a
name join (`SuperPulse.nki` ↔ `WAV/SuperPulse/`) — not built until one
exists. Vocoder (883 samples) is not in `instruments.toml` yet, so those
stay silent until the lexicon learns the word.

## v0.9.23 — 2026-09-01 (a rack lands beside its samples)

Jonathan asked how Ableton handles multisampled instruments and whether
the file that "just makes that shit work" could ride to Push. It can and
it already did mechanically — companions (§4.4) rewrite every `<FileRef>`
— but under his `{family}/{instrument}/{category}/{pack}/{file}` layout a
document had nowhere to go: it is not audio, so the harvest never gave it
family or instrument, and the template sent every one of Samples From
Mars' 1,692 `.adg`s to `_Unsorted/`. Live would find them there; nobody
browsing for a synth would.

Now the catalog records what a document points at (scan reads each one
whole and stores its refs; old catalogs backfill on the next scan), and
plan places the document by a vote over the referenced samples' harvested
facts. A Sampler multisample follows its zone map — `SuperPulse.adg`
lands in the same folder as `SuperPulse C0.wav`. A drum kit spanning
kick, snare and hat is a drums thing, not a hat thing: instrument and
category need two thirds of the pads to agree, else the level falls to
`_General` / `_Unsorted`. The resolution order that decides which file a
ref means is now one type, `ableton.Resolver`, used by plan and
materialize alike, so what the plan says a rack is made of is what the
rewrite wires.

Not proven on hardware yet: whether Push 3 standalone resolves the
User-Library-relative paths from a relocated library. First real `.adg`
through the pipe answers it.

## v0.9.22 — 2026-09-01 (a correction can be withdrawn)

Jonathan, first session with the Plan step: he set MS10's SuperPulse to
bass, then took the synth argument, and asked whether there was "a
reasonable way to revert things". There was not — the local layer's
only removal was *drop* on the reconcile list, and drop was gated to
entries the checkout had already made redundant. A live correction had
no undo; the closest thing was correcting the same folder again, which
overwrites the entry but never returns the files to *needs a decision*.

Every entry on the reconcile listing now has a button: *drop* when it is
redundant or unmatched, *withdraw* when it is still doing work.
Withdraw (`correct.Withdraw`, `POST /api/local/withdraw`) judges the
entry, removes it, and patches the files it covered with the re-harvest
— so the plan sees them exactly as they were before the correction, and
a folder that needed a decision needs one again. Logged as a drop with
reason `withdrawn`. The reconcile pass now loads its layers once and
`judge` hands back the re-harvest it already computed, so a withdrawal
is one pass, not two.

## v0.9.21 — 2026-09-01 (the plan's right panel earns its width)

Jonathan's first ten minutes with v0.9.20: the panel was 420px and the
file box inside it clipped names to an ellipsis. Sample names are long
and the tail is where the note, velocity and round-robin live — a name
you cannot read is a file you cannot decide about. The panel is now
`clamp(560px, 40%, 780px)`, the file box grows to ~44vh and scrolls
sideways instead of clipping (each row is as wide as its name), the
per-file instrument · category trails the name as one dim annotation,
and nothing on the Plan step ellipsizes a file or folder name any
more — the wide list wraps instead.

## v0.9.20 — 2026-09-01 (the screens are steps, not tabs)

SPEC §19.6 step 6, the last of Jonathan's 2026-09-01 ask. The tab bar
is two places and a step strip: Library · [Recipe → Plan → Materialize]
· Setup. Steps happen in order and a step is reachable only when the
one before it has something to hand over — Plan needs a recipe,
Materialize needs a plan that fits with no errors (or a run already
under way); a step that cannot be entered is dimmed and says why. The
Library's one exit into the pipeline is *materialize…*, which lands on
Recipe. Recipe's exit is *PLAN → N files*, with how many need a
decision; the meter and issues stay there because they are useful
while editing rules, but the verdict and the materialize / migrate
buttons now live on Plan, whose empty right panel shows them. Materialize
offers *back to the plan* when it is not running and tucks the lock
history behind *history & diff* rather than a top-level tab. Keyboard
numbers respect the gates.

## v0.9.19 — 2026-09-01 (the local layer does not become a shadow)

SPEC §19.6 step 5. A local entry the checkout now says itself is a
shadow: remove it and nothing moves, keep it and the layer drifts into a
second source of truth. `correct.Reconcile` judges every entry by taking
it away in memory — the checkout overlaid with the layer minus that one
entry — and re-harvesting only the files it covers; changed = 0 is
redundant, and an entry with no cataloged file under it is unmatched.
`correct.Drop` rewrites the pack file without it, deletes the file when
nothing but a borrowed identity is left, and logs the drop. On the Plan
step the layer's listing gets *check against the checkout*, per-entry
verdicts (redundant / still needed — N of M would move / no files under
it) and *drop*; a sync that changes the checkout says how many local
entries it just made redundant. On the probe, two of four seeded entries
came back redundant for the right reason: upstream already carries the
Drumtrax "Bass" block and the Rhythm Lab break pins.

## v0.9.18 — 2026-09-01 (the plan is the review surface)

SPEC §19.6 step 4: seeing before doing, and correcting what you saw.
A new **Plan** step (tab 3, or *review plan* from the Recipe screen)
reads the cached artifact two ways. **Queues** group every placement
failure by source folder, biggest first — 7,792 uncategorized files are
a few hundred folders — with the kind of question each asks (no
instrument / loop or one-shot? / family only), the facets that did
resolve, and the why. **Tree** walks the destination as it will be
written, one level at a time, every file's why one click away; that is
where a confident misfile is found. Files audition through the existing
preview endpoint.

One form for every kind, offering only what the schema can hold as a
fact: the facet (category, instrument, *word means* — a pack
`[[instrument]]` block, *skip* — a `[[dir]]` role), a value from the
lexicon, pin or default, the path it covers (a folder, or a glob within
the pack), a note, and a local-only flag. *Preview* lays the entry over
the loaded annotations in memory and re-harvests only the files it
covers (`annotations.Overlay`, `harvest.ExplainPrefix`), then shows the
blast radius — covered, changed, filled in, and the ones that currently
resolve elsewhere, grouped before → after with examples. *Apply* writes
the entry into `<workspace>/annotations.local/vendors/<vendor>/packs/<pack>.toml`
(a vendor or pack the checkout does not know gets a minimal identity
file), logs the evidence to `corrections.jsonl` — what the app resolved
and via which tier, app version, annotations SHA — and patches the meta
cache for the covered files. *Leave it* acks a folder out of the queue
without inventing a label; *this is the parser* logs a report with no
TOML. The local layer's entries are listed on the Plan step and
`/api/local/export` zips them, minus local-only entries and acks, as the
submission. `internal/correct` holds all of it; the endpoints are thin.

## v0.9.17 — 2026-09-01 (the plan is a run, and it is kept)

SPEC §19.6 step 3, and the fix for the preflight hang on Jonathan's
190k-file library. The old `/api/preflight` built the plan once per
rule (for the per-rule counts) and once more for the set — five silent
builds on a four-rule recipe, each reloading the catalog and the meta
cache — and then threw the entries away. Now `POST /api/plan {view,
disabled}` is a run: it answers from the cached artifact when the
recipe, the toggles and the library are unchanged, reports the build's
stage while it runs (loading catalogs → selecting → placing → resolving
cuts → checking), or starts one. One build per ask: every entry records
the rule that picked it (`Entry.Rule`), per-rule counts are attribution
over that one plan, and a rule toggled off reports its matches less the
excludes instead of being planned alone. `plan.Inputs` keeps catalogs,
harvested metadata and the merged annotation layers across builds,
stamped by the files they came from, so a re-plan pays for placement
only; `plan.BuildWith` takes them plus a progress callback. Materialize
and migrate start from the artifact when it is current. Measured on the
167k-file probe: cold 4.1 s with progress, cached 0 s, a rule toggle
2 s. The artifact keeps its entries — the review surface §19.2 will read
them.

## v0.9.16 — 2026-09-01 (the cascade exists)

SPEC §19.6 step 2. `annotations.Load` takes N roots and merges them in
order: the repo checkout, then `<workspace>/annotations.local/` — a
partial tree in the repo's own layout holding only what the user
asserted about their own copy. The local layer's `[[dir]]`,
`[[instrument]]` and `[[category]]` entries are prepended, its packs
union in, a vendor dir may carry only `packs/`, and no precedence rule
was added: pins were already deepest-match with the first entry winning
a tie and override blocks already first-hit, so a local entry at the
same or deeper path simply wins. Every caller now reads
`ws.AnnotationRoots()`.

Two schema additions landed in sample-vendor-annotations the same day
and harvest honours them: `default_category` / `default_instrument` on
`[[dir]]` speak last — only for a file no word, no vendor rule and no
directory shape claimed (Source tier `dir-default`) — where today's
`category` / `instrument` are pins that beat the filenames; and
`observed` / `note` provenance on `[[dir]]` and `[[instrument]]`, with a
consumer-local `local = true` the repo's lint (L7) rejects. Nothing
writes the local layer yet — that is step 4.

## v0.9.15 — 2026-09-01 (every facet says why)

The first piece of SPEC §19 (seeing before doing). Every harvested
category and instrument now carries its provenance: which tier answered
— pack `[[dir]]` pin, vendor `dedicated_packs`, vendor `[[category]]`
glob, `categories.toml` alias, the directory's multisample shape; pack /
vendor `[[instrument]]` alias or code, `instruments.toml` alias or code,
a compound segment's family catch-all, a demoted word's family
catch-all — and the exact path segment and word it fired on (`Meta.why`,
`annotations.Source`; meta cache format 3, so the next run re-harvests).
`mtunes catalog why <path>…` and `GET /api/why?location=&path=` compute
the answer fresh from the annotations on disk rather than reading the
cache, so an annotation edit can be checked before a 70 s re-harvest.
Under the hood `harvest.Run` is now a location context plus a pure
per-path `one`, which is the partial re-harvest §19.4 will lean on.
Nothing about placement changed; the probe over Jonathan's 167k-file
listing resolves identically.

## v0.9.14 — 2026-09-01 (breaks have no one-shots)

A kit called "Beat" filed its 808/909 kicks under Drums/Break/One-Shots.
Two faults, both fixed. First, `808_Kick02` normalized to `808 kick02`,
and a whole-word match sees no kick in `kick02` — the folder word was
the only label left, and `beat` is a break alias. `annotations.Normalize`
now opens a boundary wherever a letter meets a digit (`kick 02`, `bd 3`,
`tr 909`); aliases pass through the same function, so "80s", "8bit" and
"808" still meet themselves. Expect movers: every one-shot whose only
label was a glued take-number word ("Snare01", "Hat03", "BD3") and
whose folder said something vaguer.

Second, the previous version routed Dr Sample's "Break Chop" hits to
Drums/Break/One-Shots, and Jonathan's read is right: a break is a loop
by definition, so a one-shot under Break is a filing error by
construction. `[[instrument]]` entries in the shared lexicon may now
carry `category` (annotations `next`: break = loops). Harvest resolves
the category first and passes it to the instrument lexicon
(`Lexicon.ResolveIn`); a word whose category disagrees with the file's
is a title there, not a label — passed over so every lower entry gets
its turn (a "Shaker Hit" under Breaks is a shaker), and if nothing else
speaks it stands in for its family through the catch-all, ranked where
catch-alls rank. So "Beat/Perc07" and "Break Chop Dr Sample 01" land in
Drums/_General/One-Shots; "Amen Break 01" is still a break. Vendor and
pack blocks naming the id inherit the gate. Drums/Break/One-Shots can no
longer exist.

## v0.9.13 — 2026-09-01 (a dir that names the pack is not a label)

Four more strays from Jonathan, all under Drums/Break. Three were the
shared lexicon's rank order and are fixed in the annotations repo
(`dc1dc6a` → `next`): "Vocal Loop Jack To The Beat" read as a break
because `beat` is a break alias and break outranked vocal — the words
that can only mean a voice (vocal, acapella, adlib) now sit above the
drum vocabulary like the FX words do, while the loose ones (vox, voice,
choir) stay low so "Vox Continental" is still an organ. DECAP's
`percussion_loop_fill_repeating_conga` read as a break because `fill`
was a break alias — fill is its own drums entry now, ranked below every
named piece (a conga fill is a conga, a snare roll a snare) and above
the two catch-alls (`percussion_loop_fill_drum_kit` → Drums/Fill). And
Dr Sample From Mars' "Break Chop Dr Sample 01" hits read as loops
because `break` is a loops alias — `chop` is a one-shots alias that
beats the break words and loses to the loop words (a vocal chop loop is
a loop), which is why loops is two entries in categories.toml.

Moving vocal above drums exposed a harvest bug worth fixing on its own:
every in-pack dir was a label, and Splice wraps nearly every pack in
`<Label_-_Title_Audio>/`. Read as a label, that wrapper outranks the
file's own words — a kick in "Vocal Pop House 2" would have become a
vocal, and every one-shot in a Function Loops pack already reads as a
loop (`*loop*` matches the label's name). The pack dir was never a
label; a dir that restates it says nothing more. Harvest now splits a
file's dirs into labels and pack-name echoes (whole-phrase match on
normalized text, against the on-disk dir and the annotated name):
globs and both lexicons read the labels, and consult the echoes only
when nothing else on the path spoke — "Silk Vocals/RNT_silk_vocals/
loops/RNT_SV_01.wav" is still a vocal. Unwrapped Splice packs that put
"DRUMS" or "Loops" at the same depth keep those as labels. Expect
Function Loops one-shots to move out of Loops on the next plan.

## v0.9.12 — 2026-09-01 (a pack can say what its own words mean)

Drumtrax From Mars files its kicks under Bass. The pack calls them
"Bass" — the hits folder, the filename (`Bass Drumtrax 08.wav`), the
Kits copies — and the product page says "Linn-esc bass drum", but
"bass" is an honest bass in every other Samples From Mars pack, so the
vendor's `[[instrument]]` block can't say otherwise. A `[[dir]]`
instrument pin can't either: it claims a folder, and the same names
recur inside `02. Kits`, so pinning the hits folder would split one
recording across Drums/Kick and Bass and hand the kit copy to the wrong
cut. Jonathan's framing was the design: "the pack annotations need to
override rather than us doing some complex math to figure out what bass
means in this instance."

So a pack file may carry `[[instrument]]` blocks of its own, the same
shape as the vendor's. Harvest consults them first, then the vendor's,
then the shared lexicon; the block adds a meaning without inventing
labels, so a file the pack's words don't describe still reads through
the tiers below. Annotations: Drumtrax and SDS800 (the Simmons has a
Bass module — its "Bass SDS800 01" is the bass drum) each teach
`kick = ["bass"]`; the lint checks a pack block's id against the lexicon
like a `[[dir]]` pin. Left alone on purpose: Pulsar-23's "Bass Deep
Short" (its BASS voice is tonal) and Perkons' "Bass Guit" (a bass).
Older builds ignore a pack's `[[instrument]]` table; they keep reading
Drumtrax as bass rather than misreading anything else.

## v0.9.11 — 2026-09-01 (a code speaks only when no word does)

Two Splice files under Drums/Clap: `FF_CP_124_drum_loop_venice_shaker`
(CP is the pack — Club Progressive; the file is a shaker loop) and
`AU_PC_94_drum_loop_full_cp` (cp is cyberpunk; a full drum loop). The
shared lexicon listed `cp` as a plain clap alias, and clap outranks both
shaker and the generic drums entry, so a two-letter token beat the full
word written right beside it. Two letters are a drum-machine code when
the vendor writes nothing longer — and a pack code or genre tag when he
does.

So `[[instrument]]` grows `codes` (`bd`, `sd`/`sn`, `cp`, `hh` move there
from `aliases`): a code is consulted for a segment only after every
alias of every instrument has declined it, and where it speaks it ranks
as its instrument — `Drum Hits/909 CP 01.wav` is still a clap. Vendor
blocks take `codes` too; SFM's own `cp` stays a plain alias, the vendor's
assertion. Alongside, the lexicon's generic drums entry moves below the
percussion pieces: "drum" is written everywhere, and a shaker loop in a
drum-loop pack is a shaker. Older builds ignore the unknown key, so a
lexicon ahead of the binary loses the codes rather than misreading them.

## v0.9.10 — 2026-08-31 (the proof is the structure — length picks, it never blocks)

Same night, third Polyend collision, and the one that killed the length
gate: Thump. `kick_thick.wav` erroring from all three trees — `Thump 24
bit stereo`, `Thump 16 bit stereo`, `Thump 16 bit mono` — with every
piece of the machinery working: vendor globs matched, `thump.toml`'s own
`[[dir]]` map matched, the trees stripped, `cuts = "best"` on. The only
branch left was v0.9.8's equal-duration proof, and Thump fails it by
construction: alone among the 23 Palette packs it ships as three inner
zips, one per format tree (`[packs] zip_name_grammar` had recorded this
all along), and separately produced renders drift by more than the
millisecond the gate allowed. The premise "a cut vendor's trees are the
same length by construction" was a fact about 22 packs, not about the
vendor.

So the length gate is gone — for everyone, not patched for one pack.
What remains is the structural proof, now with both halves stated:
members from *different* declared trees of one pack, at the *same
relative path* inside each tree (case- and extension-insensitive; the
relpath is the sample's coordinate). The second half is new, and it is
what made removing the gate safe: without it, two genuinely different
files landing on one templated output path from two trees would have
merged silently. Duration still leads the scoring — the longest render
wins, a truncated copy can never displace a whole one, and the plan
warns how many dropped cuts disagreed. `parallel_role = "reexport"`
keeps its job scoping `vendor_prep`; it no longer changes the resolver.

And the across-the-board half of Jonathan's ask ("can we hit this
without looking at each kit manually?"): format trees are now also
recognized *structurally*, with no annotation at all. A dir directly
under a pack named like the pack plus nothing but format words —
`Thump/Thump 16 bit mono`, `Kit/Kit 16-Bit WAV`, Polyend's literal
`Pack 24 bit stereo` — reads as a tree by naming alone. The rule is
deliberately narrow: every word after the pack's name must be format
vocabulary, anchored by a channel word or bit depth; `Kicks mono`, a
bare `WAV`, and BPM-suffixed loop dirs are refused, and a pack's own
`[[dir]]` map always wins over it. Annotations still speak first —
the heuristic is the floor, not the replacement.

## v0.9.9 — 2026-08-31 (the vendor's device prep is not content)

Follow-on the same night, and the wider call: "in sfm packs we need to
ignore any of their predone shit. We prep for devices ourselves so their
work is unnecessary."

That is a different claim from v0.9.8's, and a better one. v0.9.8 treated
Samples From Mars' sampler trees as *cuts to choose between* and taught
the resolver to choose. But the cut resolver only ever sees a collision,
and a sampler tree mostly does not collide with the canonical one — SFM
files the same hit under `WAV/01. Clean/Agogo Hi 727 25.wav` and under
`Battery/727 From Mars/Assorted 1 Samples/Agogo Hi 727 25.wav`. Different
folder names, different output paths, no collision, both render. So after
v0.9.8 the eight parallel trees collapsed to one — and that survivor was
still a second copy of hits already present, landing somewhere else.
~170 files per pack, times however many SFM packs are in the library.

The reason the resolver was the wrong tool: those trees were never a
choice. They are the vendor doing, ahead of time and for hosts the owner
may not have, the one job this tool exists to do — prepare a library for
a device. Their prep is not content. It is the same recordings in a
folder shape nobody asked for, once per sampler, with that host's patches
beside it.

So `vendor_prep = "skip"` (default) drops them outright, before cuts are
scored. Scope is the vendor's own declaration — `[formats] parallel_role
= "reexport"` only, so Polyend is untouched and a cut vendor's parallel
trees stay content that `cuts` decides. And it is **a swap, never a
subtraction**: a pack's sampler trees are skipped only where that pack's
canonical tree is in the selection to replace them, so a pack shipping
nothing but a `Battery` tree keeps it, and so does one whose `WAV` tree
the recipe's globs never picked. The two v0.9.8 tests pass unchanged for
exactly that reason — they describe packs with no canonical tree.

What it cannot prove is that every hit in a sampler tree also exists
under the canonical one; nothing cheap can, since the trees are named
differently, every byte differs by construction and the trims drift. So
it does not claim to. The plan reports how many skipped names have no
same-named file under the canonical tree and gives examples — zero being
the answer that means the swap was clean, shown rather than assumed.
`vendor_prep = "keep"` renders them like any other source.

## v0.9.8 — 2026-08-31 (a re-export is not a cut — length stops being the proof)

Field report: "collision: 2 sources render to `drums/_general/one-shots/
727 from mars/727 from mars - assorted 1 samples - agogo hi 727 25.wav`
(`…/727 From Mars/Battery/…`, `…/727 From Mars/Maschine/…`) <- sfm needs a
similar fix I think."

Similar, and instructive about where the v0.9.7 rule was actually narrow.
Everything upstream of the collision worked: the format-tree strip fired
(both `Battery/` and `Maschine/` are gone from that output path), both
entries carried their tree, and `cuts = "best"` was on. The cut resolver
refused them anyway, because it demanded one thing more — that the
members share a duration. That guard is load-bearing and stays: two files
of different lengths under one name are ordinarily two recordings, and
dropping one loses audio.

It is only right for the kind of parallel tree Polyend ships. Polyend
renders once and delivers that render at three bit depths, so its cuts
*are* equal length by construction. Samples From Mars re-renders the whole
library once per host instead — `Battery`, `Maschine`, `Kontakt`, `MPC…`
beside the canonical `WAV` — and trims each render independently. The
proof is in the pack's own manifest: 727 From Mars carries 1292 audio
files and **1292 distinct sha256**, not one byte-identical pair across
eight parallel trees. Equal length there can neither prove nor disprove
that two files are the same hit.

So the annotation says which kind a vendor is: `[formats] parallel_role =
"cut"` (default) or `"reexport"`. For a re-export vendor the structure
carries the whole proof — same pack, same relative path, two trees the
vendor itself declared parallel — and **length leads the scoring**, so the
longest render wins and nothing keeps a truncated copy over a whole one.
Length is inert for cut vendors, whose renders are equal anyway, so
Polyend's behaviour is unchanged to the byte. The warning names how many
dropped cuts disagreed on length, because a silent trim is exactly the
kind of thing you want told.

The structural half of the proof is untouched: two files colliding from
inside *one* tree are still a real collision, for every vendor.

## v0.9.7 — 2026-08-31 (one sample, one cut — the device decides which)

Field report: "collision: 3 sources render to `_unsorted/polyend/bass
tools/mids/mids_antidote_c.wav` … it's grabbing everything, including
multiple copies of the same thing. it should be filtering to one of them.
in this case I'm doing a daw one, so best quality or whatever."

Every Polyend Palette pack ships its one-shots three times — `Pack 24 bit
stereo`, `Pack 16 bit stereo`, `Pack 16 bit mono`. The annotation layer
already knew (`canonical_dir` + `parallel_dirs`), and format-tree
stripping already dropped the level from output paths; what nothing did
was *choose*. So all three cuts of `Mids_Antidote_C.wav` landed on one
output path and pre-flight errored on a collision that isn't one: those
are not different samples, they are one sample cut for different machines.

`cuts = "best"` is now the default (SPEC §6). A group of entries is
treated as cuts of one sample only when its members come out of
*different* format trees of one pack **and** share a duration — the
observational proof that nothing is being thrown away. Two files of
different lengths under one name are different recordings whatever tree
they sit in, and still error; so do two files inside the same tree.

Which cut wins is scored on what comes out the far side, never on what
went in: delivered channels, then sample rate, then bit depth, each
capped at the source's own — upsampling invents nothing, and a 16-bit
file in a 24-bit container still carries 16 bits of record. Cuts that
deliver the same thing are separated by which needs no transcode, then by
the vendor's own tree order. One rule, right answer at both ends: a
24-bit stereo DAW library keeps the master; the 16-bit mono Tracker keeps
the cut Polyend made for it and copies it byte-for-byte, no ffmpeg spawn.
`cuts = "all"` opts out.

Flow-on the collision exposed: the plan's counters were tallied during
placement, before dedup and the limit cut the set down — so a library
that ships three ways reported its unsorted files three times. They are
derived from the surviving entries now (`recount`), which is what every
number on the pre-flight card claims to mean.

## v0.9.6 — 2026-08-31 (the Recipe screen shows vendors, not rules)

Field report: "I'm looking at this big hunk of rules and thinking... I
don't think we need these. right now I've got like, literally 200+ rules
added in here. for *everything* it should basically be one rule per
vendor." Correct, and the fix is not a shorter list — it is showing the
thing the person is actually choosing.

The Recipe screen is now a picker over the library: **one row per
vendor**, checked / partial / unchecked, expandable to its packs.
`[[include]]` becomes an implementation detail — checking a vendor writes
one rule and removes the narrower ones underneath it, unchecking removes
them, and unchecking a single pack of a whole-vendor rule writes an
`[[exclude]]` rather than blowing the rule back into forty. A **tidy**
button collapses every already-fully-selected vendor to one rule at once,
selection-preserving by construction, which is the cure for the 200 that
already exist. Side effect that matters on a big library: pre-flight
plans each rule separately, so 217 rules was 217 passes over the catalog
and six is six.

Three honesty constraints the model carries (SPEC §15.1): a rule that
reaches two vendors is never deleted on one vendor's say-so (its packs
get excludes instead); a rule aimed *inside* a pack reads as "part of
it", not as the whole pack; and a rule matching nothing in the library
still shows, raw and removable, so nothing in the file can hide from the
screen. Rule-to-vendor attribution is the glob's static root, not name
matching.

Preview-only rule toggles are gone with the rule list — the checkbox now
means "in the recipe", which is what everyone read it as anyway. A recipe
emptied of all rules is allowed to exist while you pick the next one
(`view.LoadRaw`); materializing one still refuses.

## v0.9.5 — 2026-08-30 (the app updates itself)

The push-test-report loop had one manual step left: every fix meant
re-downloading the exe and replacing it by hand. Now the app tracks the
rolling `latest` release the same way it tracks annotations — plain
HTTPS against the GitHub API, no git, no console windows. The binary
knows the commit it was built from (workflow injects
`version.Commit`); a poll (launch + every 5 min) compares that against
what the `latest` tag points at, and when they differ an amber chip
appears in the tab bar: *new build · update & restart*. One click
downloads the matching release asset next to the exe, verifies it
against the release's `SHA256SUMS.txt` (which also catches a
mid-publish race — exe and sums must agree or nothing is installed),
swaps it in with the Windows rename dance (a running exe can be renamed
aside but not overwritten; the `.old` is swept at next launch), and
relaunches into the new build. `/api/update` GET/POST;
`internal/selfupdate`, dependency-free. Source builds (`dev`) are never
offered updates — that's what the compiler is for.

## v0.9.4 — 2026-08-30 (you can see which rules you're on)

"I don't know if we're actually updating annotations" — and there was no
way to check: the scan-time sync is throttled (10 min per process) and
its result was one ephemeral line in the scan status, while the binary
didn't even know its own version. So a stale layout had three
indistinguishable causes: old exe, stale checkout, or a rule that never
changed. Now the Sources screen carries a CLASSIFICATION RULES card —
the annotations checkout's commit (sha · date · subject), the app
version, and an *update now* button that reaches the remote immediately,
bypassing the throttle (`/api/annotations`, GET/POST). An update reminds
you to rescan, because rules only land in the trees when harvest re-reads
them. The release workflow injects the real version via `-ldflags -X`
(source builds say `dev`). Also fixed on the way: `mtunes init <relative
path>` cloned annotations into `<ws>/ws/annotations` — the clone ran
with cwd=workspace and a workspace-relative target; the sync path is
absolute from the start now.

## v0.9.3 — 2026-08-30 (family catch-alls stop doubling the tree)

The corrective migrate after v0.9.1 surfaced the next one within the
hour: `Woodwind/Woodwind/One-Shots`, `Drums/Drums`. The shared lexicon
deliberately carries a catch-all entry per family (instrument id ==
family id: "drums", "woodwind", …) for files labeled no deeper than the
family — and `{family}/{instrument}` rendered that label twice. Now,
when a template uses both tokens and the catch-all won, `{instrument}`
renders as `_General/` (`Drums/_General/One-Shots/`) — same design
language as `_Unsorted`: the level stays uniform, the gap stays visible,
preflight counts it with an example. Paired with an instruments.toml
expansion in the annotations repo (flute/clarinet/oboe/bassoon/recorder,
trumpet/trombone/sax/tuba, violin/cello/harp were all aliases of their
family catch-alls) so most of that material now gets a real instrument
folder and `_General` is only the honest residue.

## v0.9.2 — 2026-08-30 (the annotations checkout manages itself)

The v0.9.1 fix shipped with a lousy instruction: "go `git pull` in a
second place". The annotations repo is public and moves on observation
cadence, so the tool now keeps the checkout fresh itself — `mtunes init`
clones it, and every scan (CLI and UI, manual or auto-cadenced)
fast-forwards it before harvest reads it. Serialized and throttled
(10 min) so the UI's auto-scans don't hammer the remote; never fatal —
offline/diverged/non-git all degrade to "using what you have" with a
one-line note (surfaced in the scan result). Two safety edges: ff-only
means a contributor's local commits are never clobbered, and a bare
copied `annotations/` folder inside a versioned workspace is detected
(rev-parse walks up) and left alone rather than pulling the workspace's
own repo.

## v0.9.1 — 2026-08-30 (the category level stops collapsing)

First real migrate on Jonathan's library surfaced it within the hour:
`Drums/Break` held `Loops/` and `One-Shots/` — and nine pack folders
sitting right beside them. Two causes, one screenshot. Harvest's category
tier was vendor-annotation-gated (a location with no vendor annotation
got no category at all) while instruments resolve through the shared
lexicon for everyone — so his own drums dump placed by instrument but
never by category. And the layout's drop-empty-segment rule then
collapsed `{category}` silently, promoting each pack directory into the
category level. Fixes mirror the instrument design: a shared
`categories.toml` lexicon in sample-vendor-annotations (whole-word
aliases over normalized segments, dirs deepest-first, stem last) runs as
the fallback tier behind vendor rules; and a placed file whose
`{category}` still comes up empty now lands in an `_Unsorted/` folder at
that level instead of collapsing it — the level stays uniform, the gap
stays visible, and preflight counts it. Needs a pull of the workspace
`annotations/` checkout and a rescan to take effect.

## v0.9 — 2026-08-30 (layout templates — the rim-shot problem)

Vendor/Pack mirroring is how the source is organized, not how anyone
looks for a sound: "I want a rim shot" against 220 Splice packs meant
opening them one by one. The recipe now takes `layout = "<template>"`
(§6) and the planner builds every output path from it — `{family}/
{instrument}/{category}/{pack}/{file}` puts every rim under
`Drums/Rim/One-Shots/<pack>/`. The facts were already there: harvest
resolves an instrument for 98.6 % of a 5 005-file Splice library
(category 98.3 %) from the vendor's own folder and file names, cached by
SHA, and plan simply reads that cache. Decisions: two levels at the top
(family → instrument — 10 folders on the Push, not 37), category above
pack (loops away from one-shots), `{file}` drops intra-pack folders with
the flatten disambiguation rule keeping names apart, unlabeled files go
to `_Unsorted/` in the mirror tree rather than being guessed, `as` is
ignored under a template. Locks record the layout; `diff` gained MOVE;
plan/preflight warn from the newest lock when a layout switch would leave
the old tree beside the new one (materialize still does not prune). UI:
a layout picker on the Recipe screen with presets and a custom template.

## v0.8 — 2026-08-22 (Ableton companions)

Splice packs come with Drum Racks. Materializing for Push dropped them
(non-audio), and copying them would not have helped — the racks reference
samples by the path the pack author had. `[companions]` on the device
profile (§4.4): `.adg/.adv/.als` ride along, plan lays them out with the
audio, materialize rewrites each `<FileRef>` to the materialized output
path (User-Library-relative, type 5, plus the absolute path) and the lock
pins the ref map so restore replays it. Unresolved refs (samples not in
the recipe) stay as written and warn. Both Live 11+ and Live ≤10 FileRef
dialects handled; `.alp` rejected. Needs the Push to say whether type-5
paths resolve standalone — untested on hardware as of this entry.

## v0.7 — 2026-08-22 (materialize throughput on Windows)

First materialize from the desktop build on Windows, against a DAW-profile
recipe: thousands of cmd windows flashing and ~1 s per file. Three fixes,
each driven by the same observation — for a drum hit, the ffmpeg *process*
costs far more than the transcode.

- **No console windows** (`internal/proc`): the windowsgui exe spawns
  ffmpeg/ssh with `CREATE_NO_WINDOW`. Each spawn was also a conhost.exe
  start and a Defender rescan of ffmpeg.exe.
- **Copy path**: a PCM WAV already at the device's rate/depth/channels is
  byte-copied, no ffmpeg. `transform.copy` in the lock; `out_bytes` exact.
  Universal, not daw-only — a copy is more reproducible than a transcode.
- **Batched ffmpeg**: transcodes are chunked (≤64 files, ≤24k chars of
  argv for the Windows command-line cap) into one process:
  `-i a -i b … -map 0:a:0 <args> out_a -map 1:a:0 <args> out_b`. Every
  option we use is per-output in ffmpeg, so outputs are byte-identical to
  standalone runs (tested: fold + resample + depth cases hash equal) and
  the lock keeps recording per-entry args. A failing batch retries per
  file so the skip lands on the bad source. `MTUNES_BATCH=1` disables;
  `MTUNES_WORKERS` still sets concurrency. Linux (cheap spawn): 2.2×;
  Windows: expected much larger since spawn was the runtime.

## v0.6 — 2026-08-16 → 2026-08-17 (house archive, annotations, facets)

Built against the house archive (`E:\Sample-Archives`, 113 packs, ~160k
files) after it was laid out uniformly as `<Vendor>\<Pack>\…` with sibling
original archives.

- **Location layout `vendor-dirs`** + **docs tier**: art/about shipped in
  the pack itself (`Docs\Artwork*`, `Docs\*About*`, root `*Cover*.png`)
  resolve through `/api/art` / `/api/blurb`. Lifts 107/113 house-archive
  packs out of the honest-fallback tier with zero network and zero
  annotation files.
- **Format-tree strip** (view `format_tree = "strip"`, default): the
  vendor's parallel-format level disappears from output paths using
  annotation `[formats]` / pack `[[dir]] role = "format-tree"`. Selection of
  *which* tree is still the recipe's job.
- **Display-aware naming shipped** (ideas 1 and 2 from 2026-07-17):
  `[naming] display_length = N` warns on names identical within N chars;
  `rename = "distinguishing-first"` moves the differing tokens to the front,
  iterating until nothing clashes. Syntakt template ships 16.
- **Dual-mono detection** moved to scan time: every 2-channel integer-PCM
  WAV/AIFF on a local location is checked (|L−R| ≤ 1 LSB@16-bit, every
  frame) → `audio.dual_mono` in the catalog; backfilled on rescan (31,627
  stereo files in 6 s; 3,146 dual-mono — 10%). Mono devices fold dual-mono
  with `left`, no −3 dB pad; stereo devices opt in via `[audio] dual_mono =
  "fold"`. Remote/non-PCM stay unknown and unknown never folds.
- **Harvest**: per-file bpm/key/category/tags from filename + folder
  grammar (`_C#4`, `124 Bpm`, Camelot ` - 10A`, `Bass Lines 166.5/`) plus
  annotation `[[category]]` and pack `[[dir]]` maps →
  `annotations-cache/meta/<location>.jsonl`. Runs after every scan and via
  `catalog harvest`; category globs match case-insensitively.
- **Content dedup**: view `dedup = "content"` renders identical bytes once
  (first output path in sort order), opt-in; `catalog dupes` reports groups
  (house archive: 6,726 groups, 10,420 redundant copies, 1.6 GiB).
- **Marketplace resolvers** (`internal/resolve`): pack houses are annotated
  per pack in the repo; marketplaces ship grammar + `[packs] resolver =
  "<strategy>"`. `splice-graphql` identifies a pack dir via one public
  unauthenticated query probed by a sample path (strict match — no
  wrong-pack attribution; basename search, multi-probe, yield on throttle),
  cached with negatives in `annotations-cache/resolve/<vendor>/`. Rate
  policy per strategy (burst / pace / per-run cap / persisted cooldown),
  because Splice 429s `assetsSearch` for hours after a few hundred rapid
  queries (measured 2026-08-17).
- **Instrument facet + cross-pack search**: shared `instruments.toml`
  lexicon + per-vendor abbreviation blocks; most specific label wins; 86%
  of the house archive and 97% of Splice carry one. `catalog samples
  --instrument/--family/--category/--key/--bpm/--pack/--device`; the UI's
  filter bar swaps pack cards for cross-pack sample rows.
- `location add --rescan`; annotation `[meta] description` surfaced inline
  (discontinued packs); UI chips/breadcrumbs group by vendor not location;
  `init` writes `.gitattributes` (eol=lf) and ignores `annotations/`,
  `annotations-cache/` in new workspaces.
- Cross-platform pass: `cmd/mtunes` recovered, `.gitignore` anchored
  (unanchored `mtunes` had matched `cmd/mtunes/`), Windows polish; magic
  bytes sniffed before trusting extension (Rhythm Lab ships AIFFs named
  `.wav`); 24-bit WAV output has a 68-byte EXTENSIBLE header, not 44
  (plan size math fixed); `MTUNES_WORKERS` override + non-TTY progress
  heartbeat; traffic-light inset only on macOS.

## v0.5 — 2026-07-28 (authoring in the UI)

- The UI writes recipes, device and storage profiles — surgically (append /
  remove a whole `[[include]]`, rewrite one scalar) so hand-written comments
  survive a round trip (verified byte-identical on a heavily-commented
  recipe). Only brand-new files are generated wholesale.
- Sources screen: suggestions from annotation `[install]` paths + a builtin
  table (existence checks, never a crawl), add, scan with progress,
  per-location rescan cadence + background ticker.
- Device presets as prefills with every field editable; storage capacity
  measured from a mounted volume.
- Add-to-recipe lives in the Library: a pack card's `+` or "add to recipe"
  on the folder you're looking at.

## v0.4 — 2026-07-26 (embedded UI + desktop shell)

- `mtunes ui`: the claude.ai/design prototype made real — go:embed,
  localhost only, vanilla JS. Library (pack browser, device lens, identity
  badges, artwork), Recipe (per-rule match stats, live pre-flight with fit
  meter), Materialize (live progress; resumed/skipped as first-class
  outcomes), Cards (lock history, staleness, restore as a copied command).
  Deliberate deviations from the prototype: one honest progress bar instead
  of pull/transcode/write phase pills; restore copies the CLI command.
- Pack detail drill-in with tree browse and preview-from-source (audition);
  universal sort; playback stops on nav; location chips; canonical tag
  vocabulary (`tags.toml` → `annotations.TagMap`) plumbed through browse,
  search and UI.
- **Enrichment split (decided 2026-07-26)**: the annotations repo ships
  facts and pointers only; vendor prose and pixels are fetched on demand
  into `annotations-cache/` (never committed, never redistributed).
- `cmd/mtunes-desktop`: Wails v2 around the identical embedded UI and the
  same `/api/*` handler — one HTTP contract, no bindings. Falls back to
  `~/mtunes-library` when `MTUNES_WORKSPACE` is unset (Finder launches
  don't inherit shell env). Title-bar geometry tuned by eyeball against the
  traffic lights, the only instrument that matters.
- ssh: one goroutine owns the ControlMaster; a cut stream is not corruption.
- The resilience suite (retry, resume, skip, cap) got test coverage.

## v0.3 — 2026-07-26 (card-hardened materialize)

Decided 2026-07-18 after a 3×-flaky Octatrack USB session, and a 47k-file
run aborted at 44% by a pull hash mismatch that re-hashed clean on the
server (in-flight corruption, not a stale catalog).

- Pull retries ×3 with backoff; only repeatable failures surface.
- **Resume**: an existing output at its exact planned byte size is reused
  and hashed into the lock, not re-rendered.
- **Skip-on-fail**: a persistently failing entry is skipped, the run
  continues, the skip list prints loudly; the lock pins only bytes written
  so `diff` shows the gap. Capped at 50 — past that the failure is systemic
  and the run aborts. ^C aborts without writing a lock.
- `[naming] sanitize` (char → replacement at plan time; `C#1` → `Cs1`),
  first real use SFM pitched kits vs the Octatrack's `#` allergy.
- View `target`: where the render lands is a view preference (local staging
  + manual copy is a workflow), `--to` overrides.
- `catalog packs`: pack-first browsing over the annotation layer — tier 1
  (known vendor via `annotations/` checkout) and tier 3 (top-level dirs,
  honest fallback).

## v0.2 — 2026-07-17 (machine interface)

- `--json` everywhere: the GUI and any script consume the same structs the
  human reports render.
- `catalog ls --device D`: the device lens as a CLI precursor.
- ssh connection multiplexing + single-stream batched header reads.
- View `limit = N` (first N eligible by output-path sort — the quota-device
  fill pattern); `delivery.layout = "flatten"` for folderless devices.

### First hardware contact — Syntakt, 2026-07-17

- The Syntakt has no folders: 64 flat slots → `layout = "flatten"`.
- Its list view crops names, so `BD A 808 Decay A 01..06` display
  identically → display-aware naming (ideas logged, shipped in v0.6).
- Over-length sources (>5 s) are auto-excluded and listed in the plan; no
  trimming — a 30 s break chopped to 5 s was never the sample you wanted,
  and letting Transfer silently trim is the worst option.
- Below-rate sources convert up silently: a format requirement, not a
  quality claim.
- Workspace is a user-chosen synced/git dir via `mtunes init`, no dotdir
  default.
- Octatrack `[naming]`: no documented hard limits exist; ship conservative
  warn-level heuristics.
- Storage `reserve` defaults to 10% everywhere — "how full are we willing
  to go" is a policy knob, never an accident.
- Lockfiles are kept forever; `restore` is the `git revert` equivalent.

## v0 — 2026-07-17

Sample libraries as materialized views over immutable sources: init →
locations (local, ssh) → scan (remote hashing ships a manifest, not the
library) → plan (exact post-transform arithmetic, cluster rounding,
reserve, collisions as errors) → materialize (ffmpeg, deterministic args
recorded per entry) → lockfile → verify / diff / restore. Device profiles
for Syntakt and Octatrack from the manuals (§13 of the spec); storage
profiles of kind `filesystem` and `quota`.
