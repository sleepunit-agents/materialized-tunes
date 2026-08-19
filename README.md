# materialized-tunes

Sample libraries for hardware samplers, treated as materialized views over an
immutable, SHA-fingerprinted source library. See [SPEC.md](SPEC.md) for the
full design (current state) and [CHANGELOG.md](CHANGELOG.md) for the dated
history; the short version:

- **Sources are immutable** — Splice, Samples From Mars, etc., cataloged and
  hashed once, never modified.
- **Libraries are views** — a device profile (format constraints), a storage
  profile (capacity), and a selection recipe materialize into exactly the
  files a sampler wants, verified to fit *before* anything is copied.
- **Lockfiles make history restorable** — every materialization pins exact
  source hashes and transform parameters. Wipe the card, change your mind
  years later, get the old library back byte-for-byte.

## Status

The full v0 loop works: init → locations → scan → plan → materialize →
verify → diff → restore. Materialization transcodes with ffmpeg
(deterministic args, recorded in the lockfile), pulls remote sources into a
content-addressed cache (local sources are used in place, always
hash-verified), writes a lockfile per run, and stamps card-mode targets
with `.mtunes-card.json` so a card can identify itself later. `verify`
checks a card hash-by-hash against its lock; `diff` is the staleness
report; `restore` replays any old lock byte-for-byte.

```
mtunes init ~/mtunes-library --git
export MTUNES_WORKSPACE=~/mtunes-library
mtunes location add splice --root ~/Splice
mtunes location add workstation --type ssh --host workstation --root /tank/samples
mtunes scan
mtunes catalog status
# edit ~/mtunes-library/views/<name>.toml (see views/EXAMPLE.toml.example)
mtunes plan <name>
mtunes materialize <name> --to /Volumes/CARD     # or a staging folder for Transfer
mtunes verify --card /Volumes/CARD
mtunes diff <name>                                # staleness vs newest lock
mtunes restore <name> --to /Volumes/CARD          # newest lock; or pass a lock path
mtunes catalog packs                              # pack-first browse (also --json, --device)
mtunes ui                                         # the same, in a browser at 127.0.0.1:7315
```

The UI (`mtunes ui`, or the native `mtunes-desktop` shell) browses the
catalog pack-first with a per-device lens, authors recipes/devices/storage,
runs materializations, and shows cards and locks. Pack art and prose come
from vendor annotations when a `<workspace>/annotations` checkout exists,
and otherwise from what the packs themselves ship (`Docs/Artwork*`,
`Docs/*About*` — SFM's convention, applied to the whole house archive).

## Building

Requires Go and ffmpeg (used by `materialize`/`restore`; must be on PATH).
Runs on macOS, Linux, and Windows.

```
go build ./cmd/mtunes
go test ./...

# native desktop shell (Wails v2; needs WebView2 on Windows, present on 10/11)
go build -tags desktop,production -o mtunes-desktop ./cmd/mtunes-desktop
#   macOS: CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production ...
#   Windows: go build -tags desktop,production -ldflags "-H windowsgui" -o mtunes-desktop.exe ./cmd/mtunes-desktop
```

Windows notes: `winget install Gyan.FFmpeg` gets ffmpeg; set the workspace with
`$env:MTUNES_WORKSPACE = "$HOME\mtunes-library"` (or at user level in
System → Environment Variables so the desktop app sees it); the built-in
OpenSSH client is enough for `--type ssh` locations (no connection
multiplexing there, so remote scans are slower than on macOS/Linux). Card
paths are just drive letters (`--to E:\`). A multi-vendor archive laid out
`<Vendor>\<Pack>` is added with `mtunes location add archives --root E:\Sample-Archives --layout vendor-dirs`.
