import type { Dashboard, FileDetail, LabReport, MedFile, Release, StreamInfo } from "@/lib/types";

export const file: MedFile = {
  id: "f1", stream: "MSC_ERICSSON", source: "/in", name: "MSC01_000001.ber", path: "/in/MSC01_000001.ber", size: 2048, sha256: "ab".repeat(32),
  exchange_id: "MSC01", status: "FAILED", priority: 5, attempts: 2, not_before: "2026-10-16T10:00:00Z", records_in: 10, records_out: 7,
  records_err: 2, records_drop: 1, records_held: 0, records_dup: 0, error_code: "IO", error: "disk", received_at: "2026-10-16T10:00:00Z",
};

export const detail: FileDetail = {
  file,
  events: [{ ts: "2026-10-16T10:00:00Z", status: "NEW", actor: "collector", detail: "" }],
  stats: [{ stage: "MAIN", call_type: "MO", action: "ROUTE", reason: "", count: 7, duration: "120", volume: "0" }],
  outputs: [
    { id: "o1", file_id: "f1", stream: "MSC_ERICSSON", route: "R", name: "R.csv", path: "/out/R.csv", records: 7, bytes: 300, sha256: "x", visible: true, created_at: "2026-10-16T10:00:00Z" },
    { id: "o2", file_id: "f1", stream: "MSC_ERICSSON", route: "L", name: "L.csv", path: "/out/L.csv", records: 0, bytes: 3000000, sha256: "y", visible: false, created_at: "2026-10-16T10:00:00Z" },
  ],
  errors: [
    { day: "2026-10-16", file_id: "f1", stream: "MSC_ERICSSON", seq: 5, offset: 100, stage: "VALIDATE", reason: "BAD_IMSI", detail: { f: "imsi" } },
    { day: "2026-10-16", file_id: "f1", stream: "MSC_ERICSSON", seq: 6, offset: 120, stage: "MAIN", reason: "CAUSE" },
  ],
};

export const dashboard: Dashboard = {
  by_status: { COMMITTED: 3, FAILED: 1 },
  queue: { MSC_ERICSSON: 2 },
  last_24h: { day: "", stream: "", files: 4, in: 100, out: 80, err: 5, drop: 10, held: 0, dup: 5 },
  partials: { PGW_DATA: 6 },
  active_alarms: 1,
  workers: [{ id: "w1", component: "med-engine", host: "h", pid: 1, started: "2026-10-16T09:00:00Z", seen: "2026-10-16T10:00:00Z" }],
  pending_post_actions: 0,
  active_streams: 2,
};

export const rel1: Release = { id: "r1", stream: "MSC_ERICSSON", number: 1, sha256: "abcdef0123456789", note: "first", published_by: "seed", published_at: "2026-10-16T09:00:00Z", active: false };
export const rel2: Release = { ...rel1, id: "r2", number: 2, note: "second", active: true };

export const streams: StreamInfo[] = [
  { name: "MSC_ERICSSON", version: 1, doc: { description: "voice" }, active: rel2, queue: 2, partials: 0 },
  { name: "PGW_DATA", version: 1, doc: {}, queue: 0, partials: 6 },
];

export const report: LabReport = {
  problems: [{ severity: "warning", path: "x", message: "careful" }],
  counts: { in: 10, out: 7, err: 2, drop: 1, held: 0, dup: 0, merged: 0, expired: 0 },
  balanced: true,
  decode: { records: 10, errors: 0, skipped: 0 },
  stats: [],
  samples: [{ seq: 1, offset: 0, length: 10, record_type: "MO", call_type: "MO", fields: { a: "1" }, stage: "MAIN", action: "ROUTE", reason: "", routes: ["R"] },
    { seq: 2, offset: 10, length: 10, record_type: "MT", call_type: "MT", fields: {}, stage: "PRE", action: "DROP", reason: "zero" }],
  rejects: [],
  outputs: [
    { route: "R", records: 7, bytes: 10, binary: false, text: "H|x", truncated: false },
    { route: "B", records: 1, bytes: 4, binary: true, hex: "0a0b", truncated: true },
  ],
  routes: { R: 7 },
  pending_partials: 0,
};
