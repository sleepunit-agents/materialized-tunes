# Changelog

The dated history of materialized-tunes. SPEC.md describes the tool as it
is *now*; this file is where the "decided 2026-07-17" / "shipped
2026-08-16" trail lives, with the physical observations that drove each
change, because *why* a constraint exists is as durable as the constraint.
Newest first. Versions are milestones, not releases — there is one binary
and it is whatever `main` builds.

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
