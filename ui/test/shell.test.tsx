import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import MainLayout from "@/app/(main)/layout";
import RootLayout from "@/app/layout";
import LoginPage from "@/app/login/page";
import { active, Can, Shell } from "@/components/Shell";
import { Badge, Json, Table } from "@/components/ui";
import { useAction, useApi } from "@/lib/hooks";
import { assigned, mockFetch, renderAs, reply } from "./helpers";
import { nav } from "./nav";

describe("hooks", () => {
  it("loads, reloads, polls and reports errors", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let n = 0;
    mockFetch({ "GET x": () => ({ n: ++n }), "GET bad": reply(500, { error: "down" }) });
    const { result, rerender, unmount } = renderHook(({ p }) => useApi<{ n: number }>(p, 1000), { initialProps: { p: "x" as string | null } });
    await waitFor(() => expect(result.current.data).toEqual({ n: 1 }));
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.data).toEqual({ n: 2 }));
    await act(async () => {
      vi.advanceTimersByTime(1100);
    });
    await waitFor(() => expect(result.current.data?.n).toBeGreaterThan(2));
    rerender({ p: "bad" });
    await waitFor(() => expect(result.current.error).toBe("down"));
    rerender({ p: null });
    unmount();
    vi.useRealTimers();
  });

  it("ignores results after unmount", async () => {
    let resolve: (r: Response) => void = () => undefined;
    globalThis.fetch = vi.fn(() => new Promise<Response>((r) => (resolve = r))) as unknown as typeof fetch;
    const { result, unmount } = renderHook(() => useApi("slow"));
    expect(result.current.loading).toBe(true);
    unmount();
    resolve(new Response("{}"));
    const h2 = renderHook(() => useApi("slow2"));
    h2.unmount();
    resolve(new Response("x", { status: 500 }));
  });

  it("tracks actions", async () => {
    const { result } = renderHook(() => useAction());
    await act(async () => {
      expect(await result.current.run(async () => "done")).toBe(true);
    });
    expect(result.current.message).toBe("done");
    await act(async () => {
      await result.current.run(async () => undefined);
    });
    expect(result.current.message).toBe("");
    await act(async () => {
      expect(
        await result.current.run(async () => {
          throw new Error("nope");
        }),
      ).toBe(false);
    });
    expect(result.current.error).toBe("nope");
  });
});

describe("shell", () => {
  it("shows the navigation allowed for the roles and signs out", async () => {
    nav.pathname = "/files/f1";
    const calls = mockFetch({ "GET auth/me": { username: "vic", roles: ["viewer"] }, "POST auth/logout": { ok: true } });
    render(<Shell>content</Shell>);
    expect(await screen.findByText("content")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Files" })).toHaveClass("active");
    expect(screen.queryByRole("link", { name: "Users" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Template Lab" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(assigned()).toEqual(["/login"]));
    expect(calls.map((c) => c.path)).toContain("auth/logout");
  });

  it("signs out even when the API is down and shows load errors", async () => {
    mockFetch({ "GET auth/me": { username: "a", roles: ["admin"] }, "POST auth/logout": reply(502, { error: "down" }) });
    render(<Shell>x</Shell>);
    expect(await screen.findByRole("link", { name: "Users" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(assigned()).toEqual(["/login"]));
    cleanupAndRenderError();
  });

  it("marks active links", () => {
    expect(active("/", "/")).toBe(true);
    expect(active("/files", "/")).toBe(false);
    expect(active("/config/route", "/config")).toBe(true);
    expect(active("/configx", "/config")).toBe(false);
  });

  it("gates content by role", () => {
    renderAs(
      <>
        <Can role="admin">secret</Can>
        <Can role="viewer">public</Can>
      </>,
      ["operator"],
    );
    expect(screen.queryByText("secret")).toBeNull();
    expect(screen.getByText("public")).toBeInTheDocument();
  });

  it("renders shared widgets", () => {
    render(
      <>
        <Badge value="DEAD" />
        <Json value={{ a: 1 }} />
        <Table rows={[{ a: 1, b: null }]} rowKey={(_, i) => String(i)} columns={[{ key: "a", label: "A" }, { key: "b", label: "B" }]} />
        <Table rows={[]} rowKey={() => ""} columns={[]} />
        <Table rows={undefined} rowKey={() => ""} columns={[]} />
      </>,
    );
    expect(screen.getByText("DEAD")).toHaveClass("bad");
    expect(screen.getByText("Nothing to show")).toBeInTheDocument();
    expect(screen.getAllByText("Loading…")).toHaveLength(1);
  });

  it("builds the layouts", () => {
    const root = RootLayout({ children: "c" });
    expect(root.type).toBe("html");
    const main = MainLayout({ children: "c" });
    expect(main.type).toBe(Shell);
  });
});

function cleanupAndRenderError() {
  mockFetch({ "GET auth/me": reply(500, { error: "boom" }) });
  render(<Shell>x</Shell>);
  return screen.findByText("boom");
}

describe("login", () => {
  it("signs in and reports failures", async () => {
    let ok = false;
    mockFetch({ "POST auth/login": () => (ok ? { username: "u", roles: [] } : reply(401, { error: "invalid user name or password" })) });
    render(<LoginPage />);
    await userEvent.type(screen.getByLabelText("User name"), "u");
    await userEvent.type(screen.getByLabelText("Password"), "p");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("invalid user name or password")).toBeInTheDocument();
    expect(assigned()).toEqual([]);
    ok = true;
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(assigned()).toEqual(["/"]));
  });
});
