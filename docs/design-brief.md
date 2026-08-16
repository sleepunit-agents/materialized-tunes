# Design brief: mtunes — sample libraries as materialized views

## What this is

A desktop app (Wails: Go backend, web frontend) for musicians who own
hardware samplers. Their sample library is huge (100k+ files, hundreds of
gigabytes, multiple vendors) and immutable; each hardware device (Elektron
Octatrack, Syntakt, …) wants a small, format-constrained subset on a card
or in device memory. The app's model: the library is the database, a
device's card is a **materialized view** over it. You browse the library
through a device's constraints, compose a selection recipe, pre-flight it
against the card's real capacity, then materialize — with every export
pinned by a lockfile that can rebuild the card byte-for-byte years later.

The CLI already works; this is its first GUI. All data shown below already
exists as JSON.

## Who it's for

One musician at their desk, planning what goes on a card before a session
or a trip. Expert user, owns the vocabulary (packs, one-shots, loops,
16-bit/44.1kHz). Values density and honesty over hand-holding. Think
"pro audio tool," not "music store": closer to a database client or
Lightroom's library module than to Splice's marketplace.

## Core screens

### 1. Pack browser (the home screen)
- Packs are the browsing unit — cards or rows showing: pack name, vendor,
  cover image + description + product link when known (from a community
  annotation layer; many packs have og-style metadata, some don't).
- **Identity badges**: "complete" / "74% of pack" — the tool recognizes
  which packs you own by content hash. Unannotated sources fall back to
  honest folder names with no badge, and the UI must not make the
  fallback feel broken.
- **The device lens is the defining interaction**: pick a device
  (Octatrack, Syntakt, …) and every pack re-renders to show only what
  that device can actually load — eligible file counts and *converted*
  sizes (e.g. "4,050 of 4,218 files, 1.3 GiB after transcode"). The lens
  is a filter you toggle, never a separate mode or page. Design the
  lens-on state to feel like putting on glasses, not navigating away.
- Facets beyond device: vendor, category (one-shots / loops / kits /
  multisamples — from the annotation layer), location (which disk/server
  the source lives on).

### 2. Recipe editor (a view = device + storage + selection)
- A recipe is a small set of include/exclude rules over the library. In
  the CLI it's TOML; in the UI it should feel like building a playlist
  of packs/folders with a live running total.
- **The killer element is the live pre-flight**: a fit meter showing
  post-transform size against the card's *usable* capacity (capacity −
  reserve), updating as the selection changes. Overflows, filename
  violations, per-folder count limits, and name collisions surface here,
  before anything is written. Warnings (long names) vs errors (won't
  fit, illegal characters) must read differently.
- Selection granularity: whole pack, or subtree within a pack (e.g. just
  "WAV/Acid Loops"). Show what a rule currently matches.

### 3. Materialize / run screen
- Progress over tens of thousands of files (pull → transcode → write),
  with the resilience states as first-class UI: retries happening,
  resumed files skipped instantly, individual failures skipped-and-listed
  (run continues), systemic abort. A run that ends "83,875 of 83,889
  written, 14 skipped — here they are" is a *successful* run and should
  look like one, with the gap made actionable, not shameful.

### 4. Cards & history
- Each materialization writes a lockfile; a card knows which lock it came
  from. This screen: cards this workspace knows, drift between a card /
  its lock / the current recipe ("stale: 214 files would change"), and
  restore — rebuild any historical lock onto a card, byte-for-byte.
- History should read like git log for your sample cards, not like a
  backup tool's guilt-trip screen.

## Data available to every screen (already-shipping JSON)
- Pack summaries: name, vendor, dir, file count, bytes, url, image,
  description, identity match kind + fraction, per-device eligible count
  and converted bytes.
- Plan results: exact fit math (bytes, cluster overhead, reserve),
  warnings, errors, per-file skip reasons.
- Diff results: added/removed/changed vs any lock.
- Device profiles: audio constraints, naming rules, delivery mode.

## Tone & constraints
- Dark-first, information-dense, keyboard-friendly. Generous with
  numbers; they are the product. Tabular data should look confident, not
  apologetic.
- Pack imagery is decoration, not structure: the UI must be fully usable
  when no artwork exists (most of a real library).
- Desktop window, resizable, min ~1100px wide. No mobile.
- No audio playback in v1 (preview is future work — leave a affordance,
  don't design around it).
- Avoid: marketplace patterns (carts, stores, promos), DAW patterns
  (timelines, transport bars), wizard flows. The user composes and
  verifies; the tool is a truthful lens and a careful pair of hands.
