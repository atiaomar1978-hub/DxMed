// @vitest-environment node
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it, vi } from "vitest";
import { proxy, readCookie, upstreamUrl, type ProxyConfig } from "@/lib/proxy";

vi.mock("undici", () => ({
  Agent: class {
    constructor(public o: unknown) {}
  },
  fetch: vi.fn(async () => new Response("up")),
}));

function cfg(handler: (url: string, init: RequestInit) => Response | Promise<Response>, secure = true): ProxyConfig & { calls: [string, RequestInit][] } {
  const calls: [string, RequestInit][] = [];
  return {
    apiUrl: "https://api:8543/",
    secureCookie: secure,
    calls,
    fetch: async (url, init) => {
      calls.push([url, init]);
      return handler(url, init);
    },
  };
}

const req = (method: string, path: string, init: RequestInit & { headers?: Record<string, string> } = {}) =>
  new Request(`https://ui:8090/bff/${path}`, { method, ...init });

describe("proxy", () => {
  it("forwards GET requests with the session as bearer token", async () => {
    const c = cfg(() => new Response('{"a":1}', { status: 200, headers: { "content-type": "application/json", etag: '"3"', "x-secret": "no" } }));
    const res = await proxy(
      req("GET", "files?limit=5", { headers: { cookie: "other=1; med_session=tok%3D", accept: "application/json", "x-forwarded-for": "10.0.0.1" } }),
      ["files"],
      c,
    );
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ a: 1 });
    expect(res.headers.get("etag")).toBe('"3"');
    expect(res.headers.get("x-secret")).toBeNull();
    const [url, init] = c.calls[0];
    expect(url).toBe("https://api:8543/api/v1/files?limit=5");
    const h = init.headers as Headers;
    expect(h.get("authorization")).toBe("Bearer tok=");
    expect(h.get("x-forwarded-for")).toBe("10.0.0.1");
    expect(init.body).toBeUndefined();
  });

  it("requires the CSRF header on mutations and rejects bad paths", async () => {
    const c = cfg(() => new Response("{}"));
    expect((await proxy(req("POST", "x", { body: "{}" }), ["x"], c)).status).toBe(403);
    expect((await proxy(req("GET", "x"), ["a", ".."], c)).status).toBe(400);
    expect((await proxy(req("GET", "x"), ["a/b"], c)).status).toBe(400);
    expect(c.calls).toHaveLength(0);
  });

  it("stores the login token in an HttpOnly cookie and hides it", async () => {
    const exp = new Date(Date.now() + 3600_000).toISOString();
    const c = cfg(() => new Response(JSON.stringify({ token: "T", username: "u", roles: ["admin"], expires_at: exp }), { status: 200 }));
    const res = await proxy(req("POST", "auth/login", { body: '{"username":"u"}', headers: { "x-med-csrf": "1", "content-type": "application/json" } }), ["auth", "login"], c);
    const body = await res.json();
    expect(body).toEqual({ username: "u", roles: ["admin"], expires_at: exp });
    const ck = res.headers.get("set-cookie")!;
    expect(ck).toMatch(/^med_session=T; Path=\/; HttpOnly; SameSite=Strict; Max-Age=35\d\d; Secure$/);
    expect(new TextDecoder().decode(c.calls[0][1].body as ArrayBuffer)).toBe('{"username":"u"}');
  });

  it("clears the cookie on logout and on 401", async () => {
    const c = cfg(() => new Response(null, { status: 401 }), false);
    const res = await proxy(req("GET", "auth/me"), ["auth", "me"], c);
    expect(res.status).toBe(401);
    expect(res.headers.get("set-cookie")).toBe("med_session=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0");
    const ok = cfg(() => new Response('{"ok":true}', { status: 200 }));
    const out = await proxy(req("POST", "auth/logout", { headers: { "x-med-csrf": "1" } }), ["auth", "logout"], ok);
    expect(out.headers.get("set-cookie")).toContain("Max-Age=0");
    const failed = cfg(() => new Response('{"error":"x"}', { status: 401 }));
    const login = await proxy(req("POST", "auth/login", { headers: { "x-med-csrf": "1" }, body: "{}" }), ["auth", "login"], failed);
    expect(login.status).toBe(401);
  });

  it("reports an unreachable API", async () => {
    const c = cfg(() => {
      throw new Error("ECONNREFUSED");
    });
    const res = await proxy(req("GET", "x"), ["x"], c);
    expect(res.status).toBe(502);
  });

  it("parses cookies and builds upstream URLs", () => {
    expect(readCookie(null, "a")).toBe("");
    expect(readCookie("a=1=2; b=3", "a")).toBe("1=2");
    expect(upstreamUrl({ apiUrl: "https://h", fetch: vi.fn(), secureCookie: true }, ["x y"], "?q")).toBe("https://h/api/v1/x%20y?q");
  });
});

describe("upstream", () => {
  it("builds a verified client from the environment and caches the process default", async () => {
    const { upstream } = await import("@/lib/upstream");
    const dir = mkdtempSync(join(tmpdir(), "ca"));
    writeFileSync(join(dir, "ca.crt"), "CA");
    const c = upstream({ MED_API_URL: "https://api:1", MED_API_CA: join(dir, "ca.crt"), MED_UI_INSECURE_COOKIE: "1" });
    expect(c.apiUrl).toBe("https://api:1");
    expect(c.secureCookie).toBe(false);
    expect(await (await c.fetch("https://api:1/x", { method: "GET" })).text()).toBe("up");
    const d = upstream({});
    expect(d.apiUrl).toBe("https://localhost:8543");
    expect(d.secureCookie).toBe(true);
    expect(upstream()).toBe(upstream());
  });

  it("routes every method through the proxy", async () => {
    vi.resetModules();
    vi.doMock("@/lib/upstream", () => ({ upstream: () => ({ apiUrl: "https://a", secureCookie: true, fetch: async () => new Response('{"ok":1}') }) }));
    const route = await import("@/app/bff/[...path]/route");
    const res = await route.GET(new Request("https://ui/bff/dashboard"), { params: Promise.resolve({ path: ["dashboard"] }) });
    expect(await res.json()).toEqual({ ok: 1 });
    expect(route.POST).toBe(route.GET);
    vi.doUnmock("@/lib/upstream");
  });
});
