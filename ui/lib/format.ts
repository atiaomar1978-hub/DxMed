export const roles = ["viewer", "operator", "configurator", "publisher", "admin"] as const;
export type Role = (typeof roles)[number];

// allows mirrors the API hierarchy: every role includes the ones before it.
export function allows(have: string[] | undefined, need: Role): boolean {
  const n = roles.indexOf(need);
  return (have ?? []).some((r) => roles.indexOf(r as Role) >= n);
}

export function fmtTime(t: string | null | undefined): string {
  if (!t) return "";
  const d = new Date(t);
  if (Number.isNaN(d.getTime()) || d.getUTCFullYear() < 1900) return "";
  return d.toISOString().replace("T", " ").replace(/\.\d+Z$/, "Z");
}

export function fmtNum(n: number | null | undefined): string {
  return n === null || n === undefined ? "" : n.toLocaleString("en-US");
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

const tone: Record<string, string> = {
  COMMITTED: "ok", POSTED: "ok", NEW: "info", LEASED: "info", HELD: "warn", FAILED: "warn", DUPLICATE: "muted",
  CANCELLED: "muted", DEAD: "bad", ERROR: "bad", critical: "bad", major: "bad", minor: "warn", warning: "warn",
  WARN: "warn", INFO: "info", DEBUG: "muted",
};

export function toneOf(s: string): string {
  return tone[s] ?? "muted";
}

// fmtDetail shows a rejection detail: strings as-is, structured details as JSON.
export function fmtDetail(d: unknown): string {
  if (d === null || d === undefined) return "";
  return typeof d === "string" ? d : JSON.stringify(d);
}

// dayInput returns YYYY-MM-DD for <input type=date>, n days from today (UTC).
export function dayInput(offset = 0, now = new Date()): string {
  return new Date(now.getTime() + offset * 86400000).toISOString().slice(0, 10);
}
