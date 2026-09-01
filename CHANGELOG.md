# Changelog

The dated history of materialized-tunes. SPEC.md describes the tool as it
is *now*; this file is where the "decided 2026-07-17" / "shipped
2026-08-16" trail lives, with the physical observations that drove each
change, because *why* a constraint exists is as durable as the constraint.
Newest first. Versions are milestones, not releases — there is one binary
and it is whatever `main` builds.

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
