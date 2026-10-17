// Browser client for the BFF (/bff/* is proxied to med-api /api/v1/*).

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public details?: unknown,
  ) {
    super(message);
  }
}

export interface Options {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
}

export interface Reply<T> {
  data: T;
  etag: string | null;
}

export const base = "/bff/";

export function onUnauthorized() {
  if (!window.location.pathname.startsWith("/login")) window.location.assign("/login");
}

export async function request<T = unknown>(path: string, o: Options = {}): Promise<Reply<T>> {
  const method = o.method ?? (o.body === undefined ? "GET" : "POST");
  const headers: Record<string, string> = { accept: "application/json", ...o.headers };
  if (method !== "GET") headers["x-med-csrf"] = "1";
  let body: BodyInit | undefined;
  if (o.body !== undefined) {
    headers["content-type"] = "application/json";
    body = JSON.stringify(o.body);
  }
  const res = await fetch(base + path.replace(/^\//, ""), { method, headers, body, credentials: "same-origin" });
  const text = await res.text();
  let data: unknown = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = text;
  }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith("auth/login")) onUnauthorized();
    const e = (data ?? {}) as { error?: string; details?: unknown };
    throw new ApiError(res.status, e.error ?? `HTTP ${res.status}`, e.details);
  }
  return { data: data as T, etag: res.headers.get("etag") };
}

export async function api<T = unknown>(path: string, o: Options = {}): Promise<T> {
  return (await request<T>(path, o)).data;
}

// qs builds a query string from the non-empty values.
export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "" && v !== false) p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return e instanceof Error ? e.message : String(e);
}

// toBase64 encodes file bytes for JSON ([]byte fields of the API).
export function toBase64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}
