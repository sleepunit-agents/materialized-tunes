# materialized-tunes — design spec

Sample libraries for hardware samplers (and DAWs), treated as **materialized
views over an immutable source library**. Sources are cataloged and
SHA-fingerprinted once; device-specific libraries are rendered from
declarative recipes, verified against storage constraints *before* any
copying, and pinned by lockfiles so any past library is restorable years
later.

Status: **living spec** — describes the tool as built on `main`. The dated
trail (what shipped when, and the hardware observation that forced it) lives
in [CHANGELOG.md](CHANGELOG.md). Markers used below:

- `[confirm]` — a value or decision that still needs a real answer.
- `[proposed 2026-08-19]` — a refinement proposed by Art, not yet decided.
  Collected in §18 for review in one place; delete the tag when accepted.

---

## 1. Concepts

| Term | What it is |
|---|---|
| **Source location** | A place source packs live (local dir, or a dir on a remote machine over SSH). Sources are immutable. |
| **Catalog** | The scanned index of all source files: path, size, SHA-256, audio metadata. |
| **Device profile** | Audio format + naming/layout/filesystem constraints for one sampler. |
| **Storage profile** | One physical card/drive: capacity, filesystem, reserved headroom. |
| **View (recipe)** | A named selection: device + storage + path include/excludes. Human-edited TOML. |
| **Plan** | Pre-flight report for a view: exact post-transform sizes, fit check, collisions, constraint violations. No files touched. |
| **Materialization** | Rendering a view: transcode selected sources per the device profile, lay them out at a target path, write the lockfile and card metadata. |
| **Lockfile** | Machine-written record of exactly what a materialization contained: source SHAs, transform parameters, output hashes. The restorability guarantee. |

Recipes are *queries*; lockfiles are *pins*. They are operated on by different
commands, and staleness is the visible diff between them.

## 2. Workspace layout

All state lives in one workspace directory **chosen by the user** — ideally
a git repo and/or synced dir, because this directory *is* the library
definition and deserves history and backup. Resolved via `--workspace`,
then `MTUNES_WORKSPACE`; `mtunes init <dir>` scaffolds it (and offers
`git init`). **Decided 2026-07-17** — no hidden dotdir default.

```
workspace/
  config.toml            # locations, cache settings
  devices/<name>.toml    # device profiles
  storage/<name>.toml    # storage profiles
  views/<name>.toml      # recipes
  locks/<view>/<timestamp>.lock.json
  catalog/<location>.jsonl   # one manifest per location, greppable
  cache/objects/<sha:2>/<sha> # content-addressed source cache (disposable)
```

Design choices baked in here:

- **Catalog is flat JSONL, not a database.** ~200k files ≈ tens of MB; full
  scans are fast, files are human-inspectable and diffable, which matches the
  "readable years later" ethos. If it ever gets slow, SQLite is a drop-in
  swap behind the same interface.
- **Cache is content-addressed by source SHA** and safe to delete at any
  time. A remote file crosses the network at most once, ever, regardless of
  how many views use it. This is what makes a small-disk laptop workable
  against a big remote library.


### 2.1 Workspace durability `[proposed 2026-08-19]`

The workspace *is* the library definition plus every lockfile ever written.
Restorability is only as good as the workspace's second copy, so:

- `mtunes init --git` stays the default recommendation, and the workspace
  gets a **remote** (a private GitHub repo; Art mirrors it nightly alongside
  the loom vault). Locks and recipes are small text; `catalog/` is tens of
  MB of JSONL and is worth versioning too (it is what `diff` compares
  against, and it is the only record of a location that later disappears).
  `cache/`, `annotations/`, `annotations-cache/` stay ignored — derived or
  redistributable-not-ours.
- `mtunes materialize` prints a one-line nag when the workspace has
  uncommitted locks and a remote configured but not pushed. No auto-commit:
  the UI never owns the files (§17), and neither does the CLI.
- **Source-library durability is out of scope for the tool but not for
  us:** the house archive at `E:\Sample-Archives` is single-copy on a
  workstation drive. The recipe-plus-redownload argument covers vendors
  that still exist; it does not cover digitized 90s sample CDs or
  purchases whose vendors are gone. Plan of record: move the archive to
  the mirrored spinner (`F:`) and treat `E:` as a working copy, or point
  the canonical location at the NAS. Either way the catalog, with its
  SHAs, is the manifest that would let a rebuilt archive prove itself
  complete (`mtunes verify` against a location is the missing verb —
  see §12).
## 3. Source locations

```toml
# config.toml
[[locations]]
name = "splice"
type = "local"
root = "~/Splice"

[[locations]]
name = "workstation"
type = "ssh"
host = "workstation"        # resolved via ~/.ssh/config (aliases, keys, etc.)
root = "/tank/samples"
```

A location backend implements four operations: **list, stat, hash, read**.
No filesystem mount, no FUSE, no sshfs.

- `local`: direct filesystem access.
- `ssh`: listing/hashing run **remotely** (`find` + `sha256sum` over an SSH
  session) so cataloging a multi-TB library ships back a manifest, not the
  library. File reads use SFTP, and land in the local cache.

Audio metadata (duration, channels, rate, bit depth) is extracted at scan
time — locally via header parsing/ffprobe; for `ssh` locations, via remote
`ffprobe` when available, else by reading only each file's header bytes over
SFTP. `[confirm]` whether ffprobe is installed on the workstation (it almost
certainly is via ffmpeg).

Source formats: WAV, AIFF, FLAC. Anything else is cataloged (path + SHA)
but flagged unsupported-for-materialization. Format is decided by magic
bytes, not extension (Rhythm Lab ships AIFFs named `.wav`).

### 3.1 Archive layout convention (the house archive, 2026-08-16)

The reference source library lives at one local location (`archives` →
`E:\Sample-Archives` on the Windows box) laid out uniformly as
`<Vendor>\<Pack>\...` with the vendor's original download kept as a
**sibling archive** next to the extracted pack (`<Vendor>\<Pack>.zip`,
`.rar`) — SFM's own convention, applied to everyone. Pack directories keep
the vendor's internal tree verbatim (SFM's parallel format trees, Polyend's
`16 bit mono / 16 bit stereo / 24 bit stereo` trees, Zero-G's `VOL n`
folders become one pack per volume). macOS litter (`__MACOSX`, `.DS_Store`)
is purged from packs we extract ourselves; vendor-shipped litter is left
alone (dotfiles are skipped at scan anyway).

**Pack docs.** SFM packs ship `Docs\Artwork - <Pack>.jpg` and
`Docs\<Pack> - About.rtf`. Packs that arrive without art/description get the
same shape, assembled by us: `Docs\Artwork - <Pack>.<ext>` (vendor product
image) and `Docs\<Pack> - About.md` (title, creator, counts, tags, product
URL, vendor blurb, and a trailer noting the file is ours, not the vendor's).
Non-audio, so it never touches a lockfile; it is the raw material for the
pack-first browser (§11.2) — vendor grammar for the house archive is simply
"art and about live in `Docs\` at the pack root". Zero-G's Jungle Warfare is
the degenerate case: discontinued, so the About is a research note, not a
copy of a product page.

**Location layout.** A location's `layout` says how packs sit under its
root: `""` (default) — the root *is* one vendor's library and each top-level
dir is a pack (a Splice folder; set `vendor = "<slug>"` to bind annotations);
`"vendor-dirs"` — the house archive above, `<Vendor>/<Pack>/…`, where each
top-level dir names a vendor and is matched to annotations by slug / name /
alias when they exist. The browser's pack rows carry `dir` (catalog prefix,
two segments under vendor-dirs), `name` (the pack), and `vendor`.

**Docs tier.** Between "vendor annotations" and "honest fallback" sits a
third tier the archive itself provides: a pack whose `Docs/` (SFM also
numbers it, `5. Docs`) holds art and/or an About file — or a `*Cover*.png`
at the pack root, Blu Mar Ten style — gets `image` / `blurb` refs of the
form `catalog:<location>/<path>`, resolved by `/api/art` and `/api/blurb`
straight from the cataloged file (same trust boundary as preview: only
cataloged paths resolve; remote locations go through the object cache).
`About.md` yields title / prose / product URL; SFM's `About.rtf` is stripped
to prose; `.txt` passes through. Vendor annotations, when present, still win
for name/slug/identity; docs fill whatever they leave empty. On the house
archive this lifts 107 of 113 packs out of the fallback tier with zero
network and zero annotation files.


### 3.2 Catalog enrichment (derived facts about sources)

Everything below is derived from the bytes or the paths of immutable
sources, so it is keyed by SHA / catalog path, recomputed on rescan, and
never user-authored. Two tiers by cost and trust:

**In the catalog** (`catalog/<location>.jsonl`, cheap, authoritative):

- Audio header facts: format (by magic bytes), duration, channels, rate,
  bit depth.
- **`audio.dual_mono`**: for every 2-channel integer-PCM WAV/AIFF on a local
  location, whether L and R are identical (|L−R| ≤ 1 LSB@16-bit for every
  frame). Remote and non-PCM sources stay unknown (`null`), and unknown
  never folds. Entries scanned before the field existed are backfilled on
  the next rescan.

**In `annotations-cache/meta/<location>.jsonl`** (the harvest tier —
grammar-derived, cheap to regenerate, never in a lockfile):

- `harvest` derives bpm / key / category / tags from filename and folder
  grammar (`_C#4`, `124 Bpm`, Camelot ` - 10A`, `Bass Lines 166.5/`) plus
  the annotation layer's `[[category]]` and pack `[[dir]]` maps, with the
  shared `categories.toml` lexicon (whole-word aliases, dirs deepest-first
  then the stem) as the cross-vendor fallback — so unannotated vendors
  still resolve loops/one-shots. A dir that restates the pack's own name
  (Splice's `Label_-_Title_Audio` wrapper, SFM's `Maschine/<Pack>` export
  dirs) is not a label: globs and lexicons read it only when nothing else
  on the path spoke. Runs after every scan and via `catalog harvest`;
  category globs match case-insensitively.
- The **instrument facet** (§11.4) is computed the same way — from what the
  vendor labelled, never from audio analysis.

`catalog dupes` reports groups of byte-identical sources (house archive:
6,726 groups, 10,420 redundant copies, 1.6 GiB); views opt into rendering
each group once with `dedup = "content"` (§6).

**Loudness measurement** `[proposed 2026-08-19]` — see §4.5. Integrated
loudness (LUFS) and true peak per source are derived facts like
`dual_mono`, but cost a full decode per file (ffmpeg `ebur128`), so they
are measured **lazily**: for the entries a plan selects when the view or
device asks for normalization, then cached in the catalog by SHA so the
second plan is free. Never a whole-library pass unless asked
(`catalog loudness [--location L]`).
## 4. Device profiles

Two real profiles, from the horses' mouths (Syntakt User Manual OS 1.40,
Octatrack User Manual OS 1.40A):

```toml
# devices/syntakt.toml  — SP Twinshot sample support, OS 1.40+
name = "syntakt"

[audio]
format      = "wav"
bit_depth   = 16
sample_rate = 48000
channels    = "mono"
downmix     = "sum-3db"
max_duration_seconds = 5.0    # hard device limit; Transfer silently trims
                              # past 5s — we surface it instead (see plan)

[delivery]
mode   = "staged"             # output is a folder you drag into Elektron
                              # Transfer; no card, no filesystem constraints
layout = "flatten"            # the Syntakt has no folders: 64 flat slots.
                              # Found on first hardware contact 2026-07-17.
```

```toml
# devices/octatrack.toml
name = "octatrack"

[audio]
format      = "wav"           # OT also accepts aiff; we standardize on wav
bit_depth   = 16              # static machines: 16-bit ONLY; 24-bit is
                              # flex-only. 16 keeps every file usable by both.
sample_rate = 44100           # the only rate the OT accepts
channels    = "stereo"        # "mono" folds everything down; "stereo"
                              # PRESERVES source channel count (mono stays
                              # mono — upmixing doubles size for nothing)
downmix     = "sum-3db"       # used when channels = "mono"

[naming]
max_files_per_dir   = 1024     # audio pool folder limit (manual, §Audio File
                               # Compatibility)
# No hard documented limits exist (researched 2026-07-17: OT does FAT32 long
# filenames, but community reports undocumented total-path-length failures,
# special chars like '#' erroring, and 8.3 ~1 truncation artifacts). These
# are conservative HEURISTICS — warn-level, tunable:
max_filename_length = 32       # warn above this
allowed_chars       = "A-Za-z0-9 ._()-"   # ASCII-safe; violations are errors
max_path_length     = 120      # full path from card root; warn above
case_sensitive      = false    # FAT32: Kick.wav and KICK.WAV collide
sanitize            = { "#" = "s" }  # char → replacement, applied to output
                               # paths at plan time before allowed_chars is
                               # checked; C#1 → Cs1 (note convention). A
                               # rewrite that merges two names is a normal
                               # collision error. First real use: SFM pitched
                               # kits vs the OT's '#' allergy.

[filesystem]
type = "fat32"                 # drives cluster-overhead math + name rules

[delivery]
mode = "card"
```

Notes:

- `max_duration_seconds` is a first-class constraint. Sources exceeding it
  are **excluded from the selection**, with the count and file list shown in
  the plan (info, not error). No trimming in v0 — a 30s break chopped to 5s
  was never the sample you wanted, and letting Transfer silently trim is the
  worst of all options. **Decided 2026-07-17.**
- `delivery.mode = "staged"` means materialize writes a normal folder that
  you hand to the vendor's transfer tool; no `.mtunes-card.json` is written
  (Transfer wouldn't carry it), the lockfile alone is the record. `"card"`
  targets a mounted filesystem and gets the metadata file.
- We still pre-convert for staged devices even though Transfer can convert:
  we control downmix policy and resample quality, Transfer's conversion is a
  black box, and the lockfile can only pin bytes *we* produced.

- `downmix` is a real audio decision, not a detail — naive L+R summing can
  clip. Default `sum-3db` (sum with −3 dB pad). `[confirm]` preference.
  This matters doubly for the Syntakt: every stereo source folds to mono.
- Resampling uses ffmpeg's `soxr` resampler at high quality; the exact
  ffmpeg arguments are recorded in the lockfile per entry.
- Rate/depth conversion is a **format requirement, not a quality claim**:
  sources below the device rate are converted up silently, same as any
  other transform (the OT won't load anything but 44.1k, so there is no
  "keep it as-is" option). No plan noise. **Decided 2026-07-17.**


### 4.1 Display-aware naming

Found on first hardware contact 2026-07-17: the Syntakt's list view crops
names, so `BD A 808 Decay A 01..06` all display identically — the
distinguishing digits are past the crop.

- `[naming] display_length = N` — how many characters the device's browser
  shows before cropping (0 = unknown). Plan warns about names identical
  within N chars of their folder-mates. The Syntakt template ships 16 (≤18
  by observation; nobody has counted).
- `[naming] rename = "distinguishing-first"` — rewrites exactly those
  names by moving the tokens that differ to the front, iterating until
  nothing clashes (`BD A 808 Decay A 01` → `01 BD A 808 Decay A` → `A 01 BD
  A 808 Decay` once the A/B variants collide too). Untouched names stay
  untouched; results pin in the lock. Generic and collision-driven — it
  needs no vendor knowledge, which turned out to be enough.

### 4.2 Dual-mono

`[audio] dual_mono = "keep" | "fold"` (default `keep`) for
stereo-preserving devices: `fold` writes dual-mono sources (catalog verdict,
§3.2) as mono — lossless, half the bytes, and what the sound actually is.
Mono devices always fold dual-mono with `left` instead of the device
downmix (the −3 dB pad is for summing two *different* signals). The choice
is per entry and lands in the lock's ffmpeg args like any transform. Unknown
(`null`) never folds.

### 4.3 Passthrough audio `[proposed 2026-08-19]`

Ableton Live, Push 3 (standalone) and most desktop targets load whatever
the source already is; transcoding them is a lossy tax with no payoff. A
device profile may say

```toml
[audio]
format = "source"      # copy bytes verbatim; no ffmpeg, output SHA == source SHA
```

in which case plan's size math is source bytes (plus cluster rounding where
the storage is a filesystem), materialize copies and verifies by hash, and
the lock records `transform = null`. Everything else — layout, naming,
sanitize, dedup, format-tree strip, storage fit, lockfile, restore — works
unchanged. This turns mtunes into the curation + provenance layer for DAW
libraries, which is most of what §4.5 needs. Optional knobs for partial
passthrough (`format = "source"` but `bit_depth = 24` to cap 32-bit float
sources) are a refinement; the all-or-nothing version is the useful one.

### 4.4 Companions — Ableton documents (shipped 2026-08-22)

Packs ship Drum Racks (`.adg`), presets (`.adv`) and sets (`.als`) next to
their audio. They are gzipped XML whose `<FileRef>` blocks name samples by
path — the path the pack author had — and mtunes renames paths on the way
out (the `as` prefix, format-tree strip, collision renames, `.aif → .wav`).
Copied blindly they open as pads of "missing sample"; Live on a desktop
can hunt by filename, Push standalone mostly cannot. So:

```toml
[companions]
types  = ["adg", "adv", "als"]   # empty/absent = drop them (hardware samplers)
anchor = "user-library"           # | "document"
user_library_prefix = "Samples"   # where the recipe target sits inside the Live User Library
```

- **Plan** treats a selected companion as an entry of its own: output
  path follows the same `as`/strip/sanitize/rename rules as audio (the
  extension is kept), `out_bytes` is the source size (a close estimate —
  the rewritten gzip is not size-predictable). Counted as `companions`.
- **Materialize** decodes, resolves every reference to a selected source
  and rewrites it to that source's *output* path, re-encodes
  deterministically (no gzip mtime) and hashes it into the lock like any
  output. Resolution, in order: the relative path as written, anchored at
  the companion's directory and each parent; the absolute path's longest
  tail that is a selected source; a basename unique within the nearest
  enclosing directory (ambiguous = unresolved — guessing would wire a
  wrong pad). Content-dedup aliases resolve to the kept copy. Unresolved
  refs are left exactly as the pack wrote them and reported as a warning
  per document ("3 of 16 sample refs are not in this recipe").
- **What is written**: `RelativePathType` 5 (User-Library-relative) with
  `RelativePath = <prefix>/<out_path>` for `anchor = "user-library"`, or
  type 3 with a path relative to the document for `"document"`; the
  absolute `Path` (Live 11+) gets the target path for the machine that
  ran materialize. Live ≤10 `RelativePathElement` lists and `<Name>` are
  rewritten the same way and the stale `SearchHint/PathHint` cleared.
  Nothing outside `<FileRef>` blocks is touched; `OriginalFileSize`/`Crc`
  are left as the pack wrote them.
- **Lock** records `transform.companion = true` and `transform.refs`
  (reference as written → output path), so restore replays the exact
  rewrite without resolving. Diff reports `new transform` when a sample a
  document points at no longer lands where the lock wrote it. The
  absolute `Path` follows the target, so restoring to a different
  directory changes bytes — a warning, the references are the same.
- `.alp` is a pack installer archive, not a document; it is rejected in
  `types`.

On-device status: the XML dialects and `RelativePathType` semantics are
from Live's own files and community tooling, fixtured in
`internal/ableton`; whether Push 3 standalone resolves type-5 paths from a
relocated User Library is the thing only the hardware can say.

### 4.5 Loudness normalization `[proposed 2026-08-19]`

Sources span digitized 90s sample CDs and 2026 marketplace packs; they are
not level-matched to each other, and some aren't matched to themselves.
A hardware sampler with one output level per slot wants them brought into a
band. This is a **device default with a view override**, because headroom
is a device fact (eurorack wants −14 LUFS-ish for the modular level) while
"should this collection be normalized" is taste.

```toml
# devices/<name>.toml
[audio.loudness]
mode        = "lufs"     # "none" (default) | "lufs" | "peak"
target      = -14.0      # LUFS for mode=lufs; dBFS for mode=peak
true_peak   = -1.0       # dBTP ceiling; gain is clamped so no output exceeds it
```

```toml
# views/<name>.toml — overrides the device default for this recipe only
[loudness]
mode = "none"
```

Semantics (decided by what pins cleanly):

- **Linear gain only.** Measure each source's integrated loudness and true
  peak (§3.2, cached by SHA), compute `gain = target − measured_I`, clamp
  so `measured_TP + gain ≤ true_peak`, and apply `volume=<gain>dB` in the
  ffmpeg chain before the downmix/resample stages. No dynamics, no
  limiter: samplebank's single-pass `loudnorm` (without measured values it
  runs the filter in *dynamic* mode) is gain-riding, which smears
  transients on one-shots — exactly the material this is for. Linear gain
  is deterministic, bit-exact across ffmpeg versions for integer output,
  and costs nothing in size math.
- **Per-file, not per-collection.** Each entry's measured values and
  applied gain are recorded in its lock entry; a restore reproduces the
  gain, not a re-measurement.
- `mode = "peak"` is the one-shot-friendly alternative (normalize the
  sample peak to `target` dBFS): integrated LUFS over a 200 ms kick is a
  meaningful number but not a musically useful one, and EBU's gating
  behaves oddly on very short material. Default recommendation: `peak` for
  one-shot-heavy devices (Syntakt, Digitakt), `lufs` for loop libraries
  and eurorack players. `[confirm]` the defaults per preset.
- Measurement is on the **source**, before the device's downmix, so the
  cached value is device-independent. The −3 dB fold pad and mono summing
  shift loudness by at most ~1 LU on true-stereo material; that error is
  accepted and documented rather than paying for a per-device measurement
  pass. `[confirm]` acceptable.
- Plan reports the gain distribution (min / median / max, and how many
  entries were peak-clamped) so a recipe full of quiet CD rips is visible
  before it's rendered. A clamp is info, not a warning: the ceiling is the
  policy.

### 4.6 Device preset roadmap `[proposed 2026-08-19]`

Presets (UI `/api/presets`, workspace templates) are prefills, not gospel;
the profile schema is what matters. Current: Octatrack, Digitakt, Digitakt
II, Syntakt, Model:Samples, SP-404MKII, Deluge, generic card. Missing, in
priority order given what is actually on the desk:

| Preset | Shape | What the schema needs |
|---|---|---|
| **Ableton Live (User Library)** | `format = "source"` (§4.3), `mode = "card"` to a folder under *Places*, `layout = "mirror"` | Passthrough. Live writes `.asd` analysis sidecars next to samples — device-written, keyed to output identity: the sidecar-sync case in §12. |
| **Ableton Push 3 standalone** | as Live; target is the Push's internal drive over USB/network | Passthrough. `[confirm]` transport (Push appears as a drive / SMB share when standalone) and any path-length or character quirks. |
| **Ableton Move** | `mode = "staged"` — samples import through Move Manager (`move.local`) — `layout = "mirror"` (Move browses folders) | `[confirm]` from the Move manual: accepted formats (WAV/AIFF/MP3 at least), any rate/depth/length/count limits, storage size. A staged-folder device like the Syntakt, but folder-aware. |
| **Elektron Digitakt / Digitakt II / Model:Samples** | already present | Add `max_duration_seconds` / slot counts from their manuals, same sourcing rigor as §13. |
| **1010music Bitbox (mk2 / micro)** | card, FAT32/exFAT, `layout = "mirror"`, 16/24-bit 44.1/48 k stereo | Straight from samplebank's preset; `[confirm]` current firmware limits. |
| **Sherpa Raw Waves V2** | card, 16-bit 44.1 k **mono**, "stations" as subfolders | `loudness` (§4.4) is the original itch (−14 LUFS). `stations` = a mirror layout where each top-level `as` is a station — no new schema. |
| **Torso S-4** | card, stereo 48 k | From samplebank's preset; `[confirm]`. |
| **Polyend Play / Tracker** | card, 16-bit 44.1 k, strict folder/name rules | `[confirm]` from manuals; the Polyend packs in the house archive already ship a `16 bit mono` tree for them. |
| **Roland SP-404MKII** | present | `[confirm]` the import-folder naming the device expects. |

Rule of engagement for adding any of these: facts come from the manual
(URL + section + date in §13), the first real card/folder gets a note in
CHANGELOG.md, and nothing is asserted that hasn't been looked up.
## 5. Storage profiles

Two kinds. A **filesystem** is a mounted card/drive; a **quota** is
device-managed sample memory with slot and byte limits, no filesystem
semantics at all.

```toml
# storage/octatrack-cf.toml
name           = "octatrack-cf"
kind           = "filesystem"
capacity_bytes = 31_914_983_424   # [confirm] real value: `diskutil info` on the
                                  # mounted card — marketing "32GB" is a lie
reserve        = "10%"            # headroom kept free for device-written files
                                  # (.ot sidecars, OT recorder buffers, etc.).
                                  # Default when omitted: 10% — max fill is a
                                  # policy, not an accident
cluster_bytes  = 32768            # FAT32 allocation unit, for overhead math
```

```toml
# storage/syntakt-plusdrive.toml
name           = "syntakt-plusdrive"
kind           = "quota"
capacity_bytes = 33_554_432       # 32 MB sample memory (manual §6.2.4)
max_files      = 64               # sample slots — a COUNT constraint, not bytes
```

Fit math:

- `filesystem`: each output file's size is rounded up to `cluster_bytes`
  before summing (ten thousand small hits cost more than their byte sizes);
  total must fit within `capacity_bytes − reserve`.
- `quota`: no cluster rounding, no reserve; raw output bytes vs
  `capacity_bytes`, **and** file count vs `max_files`. For the Syntakt, 64
  slots will usually be the binding constraint, not the 32 MB.

Quota caveat worth a warning at materialize time: Syntakt samples are
**global across all projects on the device** — replacing the slot pool can
silently affect old projects that referenced the previous samples. The tool
can't fix that, but it can say it out loud.

Output size is **exact, not estimated**: PCM size is pure arithmetic from
duration × rate × depth × channels (+ header), computed from catalog
metadata with zero transcoding.

## 6. Views (recipes)

```toml
# views/dnb-2026.toml
name    = "dnb-2026"
device  = "octatrack"
storage = "octatrack-cf"
target  = "~/Desktop/dnb-2026"    # optional default materialize destination
                                  # ("~/" expands); --to overrides. Where the
                                  # render LANDS is a view preference, not a
                                  # device fact — flaky card USB taught us
                                  # local staging + manual copy is a workflow,
                                  # so the recipe gets to name it.
layout  = "{family}/{instrument}/{category}/{pack}/{file}"
                                  # optional output layout template (below);
                                  # absent = mirror the source under each
                                  # include's `as`.

[[include]]
location = "workstation"
glob     = "samples-from-mars/808 From Mars/**"
as       = "808"                  # optional output prefix; default = glob root

[[include]]
location = "splice"
glob     = "packs/*breaks*/**"

[[exclude]]
glob = "**/Ableton*/**"           # vendor parallel-format trees, DAW project
[[exclude]]                       # files, etc.; format_tree (below) handles the common case
glob = "**/*.asd"
```

- Selection is path-based (doublestar globs), evaluated against the catalog —
  not against the live filesystem.
- `limit = N` (view-level, optional) keeps only the first N *eligible* files
  by output-path sort — deterministic against a pinned catalog, so it locks
  cleanly. The quota-device pattern: "fill the remaining slots from this
  selection."
- Output layout is a device property (`delivery.layout`): **mirror**
  (default — source-relative paths under the include's `as` prefix) or
  **flatten** (bare filenames for folderless devices). Flatten
  disambiguates colliding names by prepending just enough trailing parent
  dirs ("KitA - Kick 01.wav"), only where needed; still-identical names
  fall through to the collision error.
- `layout = "<template>"` (view-level, optional; shipped 2026-08-30) puts
  the *recipe* in charge of the tree instead of the source. Folders
  separated by `/`, each a mix of literal text and tokens: `{vendor}`
  (annotations display name, else the location name; the top dir under
  vendor-dirs), `{pack}` (pack dir), `{family}` / `{instrument}` /
  `{category}` (harvested, §3.2 — `Drums`, `Rim`, `One-Shots`), and a leaf
  that must be the whole last segment: `{path}` (the file's path within
  the pack, format tree stripped) or `{file}` (name only — intra-pack
  folders dropped; names that then meet in one folder get their old
  folder prepended, the flatten rule). A segment whose tokens all come up
  empty is omitted — except `{category}`: a placed file with no
  loop/one-shot signal lands in an `_Unsorted/` folder at that level
  (`Drums/Kick/_Unsorted/<pack>/`), so pack folders never sit beside the
  category folders; a preflight warning counts these too. When the
  template uses both `{family}` and `{instrument}` and the label only
  goes as deep as the family (the lexicon's catch-all won: instrument id
  == family id), `{instrument}` renders as `_General/` instead of
  doubling the name (`Drums/_General/Loops/`, never `Drums/Drums/`);
  preflight counts these with an example, and teaching instruments.toml
  a finer label is the durable fix. A family the lexicon marks `flat`
  drops the `{instrument}` level entirely (`Bass/One-Shots/<pack>/`) —
  bass and synth sub-typing is genre jargon read out of filenames, not a
  label worth a folder — except for a single entry marked `split`, a real
  instrument the vendor names outright inside an otherwise-jargon family
  (`Bass/Upright Bass/One-Shots/`); its folder name comes from the
  lexicon's `display` when set. Under a template with no `{family}`, a
  flat family renders its own name at `{instrument}` and a split entry
  renders its own. A file with
  **no instrument label** cannot be
  placed by a template that asks for one and goes to
  `_Unsorted/{vendor}/{pack}/{path}` — the mirror tree, one folder down —
  never guessed from audio (98.6 % of a 5 k-file Splice library resolves;
  the rest is the `_Unsorted` folder, and a preflight warning counts it).
  When a layout is set, `as` is ignored (preflight says so once) and two
  rules picking one file are one output. The UI offers presets
  (Family/Instrument/Loop-or-Shot/Pack, Instrument/Vendor/Pack, Family/
  Pack) and a custom template; the recipe stores only the string. Needs
  the location's harvest cache when the template reads metadata (a scan
  writes it; plan refuses otherwise rather than filing everything under
  `_Unsorted`). Switching layout on a live target moves every file:
  materialize does not prune, so preflight warns with the count from the
  newest lock ("N of M files now land at a different path") and points at
  `mtunes migrate` (below); the alternative is emptying the target first,
  or the old tree stays beside the new one.
- **`mtunes migrate <view>`** (shipped 2026-08-30) executes a layout (or
  `as`) change by *renaming* the last materialize into the new tree —
  near-instant on one volume, nothing re-rendered, no duplicate trees.
  Scope is exactly the diff's "would MOVE" set: a locked file whose source
  SHA and transform are unchanged and whose size on the target still
  matches the lock is renamed (two-phase, via a `.mtunes-mig` temp beside
  the destination, so swaps/chains and case-only renames are safe and an
  interrupted run resumes). Ableton companions are re-rendered from source
  instead — the sample paths written inside them are the layout — and the
  old copy is deleted only after its bytes are verified against the lock.
  Directories the moves emptied are removed (`os.Remove` semantics: never
  a dir still holding anything). Everything else — new selections, content
  drift, transform changes, size-drifted outputs — is left for a follow-up
  materialize and reported. A new lock records the target as it now
  stands; diff against it is clean. `--dry-run` lists the renames;
  `--to` overrides the target. The UI offers it on preflight whenever the
  newest lock has movable files ("MIGRATE — move N files into the new
  layout").
- Excludes apply across all includes.
- `format_tree = "strip"` (default) drops the vendor's parallel-format
  level from output paths using annotations (`[formats] canonical_dir` /
  `parallel_dirs`, or a pack `[[dir]]` with `role = "format-tree"`): `808
  From Mars/WAV/Kicks/x` → `808 From Mars/Kicks/x`, `ASMR/ASMR 24 bit
  stereo/y` → `ASMR/y`. Where annotations are silent, a structural rule
  reads the dir's own name: a dir directly under a pack named like the
  pack plus nothing but format words — `Thump/Thump 16 bit mono`,
  `Kit/Kit 16-Bit WAV` — is a tree with no annotation at all, so vendors
  nobody has written up still shed their format level. The rule is
  narrow (every word after the pack's name must be format vocabulary,
  anchored by a channel word or bit depth; `Kicks mono`, a bare `WAV`, a
  BPM-suffixed loop dir are all refused) and a pack's own `[[dir]]` map
  always wins over it. Category dirs at pack root are never trees (Rhythm
  Lab, BMT, Polyend Heights); an include whose glob root already reaches
  into the tree is left to its `as` (`"keep"` to disable).
- `vendor_prep = "skip"` (default) drops a re-export vendor's per-sampler
  trees outright, before cuts are scored. Samples From Mars does not ship
  one library — it ships it again under `Battery/`, `Maschine/`, `MPC Live
  & X/`, `Kontakt/`, `Ableton Live/`, each with that host's patches beside
  audio re-rendered and re-trimmed for it. That is the vendor doing, ahead
  of time and for machines the owner may not have, the job mtunes exists
  to do; the tool prepares for *this* device, so the vendor's prep is the
  same recordings in a folder shape nobody asked for. Scope is the
  vendor's own declaration (`[formats] parallel_role = "reexport"` only —
  a cut vendor's parallel trees are content, and `cuts` decides those) and
  it is **a swap, never a subtraction**: a pack's sampler trees are
  skipped only where that pack's canonical tree is present in the
  selection to replace them, so a pack shipping nothing but a `Battery`
  tree, or one whose `WAV` tree the globs never picked, keeps what it has.
  What it cannot prove is that every hit in a sampler tree also exists
  under the canonical one — the trees are named differently, every byte
  differs by construction and durations drift — so the plan says out loud
  how many skipped names have no same-named file under the canonical tree
  and gives examples; zero is the answer that means the swap was clean,
  and it is reported rather than assumed. `vendor_prep = "keep"` renders
  them like any other source (`cuts` then dedupes them as before).
- `cuts = "best"` (default) picks *which* tree, per file, from what the
  device can take. When a pack ships one sample under several trees —
  every Polyend Palette pack holds its one-shots as `24 bit stereo`,
  `16 bit stereo` and `16 bit mono` — the stripped paths land on top of
  each other, and only the best-serving cut renders. "Best" is scored on
  what comes out, not what went in: delivered channels, then rate, then
  depth, each capped at the source's own (upsampling invents nothing);
  ties go to the cut needing no transcode, then to the vendor's own tree
  order. So a 24-bit stereo DAW library keeps the master, and a 16-bit
  mono tracker keeps the cut the vendor made for it, byte-for-byte. A
  group is only treated as cuts of one sample on a structural proof in
  two halves: its members come from *different* trees of one pack, and
  each sits at the *same relative path* inside its tree (case and
  extension aside — a re-render into another container is the same
  recording). Anything else is a real collision and still errors (§7).
  `cuts = "all"` renders every cut.
- Duration is deliberately not part of that proof — it *picks*, it never
  blocks. It used to gate cut vendors, on the theory that one render
  delivered at several bit depths is the same length in every tree;
  Polyend's Thump falsified it (its three trees ship as three separately
  produced zips whose trims drift), and Samples From Mars re-renders its
  whole library per host (`Battery`, `Maschine`, `Kontakt`, `MPC…` beside
  `WAV`) with independent trims. Where the structure says one sample,
  *length leads the scoring*: the longest render wins, so nothing keeps a
  truncated copy over a whole one, and the plan's warning names how many
  dropped cuts disagreed on length. (`parallel_role = "reexport"` still
  matters — it is what scopes `vendor_prep`, above.)
- `dedup = "content"` renders byte-identical sources once (first output
  path in sort order; deterministic, pinned). Opt-in, because a DAW kit
  folder wants its members even when they duplicate the one-shots folder.
- `[loudness]` overrides the device's normalization default for this recipe
  (§4.5) `[proposed 2026-08-19]`.
## 7. Plan (pre-flight)

`mtunes plan <view>` prints, without touching any file:

```
view dnb-2026 → octatrack (16-bit/44.1k stereo wav) on octatrack-cf

  1,204 files selected   (14 excluded by pattern)
  11.2 GiB post-transform  (11.4 GiB after FAT32 cluster rounding)
  fits: yes — 18.9 GiB free after reserve

  ⚠ 2 collisions: pairs of sources rendering to the same output path
      808/BD/BD_Long.wav  ← {16-bit,24-bit} variants; keeping neither, pick one
  ⚠ 3 filename violations (chars outside device profile)
  ⚠ 1 source missing from location 'splice' since last scan
  ⚠ 6 files below device sample rate (would not be upsampled)
```

For a quota device the fit lines become slot- and duration-aware:

```
view st-drums → syntakt (16-bit/48k mono wav) via staged folder

  61/64 slots used, 27.4 MB / 32 MB
  ℹ 2 sources excluded: exceed max duration 5.0s (6.2s, 11.0s)
```

Non-zero exit on any error-class finding (won't fit, missing sources,
collisions); warnings configurable. `materialize` runs the same checks and
refuses on errors unless `--force`.

Collision policy: two selected sources that map to the same output path (the
parallel-format-tree case) are an **error**, not a silent pick — resolve by
tightening the recipe.

## 8. Materialize, lockfiles, restore

```
mtunes materialize dnb-2026 --to /Volumes/OCTA
```

1. Runs the plan; aborts on errors.
2. Ensures every selected source is in the local cache (SFTP pull if remote;
   SHA-verified on arrival). Failed pulls retry ×3 with backoff — a hash
   mismatch on pull is far more often in-flight corruption than a stale
   catalog (first observed 2026-07-18: server bytes re-hashed clean after a
   pull mismatch aborted a 47k-file run at 44%). Only repeatable failures
   surface.
3. Transcodes per device profile (ffmpeg), writes outputs to `--to`.
   **Resume**: an existing output at its exact planned byte size is reused —
   hashed into the lock, not re-rendered. Deterministic transcodes make
   size+path a strong identity; interrupted writes are truncated and never
   match; the rare actual≠planned entries (resampler boundary) re-render
   harmlessly. Restore resumes the same way against locked byte counts, and
   its output-vs-lock hash check still runs on reused files.
   **Skip-on-fail**: an entry that still fails after retries is skipped, the
   run continues, and the skip list prints loudly at the end. The lock only
   pins bytes actually written, so card and lock stay consistent and
   `mtunes diff` reports skipped entries as the gap they are. Capped
   (50): past that the failure is systemic — dead card, dead link — and
   continuing would be denial, so the run aborts. A ^C still aborts without
   writing a lock. **Decided 2026-07-18** after a 3×-flaky OT USB session.
4. Writes the lockfile to `locks/dnb-2026/<timestamp>.lock.json`.
5. Writes `.mtunes-card.json` at the target root.

**Lockfile** (JSON, machine-written — recipes are for humans, locks are for
machines):

```json
{
  "view": "dnb-2026",
  "created": "2026-07-17T14:00:00Z",
  "recipe_sha256": "…",
  "layout": "{family}/{instrument}/{category}/{pack}/{file}",
  "device":  { "…snapshot of the full device profile…" : "" },
  "storage": { "…snapshot…" : "" },
  "tooling": { "mtunes": "0.1.0", "ffmpeg": "7.1" },
  "entries": [
    {
      "source":    { "location": "workstation", "path": "samples-from-mars/…/BD_01.wav",
                     "sha256": "…", "bytes": 882044 },
      "transform": { "ffmpeg_args": ["-ac","2","-ar","44100","-c:a","pcm_s16le", "…"] },
      "output":    { "path": "808/BD_01.wav", "sha256": "…", "bytes": 441044 }
    }
  ],
  "totals": { "files": 1204, "bytes": 12030198784, "bytes_on_fat32": 12240345088 }
}
```

Profiles are **snapshotted into the lock** so restores don't depend on
current profile files. Byte-exact restore is *verified* (output SHAs are
recorded) but honestly *best-effort across ffmpeg versions* — if a future
ffmpeg produces different bytes from identical args, restore warns rather
than fails; recorded tool versions make the cause diagnosable.

**Card metadata** (`.mtunes-card.json` at card root, written from rev0 even
though nothing reads it yet — this is the v10 GUI's "oh, I recognize this"):

```json
{ "card_uuid": "…", "view": "dnb-2026", "lock": "2026-07-17T140000Z", "mtunes": "0.1.0" }
```

**Restore / verify / staleness:**

```
mtunes restore locks/big-everything/2026-03-01T…lock.json --to /Volumes/OCTA
mtunes verify --card /Volumes/OCTA        # card contents vs its lock, by SHA
mtunes diff <lock>                        # lock vs current catalog + recipe:
                                          #   what a re-run would ADD (new sources),
                                          #   DROP (sources gone), CHANGE, or
                                          #   MOVE (same file, new output path —
                                          #   layout or `as` changed)
```

`diff` is the staleness surface: re-materializing an old view is always an
explicit choice between `restore` (exactly as it was) and `materialize`
(recipe against today's catalog), with the delta visible first.

## 9. CLI surface

```
mtunes init <dir> [--git]         # scaffold a workspace (+ git init, .gitattributes, .gitignore)
mtunes location add|list|remove   # add: --type local|ssh --root … [--layout vendor-dirs] [--vendor slug] [--rescan]
mtunes scan [<location>]          # build/refresh catalog (remote-hashes ssh locations);
                                  # then harvest + resolve for that location
mtunes catalog status [--json]    # per-location counts, sizes, last-scan
mtunes catalog ls [--device D] [--ineligible] [--location L] [--glob G] [--json]
                                  # --device = the device lens: only what can ride
mtunes catalog packs [--device D] [--location L] [--json]   # pack-first browse (§11)
mtunes catalog samples [--instrument I] [--family F] [--category C] [--key K]
                       [--bpm B] [--pack P] [--device D] [--json]   # cross-pack rows (§11.4)
mtunes catalog harvest [<location>]   # re-derive bpm/key/category/tags (§3.2)
mtunes catalog resolve [<location>]   # marketplace pack identity via vendor API (§11.3)
mtunes catalog dupes [<location>…] [--json]   # byte-identical groups (§3.2)
mtunes plan <view> [--json]
mtunes materialize <view> [--to <path>] [--force]   # --to defaults to view target
mtunes restore <view|lock> --to <path>               # newest lock for a view, or a lock path
mtunes verify [<view|lock>] --card <path>
mtunes diff <view|lock> [--json]
mtunes cache status|clear
mtunes ui [--addr 127.0.0.1:7315]     # embedded browser UI (§15)
```

`--json` everywhere is the machine interface: the UI (and any script)
consumes the same structs the human reports render, so CLI and UI can
never disagree about what a plan says.

`[proposed 2026-08-19]`: `mtunes catalog loudness [<location>]` (§3.2) and
`mtunes verify --location <name>` — a location against its own catalog, the
verb that turns the catalog into a manifest for a rebuilt archive (§2.1).
## 10. Implementation notes (Go)

- CLI: `spf13/cobra`. TOML: `BurntSushi/toml`. Globs: `bmatcuk/doublestar`.
- SSH: shell out to the system `ssh` binary — it honors `~/.ssh/config`,
  agents, and keys with zero code on our side. Remote listing/hashing run
  as remote commands (`find`, `sha256sum`); file reads stream over the same
  channel. A pure-Go ssh lib (`x/crypto/ssh` + `pkg/sftp`) is a later swap
  if shelling out ever hurts.
- Transcode: shell out to `ffmpeg`; metadata via `ffprobe` with a native
  WAV/AIFF header parser as fallback. ffmpeg is a hard runtime dependency
  for materialize (not for scan/plan of WAV sources).
- Hashing: SHA-256 throughout, byte-level. No audio-content fingerprinting.
- Concurrency: parallel hash/transcode workers, single-writer catalog.

## 11. Vendor annotations, pack browsing, facets

The catalog is paths and hashes; *meaning* — which pack a file belongs to,
who made it, what it is — comes from a separate, inert data layer and from
the packs themselves. None of it changes a byte of any source, and the
resolved per-file values a transform depends on are pinned in the lockfile,
so shared data evolving never changes a restore.

### 11.1 The annotations repo (facts, not taste)

`sample-vendor-annotations` (snapshotted at `<workspace>/annotations/`,
gitignored) is a schema-versioned, code-free set of TOML files per vendor.
The tool manages the snapshot itself (decided 2026-08-30): `mtunes init`
downloads it, app launch and every scan — CLI or UI, manual or cadenced —
freshen it first, so the data moves without a binary release. Sync is
plain HTTPS against the public repo's tarball API — **no git on the user's
machine, ever** (decided 2026-08-31: git shelling flashed console windows
on Windows and assumed an install we never checked for). A
`.mtunes-head.json` in the snapshot records the commit and marks the
directory as managed; a legacy git clone from an older mtunes is adopted
(replaced wholesale — unless it has local changes, which contributors
keep), and any other non-empty directory is used as-is. Freshening is
never fatal: offline or unmanaged just means "use what's there" plus a
one-line note.
Content per vendor:
names, slugs, aliases, product URLs, image URLs, content hashes, counts,
format-tree grammar (`[formats]`), category maps, `[install]` paths,
`[meta] description` for discontinued packs; plus shared `tags.toml`
(canonical tag vocabulary → `annotations.TagMap`) and `instruments.toml`
(§11.4). House rules:

- **Facts only, never taste.** "This pack's stereo is dual-mono" belongs;
  "sum sounds better" does not — taste is local. The test: two independent
  observers would write the same annotation.
- **Redistribution boundary (decided 2026-07-26):** the repo ships facts
  and *pointers*. Vendor prose (og:description) and image bytes are fetched
  by mtunes from the pack's URL on demand into
  `<workspace>/annotations-cache/` — never committed, never redistributed.
  The UI reads the cache; the repo stays legally boring.
- **Pack houses vs marketplaces (decided 2026-08-16):** houses (SFM,
  Polyend, Zero-G, …) have finite catalogs and are annotated per pack.
  Marketplaces (Splice) list more packs daily and every user's library is a
  different partial subset, so the repo ships their *grammar* plus
  `[packs] resolver = "<strategy>"` (§11.3).
- Observation dates on everything; inert data means a community
  contributions repo is low-risk long-term.

### 11.2 Pack-first browsing (three tiers + docs)

Packs are the mental unit, so `catalog packs` and the UI group by pack — a
derived presentation layer over catalog paths, no storage change. A pack row
carries `dir` (catalog prefix), `name`, `vendor`, identity match (`exact` /
`partial` with fraction, by content hash), per-device eligible counts and
converted sizes. Sources of pack identity, most trusted first:

1. **Known vendor** — the location's `vendor` slug (single-vendor root) or
   `layout = "vendor-dirs"` matched by slug / name / alias → the annotations
   repo's pack grammar.
2. **Docs tier** — what the pack ships: `Docs/Artwork*`, `Docs/*About*`
   (`.md` / `.rtf` stripped to prose / `.txt`), root `*Cover*.png`, exposed
   as `catalog:<location>/<path>` refs resolved by `/api/art` and
   `/api/blurb` (only cataloged paths resolve; remote goes through the
   object cache). Annotations win for name/slug/identity; docs fill what
   they leave empty. Lifts 107/113 house-archive packs out of fallback.
3. **Honest fallback** — top-level dirs pose as packs, no badge, and the UI
   must not make this feel broken.

(Tier "unknown vendor → heuristic inference" sits between 1 and 3 and is
not built; §12.)

### 11.3 Marketplace resolvers

`internal/resolve` implements per-vendor strategies. `splice-graphql`: one
public unauthenticated query per pack dir, probed by a sample's path within
the pack (strict match — no wrong-pack attribution; basename search,
multi-probe) → parent pack's name/slug/provider/URL/cover/tags, cached per
pack in `annotations-cache/resolve/<vendor>/` (negatives too, re-asked after
30 days). Runs after every scan of such a location and via `catalog
resolve`; browse reads the cache exactly like a repo pack.

**A vendor's free API is a favour.** Rate policy is per strategy: a burst
for small jobs (a new pack resolves instantly), a pace after it, a per-run
cap so a 220-pack library resolves over several runs, and a cooldown
persisted in `_state.json` that survives the process — Splice 429s
`assetsSearch` for hours after a few hundred rapid queries (measured
2026-08-17), and walking straight back into a limit still in force is the
failure to avoid. Per-SAMPLE calls do not scale; pack-level resolution is
fine and cached; filtering never depends on enrichment.

### 11.4 Instrument facet and cross-pack search

A jungle pack still holds 69 vocals and 16 pianos; the instrument facet is
how you reach them without abandoning pack-first browsing. Sources, in
trust order:

1. **What the vendor labelled** — `01. Bass Drum/`,
   `TA_Kick_Loop_124_D.wav`. `instruments.toml` carries canonical id,
   family, the words vendors write, `avoid` traps, and `codes` — the
   drum-machine abbreviations (`bd`/`sd`/`cp`/`hh`) that speak for a
   segment only when no alias of any instrument does, because two letters
   are also a Splice pack code or a genre tag
   (`FF_CP_124_drum_loop_venice_shaker` is a shaker loop) — applied to every
   vendor; per-vendor `[[instrument]]` blocks carry abbreviations only
   unambiguous inside one library (SFM's `CH`/`HH`/`BD`), and a pack file
   may carry its own for a word that means something else inside that
   one pack (Drumtrax From Mars' "Bass" is its kick) — pack blocks are
   consulted before the vendor's, both before the lexicon. Every label on a
   path is collected and the most specific wins (earliest in the lexicon),
   so `04. Rimshot/Rimshot TOM 31.wav` reads as a rimshot while
   `Drums/Kick 01.wav` stays a kick. Normalizing opens a boundary where a
   letter meets a digit, so the take number vendors glue on (`808_Kick02`,
   `BD3`) doesn't hide the word. An entry may carry `category` (break =
   loops): on a file already known to be something else that word is a
   title, not a label — a kit called "Beat" holds kicks — and it speaks
   only for its family through the catch-all (`Drums/_General`), after
   every lower entry has had its turn. 86% of the house archive and 97%
   of Splice carry one.
2. **Vendor APIs**, pack-level only (§11.3).
3. **Never** audio analysis, and never asking the user to tag a 160k-file
   library. Unlabelled samples stay unlabelled and simply don't match.

Surfaced by `catalog samples` and the UI's filter bar, which swaps the
Library from pack cards to cross-pack sample rows while any filter is set,
with facet counts and the device lens still applied.

### 11.5 Device lens

When building for a device, the whole browse surface filters to what CAN
materialize for it — too long, untranscodable, wrong-shaped material
disappears. It is the plan engine's eligibility predicates run as a live
filter over catalog metadata (duration/channels/rate/format), so it costs
nothing. `catalog ls --device`, `catalog packs --device`, `catalog samples
--device`, and the UI toggle are one predicate.

### 11.6 Acquisition pointers and discovery `[proposed 2026-08-19]`

The annotations repo is, by accident of doing its job, a *discovery*
surface: a registry of pack identities is a catalogue of things that
exist, and "you don't own this" is one lookup away from "here's where it
is". That is a feature worth keeping only if the line between *sample
management* and *sample sourcing* is drawn as data and checked by a
machine, not left to whoever reviews the PR. The posture is Discogs: it
lists every rare record ever pressed and points at no download.

**Identity is unconditional; pointers are gated.** Any real pack may have
an identity (`[pack]` + `[identity]`) — including out-of-print material
nobody can sell you. Whether an identity carries an *acquisition pointer*
is decided by its class:

| class | meaning | pointer | discovery |
|---|---|---|---|
| `vendor-free` | rights-holder distributes it themselves at $0 (Blu Mar Ten, Goldbaby freebies, SFM freebies, SampleRadar) | vendor URL | shown as acquirable |
| `vendor-paid` | rights-holder sells it | vendor store page | shown as acquirable; mtunes recognizes it by fingerprint if owned |
| `distributor` | a third party the vendor explicitly licensed to distribute (Loopmasters' free label samplers, Splice, Bandcamp) | distributor page; `via = "<distributor vendor slug>"` | shown as acquirable |
| `orphan` | out of print / delisted / vendor gone (Zero-G Jungle Warfare 1–3) | **none** | "recognized, not sourced" — never listed as acquirable |

A fifth state, *user-local*, is not a class: packs with no public identity
never enter the repo at all.

**The unspoken deal with vendors, stated.** Showing someone a pack they
don't own costs the vendor nothing and sends them a customer. Handing them
the bytes — even free ones — takes the email/dollar/attention the vendor
priced the freebie at. So every pointer is a *page*, never a file: the
product page, the Bandcamp release, the free-download landing page where
the vendor gets whatever they asked for. mtunes never completes the
acquisition; it links out.

**Enforcement, as lint (repo CI, `tools/lint.py`), not as etiquette:**

- **L1 domain closure.** Every vendor record declares `domains`; every
  pointer (`[acquisition] url`, `[pack] url`, `[meta] image`) must resolve
  to a host inside the owning vendor's `domains` (subdomains included) —
  or, for `class = "distributor"`, inside the `via` vendor's. A URL whose
  host is declared nowhere fails. "Everyone knows where" is not a host.
- **L2 pages, not bytes.** Pointer paths may not end in an archive or
  audio extension (`.zip .rar .7z .wav .aif …`). Link to the page.
- **L3 orphans carry no pointer.** `class = "orphan"` forbids
  `[acquisition] url`. `discontinued = true` packs may *only* be orphans
  and their `[pack] url` / `[meta] image` / `sources` are archival
  pointers restricted to the root `hosts.toml` *reference* list (Discogs,
  SoundOnSound, the Wayback Machine, …) — record-of-existence, never
  source-of-copies.
- **L4 distributors are vendors.** A `via` must name an existing vendor
  record with `role = "distributor"` and its own `domains`.
- **L5 relations resolve.** `[[relation]]` targets must exist; a
  `basis = "sha"` relation is checked against the two manifests and fails
  below the containment it claims.
- **L6 observation.** `[acquisition]` carries `observed`; a pointer older
  than 365 days is a warning, and an optional `--live` pass HEADs each
  pointer (weekly CI, not per-PR) so link rot surfaces as data.

**Subsets and samplers.** Vendors cut freebies from paid packs, bundle
volumes, and re-issue. Two sources of that fact:

1. **Content-derived**: when the free pack's manifest lines ⊂ the paid
   pack's, the relation falls out of the identity layer with no assertion.
2. **Asserted**: where the vendor re-encoded or renamed the freebie
   (common), `[[relation]]` in the pack file — `type` ∈ `subset-of`,
   `sampler-of`, `superseded-by`, `bundle-of`, `reissue-of`; `basis` ∈
   `sha` (lint-verified), `vendor-states` (cite), `observed`.

What it buys: `plan` and the UI say "you own JUNGLEJUNGLE; this freebie is
100% contained — skip" (no second copy of the same bytes for someone who
has them), and the reverse, "you have the sampler; the full pack exists
at <vendor url>". The second sentence *is* the marketplace, and it is the
honest form of one.

**Discovery surface.** `catalog packs --discover` / the UI's "not in your
library" filter list identities that (a) you don't hold (no `exact` /
`partial` match) and (b) carry a pointer — acquirable classes only.
Orphans appear under an explicit "recognized, not sourced" heading or not
at all; they never appear next to a link. Discovery is read-only over
annotations + catalog; no network beyond the existing og/resolver fetches.

**Discover view posture** `[ratified 2026-08-20]`:

- **Default view is your library; Discover is a toggle**, not a unified
  list with owned badges. No wishlist / "mark as wanted" in v0 — intent
  has no home here yet, and that's deliberate scope control.
- **Obtainable-only, on by default.** Discover ships with an
  "obtainable" filter enabled: only acquirable classes (`vendor-free`,
  `vendor-paid`, `distributor`) show. Turning it off reveals orphans
  under the "recognized, not sourced" heading. The default exists so the
  tool never leads with "go get Jungle Warfare 3" when the honest answer
  is "you can, but not in any way we're helping with" — the orphan tail
  is reference material you opt into, not a storefront.
- **Thin cards are intentional.** An unowned pack shows registry-level
  identity only: name, vendor, description, artwork, license ceiling,
  relations, the pointer. No sample auditioning, no per-hit browsing, no
  mixing unowned content into planning views — mtunes doesn't hold the
  bytes, and previewing is the vendor's job on the page we link to. The
  asymmetry between a rich owned pack and a thin discover card is the
  ownership boundary made visible, not a gap to fill.

**Refusal, as a criterion.** mtunes has no `fetch`, no downloader, no
"install this pack" — not for `vendor-free`, not for CC0. The binary's
only network calls are vendor og metadata (§11.1) and marketplace
resolvers (§11.3). The moment the tool acquires bytes it inherits every
vendor's terms and moves its center off "correct subset onto a device,
provable later". A downloader can be argued for later; it cannot be
un-argued.

**License classes.** `[acquisition] license` is a small enum so the UI can
say what the click leads to without reproducing terms: `royalty-free`
(use in music incl. commercial, no redistribution — the overwhelming
majority of houses and SampleRadar), `cc0` / `cc-by` / `cc-by-nc`
(Freesound-style), `informal-free` ("use it, donate if you like", no
written terms — Legowelt), `uncleared` (all-rights-reserved or contains
third-party material the publisher didn't clear — Blu Mar Ten's "samples
of commercial tracks, at your own risk"), `purchase`, `unknown`. The
2026-08-19 source survey (annotations repo `notes/`) is the evidence; its
headline rule — link the page, never the file, because vendor download
URLs rotate and bypass the page the vendor wants you on — is lint L2 plus
`observed`.

**License display posture** `[ratified 2026-08-19]`: the recorded license
is a ceiling on claims, not a badge mandate. A consumer may surface
`uncleared` quietly or not at all — but may never present a pack under a
license class it doesn't carry. Concretely: only `license =
"royalty-free"` may ever be labelled royalty-free; saying nothing is
always allowed, upgrading never is. (Jonathan: "fine not to show uncleared
as long as we don't claim royalty free.")

**Curators are not distributors** `[ratified 2026-08-19]`: `role =
"distributor"` is reserved for parties the rights-holder demonstrably
licensed to distribute (Loopmasters' label samplers, Splice, Bandcamp).
Curation surfaces — BPB, KVR threads, blogs, "best free packs" roundups —
never get distributor records, however useful they are for *finding*
packs: the pointer goes past them to the vendor's own page, always. If a
licensing relationship can't be evidenced, there is no `via`.

Schema detail lives in the annotations repo's SCHEMA.md (`[vendor] domains
/ role`, `[acquisition]`, `[[relation]]`, root `hosts.toml`).

## 12. Designed-for, not yet built

Ordered roughly by how much the design already accommodates them.

- **Sidecar sync** (Octatrack `.ot`, Ableton `.asd`): mutable,
  device-written files keyed to **output artifact identity** (source SHA +
  transform params), not to the view, so slices and analysis follow the
  sample across views. The lockfile already records everything needed to
  key them. Becomes urgent the moment Live is a target (§4.6).
- **Vendor-grammar rename** (SFM `take_suffix` → always front) and
  **common-token compression** (strip tokens shared by every name in a
  flat export — git-style unique abbreviation for sample names). The
  generic collision-driven `distinguishing-first` (§4.1) has been enough so
  far.
- **Auto vendor grouping**: a layout option that prefixes output with a
  vendor dir *conditionally* — multi-pack vendors group ("SFM/808 From
  Mars/…"), single-pack vendors stay flat. Pack identity now answers "how
  many packs does this vendor have in this view"; per-include `as` does it
  explicitly today.
- **Tier-2 pack inference** for unknown vendors: heuristic (audio critical
  mass, docs/artwork at dir root, sibling-zip pattern, shallow depth),
  cached as correctable annotations.
- **Local annotation layer**: the user's own overrides at the bottom of the
  cascade (`device default → vendor → pack → local`), always winning. Taste
  lives here, never in the repo. *Reading it shipped 2026-09-01 (§19.5);
  writing it is §19.6 step 4.*
- **Direct device upload** for staged devices (SysEx/SDS the way elektroid
  does it); v0 answer remains: hand the folder to Transfer.
- **Perceptual (audio-content) dedup** and **free-form tagging**. Byte dedup
  shipped; these stay open and may stay open — see the instrument facet's
  third rule.
- **Optional analysis sidecar** `[proposed 2026-08-19]`: if audio analysis
  (BPM/key detection, CLAP embeddings) ever earns a place, it is an
  *enrichment tier* like harvest — `annotations-cache/analysis/<location>.jsonl`,
  SHA-keyed, filled by a GPU box at its leisure, never consulted by plan or
  materialize, never in a lockfile, and only for material the vendor left
  unlabelled. The catalog's shape makes this additive whenever; nothing
  about the current design should bend toward it now.
- **GUI beyond the embedded UI**: the Wails shell is presentation only by
  design; richer native integration (drag a card in, pick a target folder)
  waits until the HTTP contract stops changing weekly.

## 13. Open questions

- True usable bytes of the 32 GB Octatrack CF card (`diskutil info` when
  mounted) — deferred until the Octatrack becomes a target again.
- Loudness defaults per preset and the source-side measurement
  approximation (§4.5) `[confirm]`.
- Ableton Move / Push 3 / Live facts for §4.6 presets — manuals not yet
  read; nothing asserted yet.
- Whether `catalog/` belongs in the workspace git history (§2.1 says yes;
  tens of MB of JSONL that churns on every scan is the cost).

Resolved questions and their dates are in CHANGELOG.md.

## 14. Device fact sources

- [Syntakt User Manual OS 1.40](https://www.elektron.se/wp-content/uploads/2026/03/Syntakt-User-Manual_ENG_OS1.40_260304.pdf)
  §6.2.4: 64 slots / 32 MB / 5 s per sample / 16-bit 48 kHz mono wav /
  +Drive, global across projects / Transfer converts and trims >5 s.
- [Octatrack User Manual OS 1.40A](https://www.elektron.se/wp-content/uploads/2024/09/Octatrack-User-Manual_ENG-OS1.40A_220204.pdf)
  audio file compatibility: static = 16-bit 44.1 kHz wav/aiff mono/stereo;
  flex = 16/24-bit 44.1 kHz wav/aiff mono/stereo; audio pool folder max
  1,024 files.

- To be sourced before their presets ship (§4.6): Ableton Move manual
  (sample import limits), Push 3 standalone file handling, Digitakt /
  Digitakt II / Model:Samples manuals (sample memory, slot counts,
  duration), 1010music Bitbox, Sherpa Raw Waves V2, Torso S-4, Polyend Play
  / Tracker. Until a line appears here with URL + section + date, the
  preset is a prefill, not a fact.
## 15. UI

`mtunes ui` serves the browser UI from the binary (go:embed, localhost
only, no toolchain): Library (pack browser, device lens, identity badges,
artwork from annotation image URLs), Recipe (§15.1 — vendors and packs,
not rules; live pre-flight with fit meter/reserve/issues), Materialize (real runs with
live progress, resumed/skipped surfaced as first-class outcomes), Cards
(lock history per view, staleness via the diff engine, restore as a
copied command). Design imported from the claude.ai/design prototype;
implemented as an embedded server + vanilla JS so Wails can wrap the same
assets later. Deliberate deviations from the prototype: no pull/transcode/
write phase pills (the pipeline is per-file concurrent, not phased — a
single honest progress bar instead), restore copies the CLI command
rather than writing to a target picked in a browser.

### 15.1 The Recipe screen is a picker, not a rule editor (2026-08-31)

A recipe's `[[include]]` blocks are the storage format, not the thing a
person is trying to think about. Adding packs one at a time produced
recipes with 200+ rules for what is, in the owner's head, "everything from
Splice and everything from Samples From Mars" — a list nobody can read,
and 200 passes over the catalog on every pre-flight.

So the screen shows **one row per vendor** — the same grouping the Library
uses (annotated vendor, falling back to location) — in three states:
**all** (everything you own from them is selected), **partial** (`3 of 27
packs`, with the row expandable to the packs), **none**. Checking a vendor
writes ONE rule and removes the narrower rules it subsumes; unchecking
removes them. Rules are derived, never typed.

The mapping from rules back to vendors is the glob's static root
(`view.GlobRoot`): a rule covers a pack when its root sits at or above the
pack's directory, and reads as *part of it* when it aims inside one. Three
consequences the screen has to be honest about:

- **A rule that reaches two vendors can't be deleted on one's say-so.** A
  location-wide `**` over a location holding several vendors belongs to
  all of them; unchecking one carves its packs out with `[[exclude]]`
  instead of cutting the rule out from under its neighbours.
- **Unchecking one pack of a whole-vendor rule writes an exclude**, not an
  expansion back into per-pack rules — expanding is exactly the pile this
  screen exists to remove, and the exclude keeps new packs falling in.
- **A rule matching no pack in the library is still shown**, in its raw
  `location : glob` form, and is removable. Nothing the file says
  disappears from the screen.

`tidy` is the one-gesture cure for a recipe that already grew: every
vendor fully selected by more than one rule collapses to one. It is
selection-preserving by construction — only groups already wholly in are
eligible — so pre-flight does not move.

Because checking and unchecking now edit the recipe directly, the old
preview-only rule toggles are gone; `/api/preflight` keeps its `disabled`
parameter for callers that want a dry run.

## 16. Desktop shell (Wails)

`cmd/mtunes-desktop` wraps the identical embedded UI in a native window:
Wails v2 serves ui.Assets() and falls through to the same /api/* handler
`mtunes ui` uses, so browser and desktop can never drift. Build needs
`-tags desktop,production` and (macOS, Wails 2.13) an explicit
`CGO_LDFLAGS="-framework UniformTypeIdentifiers"`. The CLI `mtunes ui`
remains the toolchain-free path; the desktop shell is presentation only —
no bindings, no IPC, one HTTP contract.

## 17. Authoring in the UI

The UI writes as well as reads, but never takes ownership of the files:
recipes, device and storage profiles stay hand-editable TOML, and UI edits
are SURGICAL — append or remove a whole `[[include]]` block, rewrite one
scalar — so comments and hand-tuning survive a round trip (verified: add
+ remove on a heavily-commented recipe is byte-identical). Only brand-new
files are generated wholesale.

- Sources (`/api/locations`, `/api/suggestions`, `/api/scan`): add a
  source, scan with progress, per-location rescan cadence + background
  ticker. Suggestions come from annotation `[install]` paths and a
  builtin table — known locations checked for existence, never a crawl.
- Recipes (`/api/view`): create, add-rule (optionally replacing the
  location's rules, or just those under one prefix — how "all of this
  vendor" lands as a single block), remove-rule, remove-rules (several
  in one write, so a caller never reasons about indexes shifting),
  add-exclude / remove-exclude (by glob, idempotent), set-target,
  set-layout (a preset or a hand-typed template, validated first). The
  Recipe screen (§15.1) drives all of these; the Library keeps its own
  add gesture — a pack card's `+`, or "add to recipe" in the detail
  view, which adds *the folder you're looking at* (so "just the acid
  loops" is two clicks, not a hand-written glob).
- A recipe emptied of every rule is a legitimate state to load, list and
  pre-flight — you just unchecked the last vendor and are about to check
  another. `view.LoadRaw` allows it; `view.Load`, which everything that
  materializes goes through, still refuses it.
- Profiles (`/api/device`, `/api/storage`, `/api/presets`,
  `/api/volumes`): device presets are prefills for known gear and every
  field is editable, because the next box out is one we've never seen;
  storage capacity can be measured from a mounted volume instead of
  looked up.

## 18. Proposed refinements — 2026-08-19 (review list)

Everything tagged `[proposed 2026-08-19]` above, in one place. Accept by
deleting the tag; reject by deleting the section and noting it in
CHANGELOG.md.

| § | Proposal | One-line case |
|---|---|---|
| 2.1 | Workspace remote + unpushed-lock nag; archive second copy | Restorability has a SPOF while locks live on one disk; the archive is single-copy on a workstation drive. |
| 3.2 / 4.5 | Loudness: lazy SHA-cached measurement, linear-gain normalization, `lufs` or `peak`, device default + view override | 90s sample CDs vs Splice packs aren't level-matched; samplebank's dynamic `loudnorm` smears one-shots; linear gain pins exactly. |
| 4.3 | Passthrough audio (`format = "source"`) | Live / Push / most desktop targets want curation and provenance, not transcoding. |
| 4.6 | Preset roadmap: Live, Push 3, Move first; then eurorack/Polyend/Bitbox | What is on the desk, plus what samplebank already had. Facts from manuals only. |
| 9 | `catalog loudness`, `verify --location` | The catalog is already a manifest; give it the verb. |
| 11.6 | Acquisition pointers + discovery: pack classes (vendor-free / vendor-paid / distributor / orphan), vendor `domains`, `[acquisition]`, `[[relation]]`, six lint rules, no-fetch refusal | The registry is a discovery surface whether we like it or not; drawing the management-vs-sourcing line as lintable data keeps it a tool and not a piracy index, and link-out sends vendors their customer. |
| 12 | Optional analysis sidecar, explicitly *not now* | Keeps the door open without bending the design toward it. |
| 19 | `[proposed 2026-09-01]` Plan as the review surface, kinds of _Unsorted, local annotation layer as cascade + diff + submission | See where files land before they do, and let the user correct facts in a shape the repo can take back. |

## 19. Seeing before doing; correcting what you saw `[shipped 2026-09-01]`

Proposed and built the same day (v0.9.15–v0.9.20; the shipped notes sit
under each subsection). Jonathan's ask (2026-09-01): *see
where things are going to go before they go there, and correct mistakes*
— and shape the correction so the user makes it, not the annotator. Three
parts: the flow the screens become, the corrections tool and its kinds,
and the local annotation layer that carries corrections out of the app.

### 19.1 The screens are steps, not tabs

Materializing is a sequence, and the UI should stop pretending otherwise:

```
Library ──"materialize…"──▶ Recipe ──▶ Plan ──▶ Materialize ──▶ (back to Plan/Recipe)
```

- **Library** keeps browsing, discovery, pack detail. Its one exit into
  the pipeline is *materialize…*, which lands on Recipe (pick an existing
  recipe or make one). Materialize is never reachable without a recipe.
- **Recipe** is the picker (§15.1). Its exit is *plan*.
- **Plan** replaces preflight-as-a-blocking-call. Building it is a *run*
  like materialize (progress by stage; `POST /api/plan` since v0.9.17),
  because on a 190k-file library the old `/api/preflight` hung the screen.
  The result is browsable (§19.2) and cached (§19.4). Its exit is
  *materialize* (or *migrate* when the lock says files would just move).
- **Materialize** shows the run, then returns to Plan with the lock diff.

The API is already step-shaped (`/api/views`, `/api/plan`,
`/api/materialize`, `/api/run`); this is mostly a frontend re-cut plus
turning preflight into a run. Nothing built under §19.2–19.5 may assume
the tab model.

*Shipped 2026-09-01 (v0.9.20):* the tab bar is two places and a step
strip — Library · [Recipe → Plan → Materialize] · Setup. A step is
reachable only when the one before it has something to hand over: Plan
needs a recipe, Materialize needs a plan that fits with no errors (or a
run already under way). Library's exit is *materialize…* → Recipe;
Recipe's exit is *PLAN → N files*; the verdict (fit, issues) and the
materialize / migrate buttons live on Plan; Materialize returns with
*back to the plan*, and the lock history moved behind it as *history &
diff*.

### 19.2 The plan is the review surface

Everything the user wants to see or fix is visible in one artifact: the
plan's entries, each with a source path, a destination path, the harvested
facets, and (new) *why* each facet resolved the way it did. Two ways in,
one action vocabulary:

- **Queues** — the plan's placement failures, grouped by **source folder**
  and sorted by count. 7,792 uncategorized files are a few hundred folders;
  one decision per folder, never per file. Each row shows the folder, the
  file count, a few filenames, the facets that did resolve, and an audition
  button (`/api/preview` already streams audio; playing a file to a human
  is not the audio analysis §11.4 refused).
- **Tree** — the destination tree as it will be written, walkable, with
  every file's *why* one click away. This is where misfiles are found
  ("Groove Therapy Pad in Drums/Break") — they are confident, so no queue
  holds them.

The **why** panel is the primitive both share: per facet, which tier
answered (pack `[[dir]]` pin / pack `[[instrument]]` / vendor block /
`categories.toml` entry / pack-name echo / multisample shape / nothing) and
the exact word and path segment it fired on.

*Shipped 2026-09-01 (v0.9.15):* `harvest.Meta.why` carries an
`annotations.Source` per facet — `tier`, `segment`, `word`, `echo` — and
the meta cache format is 3. `mtunes catalog why <path>…` and
`GET /api/why?location=&path=` harvest the path afresh from the
annotations on disk (`harvest.Explainer`), so a correction shows its
effect before the next full harvest. Harvest itself is now a per-path
pure function over a location context (`harvester.one`), which is the
partial re-harvest §19.4 needs.

### 19.3 The kinds of "unsorted" — different questions, different tools

"_Unsorted" is one folder name for at least five situations. The tool
asks a different question for each, and the answer lands in a different
annotation shape:

| # | Situation (plan flag) | What the user is asked | Answer lands as |
|---|---|---|---|
| A | **No kind** — instrument known, loop/one-shot unknown (`uncategorized`, `Drums/Hat/_Unsorted/`) | "Loops or one-shots?" per folder. The fastest queue; almost always a whole folder. A mixed folder ("One Shots/Fill 07") is answered by *word*, not by descending to files. | `[[dir]] category` — or `default_category` (§19.5) when the folder's own words should still win |
| B | **Nothing** — no instrument, so a templated layout can't open a top-level folder (`unsorted`, mirror tree under `_Unsorted/`) | "What is this folder, at whatever depth you honestly know — family, instrument, or leave it?" Most of the 32k here are SFM patches named *David Lynch*: category is known (multisamples), family is obvious from the pack, no word says it. | `[[dir]] default_instrument = "synth"` (family catch-all as a fallback, *not* a pin); a real pin only when the folder is uniform |
| C | **Family only** (`general`, `Drums/_General/`) | "Which instrument?" — often unanswerable (numbered takes) and *leave it* is the honest answer. | `[[dir]] instrument` when there is one; otherwise a local **ack** (§19.5) so the row leaves the queue without inventing a label |
| D | **Wrong** — placed confidently, misfiled | "What is it actually?" — with the *why* shown, so the fix targets the level that lied: a word that means something else in this pack (Drumtrax's *Bass*), a folder, or a lexicon bug. | pack `[[instrument]] aliases` / `avoid`; `[[dir]]` pin; or a **report** (evidence only, no TOML) when the user says "this is your parser, not my pack" |
| E | **Not content / wrong boundary** — a nested format tree (`Modular Creations/4. Modular Instruments/Kontakt 5.5`), docs, a dir that is really a pack | "Skip this subtree" / "this is a pack". | `[[dir]] role = "format-tree" \| "docs"`; pack-boundary edits stay out of v1 (grammar is vendor-level and rarely wrong) — and the nested-format-tree case is a consumer bug (roles honoured only at pack root) to fix regardless |

Three rules the tool enforces so corrections stay facts:

1. **Scoped, not global.** Users write `[[dir]]` entries and pack/vendor
   `[[instrument]]` blocks — things scoped to a folder or a pack they own.
   The shared lexicons (`instruments.toml`, `categories.toml`) stay curated:
   "tops" as a hat word broke *Clave Slider* across the whole archive
   (2026-09-01). A user's scoped fix is the *evidence* for a global one.
2. **Blast radius before commit.** Every correction re-plans the files it
   covers and shows the diff before it is written: "pins 143 files; 12
   currently resolve to something else: …". Seeing before doing applies to
   the fix, not only the materialize.
3. **Taste goes in the layout.** "I want all bass under Synth" is not an
   annotation — the recipe's layout template is where that belongs, and it
   already is. The tool offers no facet the annotations schema cannot
   express as a fact.

Granularity: folder is the default; *files matching a word* generates a
glob (`path = "WAV/Textures/Chop *.wav"`); a single file is allowed and
discouraged. All three are `[[dir]]` entries — the schema already takes
globs.

*Shipped 2026-09-01 (v0.9.18):* the **Plan** step (tab 3). Queues
(`GET /api/plan/queues`) group the artifact's failures by source folder
with kind, count, the facets that did resolve and their why; the tree
(`GET /api/plan/tree?prefix=`) walks the destination one level at a
time; a folder's files (`GET /api/plan/folder`) audition through
`/api/preview`. One form for every kind — facet (category / instrument /
word-means / skip), value from the lexicon only, pin or default, the path
it covers (editable to a glob), a note, a local-only flag — and
`POST /api/correct` previews it as an overlay in memory
(`annotations.Overlay` + `harvest.ExplainPrefix`, the partial re-harvest)
returning the blast radius (covered / changed / filled / moved, before →
after groups with examples) before `apply` writes the entry into
`annotations.local/`, logs it, and patches the meta cache for the covered
files. `POST /api/ack` is *leave it*; `POST /api/report` is *this is the
parser*; `GET /api/local` lists the layer and `GET /api/local/export`
zips it minus local-only entries and acks. Measured on the 167k probe:
queues 30 ms, a 291-file preview 0.2 s, apply 1.6 s, the re-plan after
it 3.8 s (a full rebuild — re-placing only the covered entries is the
remaining §19.4 optimization).

### 19.4 Plan as an artifact

The plan used to be rebuilt from scratch on every preflight — once per
rule for the per-rule counts and once more for the set, each build
reloading the library — and `/api/preflight` threw its entries away
because the UI wanted a verdict. §19.2 needs the entries, and needs them
without a rebuild per click. So the plan is a cached artifact keyed by
(recipe as it is now, rules toggled off, a stamp of every file the plan
reads: catalogs, meta cache, both annotation layers), built as a run,
read by Plan / queues / tree / materialize alike.

*Shipped 2026-09-01 (v0.9.17):* `POST /api/plan {view, disabled}`
answers from the artifact, reports the build in progress (stage, count,
total: loading → selecting → placing → cuts → checks), or starts one;
`/api/preflight` is gone. One build per ask: every entry carries the
rule that picked it (`Entry.Rule`), so per-rule counts are attribution,
and a toggled-off rule reports its matches less excludes. `plan.Inputs`
holds catalogs, meta and annotation layers across builds (`plan.BuildWith`
+ `Options{Inputs, Progress}`); materialize and migrate start from the
artifact when it is current. On the 167k-file probe: cold 4 s with
progress, cached instant, a rule toggle 2 s — where the old preflight was
five silent builds. Still to come: a correction invalidates only the
files under its path — harvest is per-path and pure (`harvester.one`),
so a partial re-harvest of one prefix followed by a re-place of those
entries is the whole cost, seconds rather than the full pass.

### 19.5 The local annotation layer — cascade, diff, and submission

§12 already lists a local layer "at the bottom of the cascade, always
winning, taste lives here". Jonathan's refinement (2026-09-01): the local
layer is *also the diff* — the format in which a user hands corrections
back to the repo. That changes what it holds: facts by default, taste by
exception, and never a private format.

**Shape.** `<workspace>/annotations.local/` is a partial annotations tree
in exactly the repo's layout — `vendors/<slug>/vendor.toml`,
`vendors/<slug>/packs/<pack>.toml` — containing only what the user
asserted. `annotations.Load` takes N roots (repo checkout, then local) and
merges by slug: packs union; `[[dir]]` and `[[instrument]]` entries
append. No new precedence rule is needed: `[[dir]]` is already the first
category/instrument tier and deepest-match, and pack `[[instrument]]`
blocks are already consulted first, so a local entry at the same or deeper
path simply wins. A vendor or pack with no upstream annotation (his own
loose drums source; Emulator before its stub) gets a minimal file created
locally — slug and dir come from the catalog.

**Two schema additions the corrections need**, proposed for
`sample-vendor-annotations` SCHEMA.md:

- `default_category` / `default_instrument` on `[[dir]]` — *speaks last*:
  used only when no word on the path said anything. Today's `category` /
  `instrument` on `[[dir]]` are **pins** (first tier, beat the filenames),
  which is right for *Sub-Urban*-style breaks and wrong for a synth pack
  whose Leads folder holds a labelled kick loop. Kind B above is mostly
  defaults, and a tool that can only pin would force mixed folders.
- `observed = <date>` and `note = "…"` on `[[dir]]` and `[[instrument]]`
  entries — provenance, same as `[vendor] observed`. A user's assertion
  from their own copy *is* the repo's "verified against a real copy" bar.

**Two markers that are local-only:**

- `local = true` on an entry — "keep this out of the export" (the
  user's weirdo opinion). Lint upstream rejects it, so it can't leak.
- The **ack list** (`annotations.local/acks.jsonl`): folders the user
  reviewed and left as-is (kind C). Not annotations — nothing to submit.

**Evidence rides with the diff.** `annotations.local/corrections.jsonl`
logs each correction: the path/glob, what the app had resolved and *via
which tier* (the §19.2 why), what the user asserted, the note, app
version, annotations SHA, timestamp. Reports (kind D, "your parser is
wrong") are log entries with no TOML. This is what lets the annotator
triage a submission into annotation-gap vs lexicon-bug vs parser-bug
without re-deriving it.

**Submission.** v1: *export corrections* writes a zip of
`annotations.local/` (minus `local = true` entries and acks) + the log;
the user drops it in the channel; the annotator lands it. v2: open a PR
from the app through the GitHub API the sync already speaks (§11.1) —
gated on wanting a token in the app at all.

**Reconciliation.** After a sync, the app checks each local entry: if
removing it changes no file's placement (upstream now says the same), it
offers to drop it. Without this the local layer becomes a permanent
shadow of the repo and the cascade rots into two sources of truth.

*Shipped 2026-09-01 (v0.9.16):* `annotations.Load(roots…)` merges layers
in order, local last-and-first — `[[dir]]`, `[[instrument]]`, `[[category]]`
entries prepended, packs unioned, a vendor dir with only `packs/` allowed;
every caller reads `ws.AnnotationRoots()` = checkout then
`<workspace>/annotations.local/`. `default_category` / `default_instrument`
are the last tier per facet (after the multisample shape; Source tier
`dir-default`); `observed` / `note` / `local` are parsed and carried.
Schema + lint L7 landed in sample-vendor-annotations the same day.
Writing the layer is v0.9.18 (§19.3). *Reconciliation shipped 2026-09-01
(v0.9.19):* `GET /api/local/reconcile` judges every local entry by
taking it away in memory (`annotations.Overlay` of the checkout with the
layer minus that entry) and re-harvesting the files it covers — nothing
moves means redundant; `POST /api/local/drop` removes the ones the user
lets go and logs the drop. The Plan step offers it under the layer's
listing; a sync that changes the checkout reports how many entries it
made redundant.

### 19.6 Order of work (when Jonathan says go)

1. ~~Per-facet provenance in `harvest.Meta` + a `why` endpoint~~ —
   shipped 2026-09-01 as `catalog why` / `/api/why` (v0.9.15).
2. ~~N-root `annotations.Load` + `default_*` + `observed`/`note` in the
   schema~~ — shipped 2026-09-01 (v0.9.16; annotations schema + lint L7).
   The cascade exists before any UI writes to it.
3. ~~Plan as a run + cached artifact (§19.4)~~ — shipped 2026-09-01
   (v0.9.17); the partial re-harvest on correction rides with step 4.
4. ~~Queues + tree + why panel on the Plan step, writing `[[dir]]` and pack
   `[[instrument]]` entries to `annotations.local/` with blast-radius
   preview, ack list, corrections log, export zip~~ — shipped 2026-09-01
   (v0.9.18, `internal/correct`).
5. ~~Reconciliation after sync~~ — shipped 2026-09-01 (v0.9.19).
6. ~~The Library → Recipe → Plan → Materialize re-cut of the UI (§19.1)~~
   — shipped 2026-09-01 (v0.9.20).
