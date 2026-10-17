import { describe, expect, it } from "vitest";
import { api, ApiError, errorText, onUnauthorized, qs, request, toBase64 } from "@/lib/api";
import { parseDoc } from "@/lib/doc";
import { allows, dayInput, fmtBytes, fmtDetail, fmtNum, fmtTime, toneOf } from "@/lib/format";
import { kindLabel, nameField } from "@/lib/kinds";
import { assigned, mockFetch, reply } from "./helpers";

describe("api client", () => {
  it("sends JSON with the CSRF header and returns data and ETag", async () => {
    const calls = mockFetch({ "PUT config/x/y": reply(200, { version: 2 }, { etag: '"2"' }), "GET plain": () => reply(200, "not json") });
    const r = await request<{ version: number }>("/config/x/y", { method: "PUT", body: { a: 1 }, headers: { "if-match": '"1"' } });
    expect(r).toEqual({ data: { version: 2 }, etag: '"2"' });
    expect(calls[0].headers["x-med-csrf"]).toBe("1");
    expect(calls[0].headers["if-match"]).toBe('"1"');
    expect(calls[0].body).toEqual({ a: 1 });
    expect(await api("plain")).toBe("not json");
    expect(calls[1].headers["x-med-csrf"]).toBeUndefined();
  });

  it("posts when a body is given and maps errors", async () => {
    mockFetch({ "POST a": reply(422, { error: "bad", details: { x: 1 } }), "GET b": reply(500), "GET c": reply(401, { error: "auth" }) });
    const e = (await api("a", { body: {} }).catch((x) => x)) as ApiError;
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(422);
    expect(e.details).toEqual({ x: 1 });
    expect(errorText(e)).toBe("bad");
    await expect(api("b")).rejects.toThrow("HTTP 500");
    await expect(api("c")).rejects.toThrow("auth");
    expect(assigned()).toEqual(["/login"]);
  });

  it("does not redirect on a failed login or on the login page", async () => {
    mockFetch({ "POST auth/login": reply(401, { error: "no" }) });
    await expect(api("auth/login", { body: {} })).rejects.toThrow("no");
    window.location.pathname = "/login";
    onUnauthorized();
    expect(assigned()).toEqual([]);
  });

  it("builds query strings and encodes bytes", () => {
    expect(qs({ a: "x y", b: "", c: undefined, d: null, e: false, f: true, g: 0 })).toBe("?a=x+y&f=true&g=0");
    expect(qs({})).toBe("");
    expect(errorText(new Error("e"))).toBe("e");
    expect(errorText("s")).toBe("s");
    expect(toBase64(new Uint8Array([104, 105]))).toBe("aGk=");
    expect(atob(toBase64(new Uint8Array(70000).fill(65))).length).toBe(70000);
  });
});

describe("format", () => {
  it("checks roles hierarchically", () => {
    expect(allows(["publisher"], "configurator")).toBe(true);
    expect(allows(["viewer"], "operator")).toBe(false);
    expect(allows(undefined, "viewer")).toBe(false);
    expect(allows(["bogus"], "viewer")).toBe(false);
  });
  it("formats values", () => {
    expect(fmtTime("2026-10-16T10:00:00.123Z")).toBe("2026-10-16 10:00:00Z");
    expect(fmtTime("0001-01-01T00:00:00Z")).toBe("");
    expect(fmtTime("garbage")).toBe("");
    expect(fmtTime(undefined)).toBe("");
    expect(fmtNum(1234567)).toBe("1,234,567");
    expect(fmtNum(null)).toBe("");
    expect(fmtBytes(10)).toBe("10 B");
    expect(fmtBytes(2048)).toBe("2.0 KiB");
    expect(fmtBytes(5 * 1024 ** 5)).toBe("5120.0 TiB");
    expect(toneOf("DEAD")).toBe("bad");
    expect(toneOf("?")).toBe("muted");
    expect(dayInput(1, new Date("2026-10-16T12:00:00Z"))).toBe("2026-10-17");
    expect(dayInput()).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(fmtDetail(null)).toBe("");
    expect(fmtDetail(undefined)).toBe("");
    expect(fmtDetail("")).toBe("");
    expect(fmtDetail("bad tag")).toBe("bad tag");
    expect(fmtDetail([{ code: "REGEX" }])).toBe('[{"code":"REGEX"}]');
  });
  it("describes kinds", () => {
    expect(kindLabel("route")).toBe("Routes");
    expect(kindLabel("x")).toBe("x");
    expect(nameField("route")).toBe("code");
    expect(nameField("stream")).toBe("name");
  });
  it("parses documents", () => {
    expect(parseDoc("{", "name", "A").error).toMatch(/^JSON/);
    expect(parseDoc("[]", "name", "A").error).toMatch(/object/);
    expect(parseDoc("null", "name", "A").error).toMatch(/object/);
    expect(parseDoc('{"name":"B"}', "name", "A").error).toMatch(/must stay/);
    expect(parseDoc("{}", "name", "").error).toMatch(/required/);
    expect(parseDoc('{"name":"A"}', "name", "A").doc).toEqual({ name: "A" });
    expect(parseDoc('{"code":"N"}', "code", "").doc).toEqual({ code: "N" });
  });
});
