# DxMed — offline CDR mediation platform

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](go.mod)
[![Next.js](https://img.shields.io/badge/Next.js-16-black?logo=next.js)](ui/package.json)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-336791?logo=postgresql)](internal/store/migrations)
[![Coverage](https://img.shields.io/badge/coverage-Go%2097%25%20%7C%20UI%2099%25-brightgreen)](#tests)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

DxMed collects CDR files from network elements (MSC/MSS, SGSN/PGW/SMF, …), decodes them, validates and
filters every record, merges partial records, removes duplicates and writes the results to output
directories for billing, roaming and other downstream systems.

- **Exactly once.** A file's outputs, statistics, dedupe keys and sequences are committed in one database
  transaction; downstream systems never see a half-written file.
- **Every file balances.** `in = out + err + drop + held + dup`, shown per file and per day.
- **Per-record error isolation.** A bad record goes to the error route with a reason code; the file carries on.
- **Versioned configuration.** Every change is a new version; publishing creates an immutable release,
  gated by Template Lab regression cases. Rollback is activating an older release.
- **Scale out.** Run more engines on the same database; files are leased with `SKIP LOCKED` and one engine
  at a time is the scheduler.

![Dashboard](docs/img/ui-dashboard.png)

## Architecture

```mermaid
flowchart LR
  subgraph NE["Network elements"]
    NE1[MSC / MSS]
    NE2[SGSN / PGW / SMF]
  end
  NE1 --> IN[(Source<br/>directories)]
  NE2 --> IN
  IN --> COL[med-collector<br/>completion check,<br/>SHA-256, register]
  COL -->|file NEW| PG[(PostgreSQL 18<br/>schema med)]
  subgraph ENG[med-engine]
    W[Workers x N<br/>lease, decode, filter,<br/>merge, route, commit]
    S[Scheduler<br/>one leader]
    PA[Post-actions]
  end
  PG <-->|lease / commit| W
  PG <--> S
  PG <--> PA
  W --> OUT[(Output<br/>directories)]
  PA --> OUT
  API[med-api<br/>REST + SSE, RBAC] <--> PG
  UI[med-ui<br/>Next.js + BFF] <-->|HTTPS, bearer token| API
  BR[Browser] <-->|HTTPS, HttpOnly cookie| UI
  CTL[medctl] --> PG
  W -.->|metrics| PROM[/Prometheus/]
  COL -.->|metrics| PROM
```

| Component | Language | Role |
|---|---|---|
| `med-collector` | Go | Watches source directories, waits until a file is complete, registers it |
| `med-engine` | Go | Leases files and runs the pipeline; scheduler, post-actions and crash recovery |
| `med-api` | Go | REST API: configuration, releases, Template Lab, operations, reports, logs, users, audit |
| `med-ui` | Next.js 16 / React 19 | Web console; its server is a backend-for-frontend holding the session |
| `medctl` | Go | CLI: migrate, bootstrap, seed, publish, user, gen (sample files), partitions, status |

## Processing pipeline

```mermaid
flowchart LR
  A[File NEW] --> L[Lease]
  L --> FD{Duplicate<br/>file?}
  FD -->|yes| DUP[DUPLICATE]
  FD -->|no| DEC[Decode<br/>template]
  DEC --> VAL[Validate<br/>dictionary]
  VAL --> PRE[Rules PRE<br/>+ transforms]
  DEC -->|bad record| ERR[Error route]
  VAL -->|invalid| ERR
  PRE -->|ERROR| ERR
  PRE -->|DROP| DROP[Dropped]
  PRE -->|HOLD| MRG[Sort & merge<br/>partials]
  MRG -->|complete| COLR[Rules COL]
  MRG -->|timeout| UNC[Rules UNCOL]
  PRE -->|route| RD[Record<br/>dedupe]
  COLR --> RD
  UNC --> RD
  RD -->|duplicate| DR[Duplicate route]
  RD --> RT[Route &<br/>encode]
  RT --> CM[Commit]
  CM --> P[POSTED]
```

Decoders cover ASN.1 BER templates (MSC, PGW), binary blocks, CSV, fixed-width, tagged, JSON and XML.
Writers produce CSV, fixed-width, lines, JSON lines, XML and BER. Rules have two passes (a general block, then the block for the record's call
type), and each rule can apply a transform group and lookups (`set`, `map`, longest-`prefix`).

### Exactly-once commit

```mermaid
sequenceDiagram
  participant W as Worker
  participant FS as Output directory
  participant DB as PostgreSQL
  W->>DB: lease file (SKIP LOCKED), heartbeat every TTL/3
  W->>FS: write outputs as .part files in .tmp
  W->>DB: BEGIN
  W->>DB: allocate route sequences, insert outputs, stats, dedupe keys, partials
  W->>DB: file COMMITTED, COMMIT
  W->>FS: rename .part to final name
  W->>DB: mark outputs visible
  W->>DB: post-actions done, file POSTED
```

If the engine crashes before `COMMIT`, the lease expires and the file is processed again from scratch; the
`.part` files are discarded. After `COMMIT`, recovery finishes the renames, so nothing is duplicated or lost.

### File lifecycle

```mermaid
stateDiagram-v2
  [*] --> NEW : collected
  NEW --> LEASED : worker lease
  LEASED --> COMMITTED : commit
  LEASED --> FAILED : error
  LEASED --> DUPLICATE : file duplicate
  LEASED --> NEW : lease expired
  LEASED --> DEAD : last attempt failed
  FAILED --> NEW : retry after back-off
  COMMITTED --> POSTED : outputs visible
  NEW --> HELD : hold
  HELD --> NEW : release
  HELD --> CANCELLED : cancel
  DEAD --> NEW : re-drive
  POSTED --> NEW : reprocess
```

## Configuration model

```mermaid
erDiagram
  STREAM ||--|| DECODING_TEMPLATE : decodes_with
  STREAM ||--o| FIELD_DICTIONARY : validates_with
  STREAM ||--|| RULE_SET : filters_with
  STREAM ||--o| MERGE_PROFILE : merges_with
  STREAM ||--|{ ROUTE : outputs_to
  STREAM }o--o{ LOOKUP_TABLE : uses
  RULE_SET ||--|{ RULE_BLOCK : "per stage and call type"
  RULE_BLOCK ||--|{ CONDITION : "OR, ordered"
  CONDITION }o--o| TRANSFORM_GROUP : applies
  ROUTE ||--|| OUTPUT_TEMPLATE : formats_with
  STREAM ||--o{ RELEASE : "published as"
```

### Publishing a release

```mermaid
sequenceDiagram
  actor P as Publisher
  participant API as med-api
  participant LAB as Template Lab
  participant DB as PostgreSQL
  participant E as med-engine
  P->>API: publish stream
  API->>DB: load current version of every referenced object
  API->>API: validate references and schemas
  API->>LAB: run saved Lab cases against the new bundle
  alt problems or failing cases
    API-->>P: 422 with details
  else all good
    API->>DB: insert release (bundle, SHA-256), activate, audit
    API-->>P: 200 release
    E->>DB: next lease reads active release
    E->>E: compile once, use from the next file
  end
```

## Console

| | |
|---|---|
| ![Files](docs/img/ui-files.png) | ![File detail](docs/img/ui-file-detail.png) |
| Input files with reconciliation counters | File detail: history, per-stage statistics, outputs |
| ![Streams](docs/img/ui-streams.png) | ![Config editor](docs/img/ui-config-editor.png) |
| Streams and releases | Configuration editor with version history |
| ![Template Lab](docs/img/ui-lab.png) | ![Reports](docs/img/ui-reports.png) |
| Template Lab | Daily volume and reconciliation |

Roles: **viewer**, **operator** (reprocess, hold, release, cancel, re-drive), **configurator** (edit, Lab),
**publisher** (publish, activate, audit) and **admin** (users, settings).

## Documentation

| Document | Markdown | Word | PDF |
|---|---|---|---|
| High-level design | [md](docs/DxMed_HLD.md) | [docx](docs/DxMed_HLD.docx) | [pdf](docs/DxMed_HLD.pdf) |
| Low-level design | [md](docs/DxMed_LLD.md) | [docx](docs/DxMed_LLD.docx) | [pdf](docs/DxMed_LLD.pdf) |
| Operations guide | [md](docs/DxMed_Operations_Guide.md) | [docx](docs/DxMed_Operations_Guide.docx) | [pdf](docs/DxMed_Operations_Guide.pdf) |

The Word and PDF files are generated from the Markdown by `scripts/build-docs.sh` (pandoc, mermaid-cli and
headless Chrome).

## Quick start

You need Go 1.26 or later (`go.mod` pins toolchain 1.27.2, which Go downloads automatically), Node.js 22 and
a PostgreSQL 18 cluster. The scripts expect the DxPS cluster helpers at `$DXPS_SCRIPTS` (default
`~/DxPS/dxps/scripts`). Runtime files go to `~/med-runtime`.

```bash
scripts/infra-setup.sh   # roles, schema, secrets, dev certificates, migrations, admin user, sample config
scripts/run-all.sh       # build and start all components
scripts/status.sh        # pid, port and health of each component
```

Open `https://localhost:8090` and sign in as `admin`. The password is `MED_ADMIN_PASSWORD` in
`~/med-runtime/secrets.env`. Generate sample input with
`~/med-runtime/bin/medctl gen msc --dir ~/med-runtime/in/msc --files 3 --records 500`.
See the [operations guide](docs/DxMed_Operations_Guide.md) for daily operations and troubleshooting.

| Listener | Address |
|---|---|
| Console | `https://localhost:8090` |
| API | `https://localhost:8543` (`/healthz`, `/readyz`) |
| Metrics | `127.0.0.1:9301` (engine), `127.0.0.1:9302` (collector) |

## Repository layout

| Path | Content |
|---|---|
| `cmd/` | The four Go binaries |
| `internal/` | Engine packages (decode, transform, filter, merge, dedupe, route, encode, pipeline, …), store, API, auth |
| `ui/` | Next.js console and its vitest tests |
| `scripts/` | Setup, run, stop, status, tests, security scan, document build |
| `tools/covergate` | Coverage gate used by the test scripts |
| `docs/` | HLD, LLD, operations guide, screenshots |

## Tests

```bash
scripts/test-all.sh      # Go unit + integration tests, UI tests, coverage gates
scripts/sec-scan.sh      # govulncheck, go vet, npm audit, secret scan of tracked files
```

Coverage gates: 100% for the engine packages, at least 95% for Go overall (currently 97.3%), and at least 90%
for the UI (currently 99.8% of statements).

## License

[Apache License 2.0](LICENSE).
