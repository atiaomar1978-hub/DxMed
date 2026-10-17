// JSON shapes of med-api (internal/store, internal/lab).

export interface MedFile {
  id: string;
  stream: string;
  source: string;
  name: string;
  path: string;
  size: number;
  sha256: string;
  exchange_id: string;
  status: string;
  priority: number;
  attempts: number;
  not_before: string;
  lease_owner?: string;
  lease_until?: string;
  release_id?: string;
  records_in: number;
  records_out: number;
  records_err: number;
  records_drop: number;
  records_held: number;
  records_dup: number;
  dup_of?: string;
  error_code?: string;
  error?: string;
  received_at: string;
  started_at?: string;
  committed_at?: string;
}

export interface FilePage {
  items: MedFile[];
  next: string;
}

export interface StatRow {
  stream?: string;
  stage: string;
  call_type: string;
  action: string;
  reason: string;
  count: number;
  duration: string;
  volume: string;
}

export interface Output {
  id: string;
  file_id?: string;
  stream: string;
  route: string;
  name: string;
  path: string;
  records: number;
  bytes: number;
  sha256: string;
  visible: boolean;
  created_at: string;
}

export interface ErrorRecord {
  day: string;
  file_id?: string;
  stream: string;
  seq: number;
  offset: number;
  stage: string;
  reason: string;
  detail?: unknown;
  raw_hex?: string;
}

export interface FileEvent {
  ts: string;
  status: string;
  actor: string;
  detail: string;
}

export interface FileDetail {
  file: MedFile;
  events: FileEvent[];
  stats: StatRow[];
  outputs: Output[];
  errors: ErrorRecord[];
}

export interface Partial {
  key: string;
  stream: string;
  release_id: string;
  first_seen: string;
  last_seen: string;
  count: number;
}

export interface Worker {
  id: string;
  component: string;
  host: string;
  pid: number;
  started: string;
  seen: string;
  info?: Record<string, string>;
}

export interface VolumeRow {
  day: string;
  stream: string;
  files: number;
  in: number;
  out: number;
  err: number;
  drop: number;
  held: number;
  dup: number;
}

export interface RouteRow {
  day: string;
  stream: string;
  route: string;
  files: number;
  records: number;
  bytes: number;
}

export interface Dashboard {
  by_status: Record<string, number>;
  queue: Record<string, number>;
  last_24h: VolumeRow;
  partials: Record<string, number>;
  active_alarms: number;
  workers: Worker[] | null;
  pending_post_actions: number;
  active_streams: number;
}

export interface Alarm {
  id: string;
  rule: string;
  severity: string;
  stream: string;
  message: string;
  raised_at: string;
  acked_by?: string;
  acked_at?: string;
  cleared_at?: string;
}

export interface LogEntry {
  ts: string;
  level: string;
  component: string;
  stream?: string;
  file_id?: string;
  msg: string;
  attrs?: Record<string, unknown>;
}

export interface AuditEntry {
  id: string;
  ts: string;
  actor: string;
  action: string;
  object_kind: string;
  object_name: string;
  after?: unknown;
  ip: string;
}

export interface User {
  username: string;
  roles: string[];
  disabled: boolean;
  created_at: string;
  last_login?: string;
}

export interface ConfigObject {
  kind: string;
  name: string;
  version: number;
  doc?: Record<string, unknown>;
  updated_by: string;
  updated_at: string;
}

export interface Release {
  id: string;
  stream: string;
  number: number;
  sha256: string;
  note: string;
  published_by: string;
  published_at: string;
  active: boolean;
}

export interface StreamInfo {
  name: string;
  version: number;
  doc: Record<string, unknown>;
  active?: Release;
  queue: number;
  partials: number;
}

export interface Problem {
  severity: string;
  path: string;
  message: string;
}

export interface PublishResult {
  release?: Release;
  unchanged: boolean;
  problems: Problem[] | null;
  gate?: Record<string, string[]>;
}

export interface Counts {
  in: number;
  out: number;
  err: number;
  drop: number;
  held: number;
  dup: number;
  merged: number;
  expired: number;
}

export interface FieldError {
  field: string;
  code: string;
  message?: string;
}

export interface Sample {
  seq: number;
  offset: number;
  length: number;
  record_type: string;
  call_type: string;
  fields: Record<string, string>;
  errors?: FieldError[];
  stage: string;
  action: string;
  reason: string;
  routes?: string[];
}

export interface Preview {
  route: string;
  records: number;
  bytes: number;
  binary: boolean;
  text?: string;
  hex?: string;
  truncated: boolean;
}

export interface LabReport {
  problems: Problem[] | null;
  counts: Counts;
  balanced: boolean;
  decode: { records: number; errors: number; skipped: number };
  stats: StatRow[] | null;
  samples: Sample[] | null;
  rejects: { seq: number; offset: number; stage: string; reason: string }[] | null;
  outputs: Preview[] | null;
  routes: Record<string, number> | null;
  pending_partials: number;
}

export interface LabCase {
  stream: string;
  name: string;
  file_name: string;
  expected: { counts: Counts; routes: Record<string, number> };
  updated_by: string;
  updated_at: string;
}
