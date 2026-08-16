# materialized-tunes — v0 design spec

Sample libraries for hardware samplers, treated as **materialized views over an
immutable source library**. Sources are cataloged and SHA-fingerprinted once;
device-specific libraries are rendered from declarative recipes, verified
against storage constraints *before* any copying, and pinned by lockfiles so
any past library is restorable years later.

Status: draft for markup. Anything tagged `[confirm]` is a value or decision
that needs a real answer before implementation.

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

v0 source formats: WAV, AIFF, FLAC. Anything else is cataloged (path + SHA)
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
pack-first browser (§11) — vendor grammar for the house archive is simply
"art and about live in `Docs\` at the pack root". Zero-G's Jungle Warfare is
the degenerate case: discontinued, so the About is a research note, not a
copy of a product page.

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

[[include]]
location = "workstation"
glob     = "samples-from-mars/808 From Mars/**"
as       = "808"                  # optional output prefix; default = glob root

[[include]]
location = "splice"
glob     = "packs/*breaks*/**"

[[exclude]]
glob = "**/Ableton*/**"           # vendor parallel-format trees, DAW project
[[exclude]]                       # files, etc. — v0 vendor intelligence is you
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
  fall through to the collision error. Templating layouts remain post-v0.
- Excludes apply across all includes.
- **Auto vendor grouping** (idea, 2026-07-18): a layout option that prefixes
  output with a vendor dir *conditionally* — vendors contributing multiple
  packs group ("SFM/808 From Mars/…"), single-pack vendors stay flat
  ("Jungle Jungle/…"). Wants the vendor-annotations pack layer (pack
  identity answers "how many packs does this vendor have in this view");
  until then, per-include `as` does it explicitly, which is how ot-florida
  ships. Display names for locations (config `label`?) are part of this.

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
                                          #   DROP (sources gone), or CHANGE
```

`diff` is the staleness surface: re-materializing an old view is always an
explicit choice between `restore` (exactly as it was) and `materialize`
(recipe against today's catalog), with the delta visible first.

## 9. CLI surface (complete v0 list)

```
mtunes init <dir>                 # scaffold a workspace (offers git init)
mtunes location add|list|remove
mtunes scan [<location>]          # build/refresh catalog (remote-hashes ssh locations)
mtunes catalog status [--json]    # per-location counts, sizes, last-scan
mtunes catalog ls [--device D] [--ineligible] [--location L] [--glob G] [--json]
                                  # --device = the device lens: only what can ride
mtunes plan <view> [--json]
mtunes materialize <view> [--to <path>] [--force]   # --to defaults to view target
mtunes catalog packs [--device D] [--location L] [--json]
#   pack-first browsing (v0.3): tier 1 (location's `vendor` slug → workspace
#   annotations/ checkout: names, urls, og meta, identity match exact/partial)
#   and tier 3 (top-level dirs, honest fallback) are live; tier 2 heuristic
#   inference for unknown vendors remains designed-for.
#   Enrichment split (decided 2026-07-26): the annotations repo distributes
#   FACTS AND POINTERS only (names, slugs, urls, image urls, hashes, counts);
#   vendor prose (og:description) and image bytes are fetched by mtunes from
#   the pack's url on demand and cached in the workspace (annotations-cache/,
#   never committed, never redistributed). UI reads the cache; the repo
#   stays legally boring. Fetch-on-demand is designed-for, not yet built.
mtunes restore <lock> --to <path>
mtunes verify --card <path>
mtunes diff <lock> [--json]
mtunes cache status|clear
```

`--json` everywhere is the machine interface: the future GUI (and any
script) consumes the same structs the human reports render, so CLI and
GUI can never disagree about what a plan says.

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

## 11. Explicitly out of scope for v0 (but designed-for)

- **Sidecar sync** (Octatrack `.ot` etc.): mutable, device-written files,
  keyed to **output artifact identity** (source SHA + transform params) —
  not to the view — so slices follow the sample across views. The lockfile
  already records everything needed to key them.
- Vendor-aware format-tree selection (auto-pick WAV vs Ableton-rack folders
  per device). v0 answer: recipe excludes.
- Direct device upload for staged devices (skipping the Transfer drag-and-
  drop) — e.g. via SysEx/SDS the way elektroid does it. v0 answer: hand the
  staging folder to Transfer.
- GUI (long-term likely; the Go core should stay cleanly separable from the
  CLI for a future Wails frontend).
  - **Pack-first browsing**: packs are the mental unit, so the browser
    groups by pack — a derived presentation layer over catalog paths, no
    storage change. Three tiers: (1) known vendor → pack grammar in the
    vendor profile ("SFM: top-level dirs are packs, WAV/ is canonical,
    sibling zip is the archival original"); (2) unknown vendor → heuristic
    inference (audio critical mass, docs/artwork at dir root, sibling-zip
    pattern, shallow depth), cached as correctable annotations; (3) no
    idea → top-level dirs pose as packs, honest degraded mode. Content-SHA
    pack identity makes declared/inferred packs recognizable across users.
    API: `catalog packs [--device D] --json` — pack summaries with
    per-device eligible counts and converted sizes, computed from the
    catalog alone.
  - **Device lens**: when building a collection for a device, filter the
    entire browse view to only what CAN materialize for it — too long,
    untranscodable, wrong-shaped stuff just disappears. This is the plan
    engine's eligibility predicates run as a live filter; everything needed
    (duration/channels/rate/format) is already in the catalog, so it costs
    nothing to evaluate. A CLI precursor (`catalog ls --device syntakt`)
    would be nearly free to add.
- Tagging, preview/audition, audio-content dedup.
- **Source annotations**: a metadata cascade over the (still immutable)
  sources, most-specific wins:
  `device default → vendor profile → vendor group/era → pack override →
  local annotation`. Split by who could have written each layer: vendor
  facts (Splice layout rules; SFM era groups like "tape-era packs are
  dual-mono"; which parallel format tree to prefer) are shareable data
  about the product itself; local annotations are the user's taste and
  exceptions, and always win. The resolved per-file value is pinned in the
  lockfile like any transform param, so shared data evolving never changes
  a restore.
  - **Pack identity by content, not path**: packs are recognizable from
    their file SHAs (already cataloged) — a vendor entry can match "these
    hashes = 808 From Mars" regardless of folder names. Path globs are the
    fallback for unfingerprinted packs.
  - **Community vendor DB** is viable long-term: annotations are inert
    data (no code execution), so a public contributions repo is low-risk.
    House rules: schema-versioned files; facts only ("this pack's stereo
    is dual-mono"), never taste ("sum sounds better") — taste is local.
- **Display-aware naming** (found on hardware 2026-07-17: Syntakt's list
  view crops names, so `BD A 808 Decay A 01..06` all display identically —
  the distinguishing digits are past the crop). Escalating ideas, none
  built yet:
  1. `display_length` heuristic per device + plan warning when multiple
     output names share their first N chars (cheap, high value).
  2. Opt-in "distinguishing-first" rename policy (move discriminating
     tokens to the front). PREFERRED DIRECTION (2026-07-17), and it slots
     into the vendor annotation layer: a vendor's naming grammar is a fact
     ("SFM: trailing take number → front"), so the rule ships with the
     vendor profile and applies everywhere that vendor appears.
  3. Common-token compression: tokens shared by EVERY name in a flat
     export carry zero information — strip them deterministically,
     maximizing distinguishing info per visible character (git-style
     unique abbreviation, but for sample names).
- **Dual-mono detection**: if L ≈ R, take one channel, skip the −3 dB pad —
  no decision needed and lossless by definition. Checkable at materialize
  time (file is already in cache), verdict cached in the catalog as derived
  metadata. Leaves annotations for genuine taste calls on true-stereo
  material only.

## 12. Open questions for markup

Resolved 2026-07-17:

- Over-length sources are auto-excluded (shown in plan, no trim code in v0).
- Below-rate sources convert silently.
- Workspace is a user-chosen synced/git dir via `mtunes init`, no dotdir
  default.
- Octatrack `[naming]`: no documented hard limits exist; ship conservative
  warn-level heuristics (see §4).
- Storage `reserve` defaults to 10% everywhere — "how full are we willing
  to go" is an explicit policy knob, never an accident.
- **Lockfiles are kept forever.** They are the history; `restore` is the
  `git revert` equivalent. (And if the workspace is a git repo, they're
  literally versioned too.)

Still open, deferred until the Octatrack becomes a target: true usable
bytes of the 32GB CF card (`diskutil info` when mounted).

## 13. Device fact sources

- [Syntakt User Manual OS 1.40](https://www.elektron.se/wp-content/uploads/2026/03/Syntakt-User-Manual_ENG_OS1.40_260304.pdf)
  §6.2.4: 64 slots / 32 MB / 5 s per sample / 16-bit 48 kHz mono wav /
  +Drive, global across projects / Transfer converts and trims >5 s.
- [Octatrack User Manual OS 1.40A](https://www.elektron.se/wp-content/uploads/2024/09/Octatrack-User-Manual_ENG-OS1.40A_220204.pdf)
  audio file compatibility: static = 16-bit 44.1 kHz wav/aiff mono/stereo;
  flex = 16/24-bit 44.1 kHz wav/aiff mono/stereo; audio pool folder max
  1,024 files.

## 14. UI (v0.4)

`mtunes ui` serves the browser UI from the binary (go:embed, localhost
only, no toolchain): Library (pack browser, device lens, identity badges,
artwork from annotation image URLs), Recipe (per-rule match stats, live
pre-flight with fit meter/reserve/issues — rule toggles are previews, the
recipe file is never modified from the UI), Materialize (real runs with
live progress, resumed/skipped surfaced as first-class outcomes), Cards
(lock history per view, staleness via the diff engine, restore as a
copied command). Design imported from the claude.ai/design prototype;
implemented as an embedded server + vanilla JS so Wails can wrap the same
assets later. Deliberate deviations from the prototype: no pull/transcode/
write phase pills (the pipeline is per-file concurrent, not phased — a
single honest progress bar instead), restore copies the CLI command
rather than writing to a target picked in a browser.

## 15. Desktop shell (Wails)

`cmd/mtunes-desktop` wraps the identical embedded UI in a native window:
Wails v2 serves ui.Assets() and falls through to the same /api/* handler
`mtunes ui` uses, so browser and desktop can never drift. Build needs
`-tags desktop,production` and (macOS, Wails 2.13) an explicit
`CGO_LDFLAGS="-framework UniformTypeIdentifiers"`. The CLI `mtunes ui`
remains the toolchain-free path; the desktop shell is presentation only —
no bindings, no IPC, one HTTP contract.

## 16. Authoring in the UI (v0.5)

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
- Recipes (`/api/view`): create, add-rule, remove-rule, set-target. The
  add gesture lives in the Library — a pack card's `+`, or "add to
  recipe" in the detail view, which adds *the folder you're looking at*
  (so "just the acid loops" is two clicks, not a hand-written glob).
- Profiles (`/api/device`, `/api/storage`, `/api/presets`,
  `/api/volumes`): device presets are prefills for known gear and every
  field is editable, because the next box out is one we've never seen;
  storage capacity can be measured from a mounted volume instead of
  looked up.
