import { readFileSync } from "node:fs";
import { Agent, fetch as undiciFetch } from "undici";
import type { Fetch, ProxyConfig } from "./proxy";

let cached: ProxyConfig | undefined;

// upstream builds the proxy configuration from MED_API_URL and the CA bundle MED_API_CA (TLS verified).
export function upstream(env: Record<string, string | undefined> = process.env): ProxyConfig {
  if (cached && env === process.env) return cached;
  const ca = env.MED_API_CA ? readFileSync(env.MED_API_CA) : undefined;
  const agent = new Agent({ connect: { ca }, bodyTimeout: 0 });
  const fetch: Fetch = (url, init) => undiciFetch(url, { ...(init as object), dispatcher: agent }) as unknown as Promise<Response>;
  const cfg = { apiUrl: env.MED_API_URL || "https://localhost:8543", fetch, secureCookie: env.MED_UI_INSECURE_COOKIE !== "1" };
  if (env === process.env) cached = cfg;
  return cfg;
}
