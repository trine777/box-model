# Box 命名规范 (Naming Standard)

> SoR for box / item naming. Goal: kill **模糊语义** and **孤岛 box**.
>
> **Naming is symbolic, not string-prefix (R13).** Data-warehouse layering
> (ods/dwd/dws/ads, `<sphere>-<name>`) is just a *模子*. Its premise — a
> table has only ONE name dimension, so you cram layer/domain/grain into a
> string prefix — does not hold here. box-model is a **符径 (Symbol Path)**
> with **7 symbol dimensions** (kind/status/relation/scope/topic/priority/
> domain). Naming projects meaning onto those symbols; it does NOT compress
> meaning back into a string. The DW string prefix is a *downgrade* forced
> by SQL's single name slot — box should *upgrade* to symbols.

## 1. The one hard rule: every box carries a SymScope symbol

A box's sphere is a **symbol**, `{kind: scope, value: <sphere>}`, on
`Box.Symbols` (R13). A box with no scope symbol is an **island** —
`box_globes` buckets it under `(none)`.

Why a symbol, not a label: the old `__op:sphere` **label** was a free
string — no `ValidateSymbol` enforcement, no `box_legend` description, not
reachable by symbol queries, not in the symbol graph. Using a *fuzzy*
free-string to impose an *anti-fuzz* naming rule is self-contradictory. A
SymScope symbol gets the controlled-vocab check, legend, and graph
membership the label never had.

Set it at creation (`box_create_box {symbols:[{kind:scope,value:dev}]}`) or
via `box_set_box_symbols`. `BoxScopeOf` reads the scope symbol, falling back
to the legacy `__op:sphere` label only during migration.

`boxlint` (see §5) flags islands AND label-only (un-migrated) boxes.

## 2. Sphere — the controlled vocabulary (类数仓 domain)

A small, closed set. Pick one. Do not invent ad-hoc spheres without adding
them here first.

| sphere | meaning | examples |
| --- | --- | --- |
| `dev` | box-model's own development & dogfood | dev-progress, box-model-dev |
| `km` | knowledge indexes (nails, references) | nail-index, nailforge |
| `content` | produced content (articles, SEO, drafts) | content-seo |
| `media` | binary/file assets via blob layer | media-store |
| `lab` | 造词 / design-language / experiments | fengyin, design-language |
| `ops` | operations: metrics, case logs, runbooks | metrics, ops-cases |
| `tmp` | throwaway / test / dogfood scratch | (auto-expire candidates) |

## 3. Box key format: `<sphere>-<name>`

`[a-z0-9-]+`, kebab-case, **sphere as the leading segment** so the key itself
declares belonging. The key SHOULD match the sphere label.

Good: `dev-progress` · `km-nail-index` · `content-seo` · `ops-metrics`
Bad:
- `mac-storage` — a **machine name** leaks into the key; it's media → `media-store`
- `nail-index-real` / `nail-index-v2` — version suffix scattered across keys;
  use ONE key `km-nail-index` + the built-in revision chain for versions
- `demo-route2` / `box-case` — opaque; say what it is (`tmp-route-demo`, `ops-cases`)

## 4. Item naming (inside a box)

Items are keyed by server id; humans navigate by **symbols + labels**, so:

- **kind symbol is mandatory** (already enforced): one of
  D/R/Q/H/T/M/F/O/A/X. Use the right one (Decision vs Memo vs Observation).
- **topic symbol** for retrieval: `{kind:topic, value:<kebab>}`. Keep topics
  consistent across a box (a small per-box topic vocabulary beats free text).
- `idem_key` SHOULD be meaningful + stable (`upload::report.pdf`,
  `release::r12`) so re-runs are idempotent and the key reads as English.
- `storage_uri` scheme picked by where bytes live: `row://` (inline),
  `blob://sha256/…` (file), `repo://`/`s3://`/… (pointer). Never `inline://`.

## 5. boxlint — the guard

`scripts/boxlint` scans every box and reports violations:

| check | violation |
| --- | --- |
| sphere present | `__op:sphere` missing → **island** |
| sphere in vocab | `__op:sphere` value not in §2 set |
| key shape | key not `[a-z0-9-]+` or no sphere-ish prefix |
| owner present | `owner_id` empty |

Run `boxlint` after creating boxes. It's advisory (Box stays dumb storage,
invariant #10 — naming is a governance layer, not enforced server-side), but
keeps drift visible. CI / a periodic timer can run it.

## 6. Migration of existing boxes (R12)

Keys are NOT force-renamed (breaking: item.box_id, storage_uri, client
config). Instead every existing box gets a **sphere label** (cheap,
non-breaking) so islands disappear immediately. Key renames are
opt-in/new-box-only.

| current key | sphere assigned | future key (new boxes only) |
| --- | --- | --- |
| dev-progress | `dev` | dev-progress ✓ |
| box-model-dev | `dev` | dev-model |
| nail-index-real | `km` | km-nail-index |
| nail-index-v2 | `km` | km-nail-index-v2 |
| nailforge | `km` | km-nailforge |
| content-seo | `content` | content-seo ✓ |
| mac-storage | `media` | media-store |
| fengyin | `lab` | lab-fengyin |
| design-language | `lab` | lab-design-language |
| box-case | `ops` | ops-cases |
| metrics | `ops` | ops-metrics |
| demo-route2 | `tmp` | (delete candidate) |

After migration `boxls` (no args) shows a real sphere per box, not `(none)`.
