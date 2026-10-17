import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import AlarmsPage from "@/app/(main)/alarms/page";
import ErrorsPage from "@/app/(main)/errors/page";
import FileDetailPage from "@/app/(main)/files/[id]/page";
import FilesPage from "@/app/(main)/files/page";
import OutputsPage from "@/app/(main)/outputs/page";
import DashboardPage from "@/app/(main)/page";
import PartialsPage from "@/app/(main)/partials/page";
import { dashboard, detail, file } from "./fixtures";
import { mockFetch, renderAs, reply } from "./helpers";
import { nav } from "./nav";

const last = (calls: { path: string }[], prefix: string) => calls.filter((c) => c.path.startsWith(prefix)).at(-1)!.path;

describe("dashboard", () => {
  it("summarises the platform", async () => {
    const calls = mockFetch({ "GET dashboard": dashboard });
    renderAs(<DashboardPage />);
    expect(await screen.findByText("Active streams")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /FAILED 1/ })).toHaveAttribute("href", "/files?status=FAILED");
    expect(screen.getByText("MSC_ERICSSON")).toBeInTheDocument();
    expect(screen.getByText("PGW_DATA")).toBeInTheDocument();
    expect(screen.getByText("med-engine")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(calls.length).toBe(2));
  });
  it("handles an idle system and errors", async () => {
    mockFetch({ "GET dashboard": { ...dashboard, queue: {}, partials: {}, workers: null, active_alarms: 0, last_24h: { ...dashboard.last_24h, err: 0 } } });
    renderAs(<DashboardPage />);
    expect(await screen.findByText("Queue empty, no pending partials")).toBeInTheDocument();
    expect(screen.getByText(/No component has reported/)).toBeInTheDocument();
  });
  it("shows load errors", async () => {
    mockFetch({ "GET dashboard": reply(500, { error: "db down" }) });
    renderAs(<DashboardPage />);
    expect(await screen.findByText("db down")).toBeInTheDocument();
  });
});

describe("files", () => {
  it("filters and pages", async () => {
    nav.search = new URLSearchParams("status=FAILED");
    const calls = mockFetch({
      "GET files*": (c: { path: string }) => (c.path.includes("before=c1") ? { items: [], next: "" } : { items: [file], next: "c1" }),
    });
    renderAs(<FilesPage />);
    expect(await screen.findByRole("link", { name: file.name })).toHaveAttribute("href", "/files/f1");
    expect(last(calls, "files")).toContain("status=FAILED");
    await userEvent.click(screen.getByRole("button", { name: "Older" }));
    expect(await screen.findByText("No files match")).toBeInTheDocument();
    expect(last(calls, "files")).toContain("before=c1");
    await userEvent.click(screen.getByRole("button", { name: "Newer" }));
    await screen.findByRole("link", { name: file.name });
    await userEvent.type(screen.getByLabelText("Stream"), "PGW");
    await userEvent.selectOptions(screen.getByLabelText("Status"), "DEAD");
    await userEvent.type(screen.getByLabelText("Name contains"), "abc");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "files")).toContain("stream=PGW&status=DEAD&q=abc"));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });

  it("works without search parameters", async () => {
    nav.search = null as unknown as URLSearchParams;
    mockFetch({ "GET files*": reply(500, { error: "x" }) });
    renderAs(<FilesPage />);
    expect(await screen.findByText("x")).toBeInTheDocument();
  });
});

describe("file detail", () => {
  it("shows the file and applies operator actions", async () => {
    nav.params = { id: "f1" };
    const calls = mockFetch({
      "GET files/f1": detail,
      "POST files/f1/actions": (c: { body: { action: string } }) => (c.body.action === "hold" ? reply(409, { error: "cannot hold" }) : { ...file, status: "NEW" }),
    });
    renderAs(<FileDetailPage />, ["operator"]);
    expect(await screen.findByText("MSC01_000001.ber")).toBeInTheDocument();
    expect(screen.getByText("IO: disk")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "release" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "reprocess" }));
    expect(await screen.findByText("reprocess: file is now NEW")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")[0].body).toEqual({ action: "reprocess" });
    await userEvent.click(screen.getByRole("button", { name: "hold" }));
    expect(await screen.findByText("cannot hold")).toBeInTheDocument();
    expect(screen.getByText("pending")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /All rejected records/ })).toHaveAttribute("href", "/errors?file_id=f1");
  });

  it("hides actions from viewers", async () => {
    nav.params = { id: "f1" };
    mockFetch({ "GET files/f1": { ...detail, file: { ...file, error: undefined } } });
    renderAs(<FileDetailPage />, ["viewer"]);
    await screen.findByText("MSC01_000001.ber");
    expect(screen.queryByRole("button", { name: "reprocess" })).toBeNull();
  });

  it("shows load errors", async () => {
    nav.params = { id: "zz" };
    mockFetch({});
    renderAs(<FileDetailPage />);
    expect(await screen.findByText("no mock for GET files/zz")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "File" })).toBeInTheDocument();
  });
});

describe("outputs and errors", () => {
  it("lists output files", async () => {
    const calls = mockFetch({ "GET outputs*": [...detail.outputs, { ...detail.outputs[0], id: "o3", file_id: undefined }] });
    renderAs(<OutputsPage />);
    expect(await screen.findAllByText("R.csv")).toHaveLength(2);
    expect(screen.getByText("flush")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Stream"), "S");
    await userEvent.type(screen.getByLabelText("Route"), "R");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "outputs")).toContain("stream=S&route=R"));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });

  it("lists rejected records", async () => {
    nav.search = new URLSearchParams("file_id=f1");
    const calls = mockFetch({ "GET errors*": [...detail.errors, { ...detail.errors[0], seq: 9, file_id: undefined }] });
    renderAs(<ErrorsPage />);
    expect(await screen.findAllByText("BAD_IMSI")).toHaveLength(2);
    expect(last(calls, "errors")).toContain("file_id=f1");
    await userEvent.type(screen.getByLabelText("Stream"), "S");
    await userEvent.type(screen.getByLabelText("Reason"), "X");
    await userEvent.clear(screen.getByLabelText("File id"));
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "errors")).toContain("stream=S&reason=X&limit=500"));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });

  it("works without search parameters", async () => {
    nav.search = null as unknown as URLSearchParams;
    mockFetch({ "GET errors*": [] });
    renderAs(<ErrorsPage />);
    expect(await screen.findByText("No rejected records")).toBeInTheDocument();
  });
});

describe("partials", () => {
  it("closes selected and all groups", async () => {
    const parts = [
      { key: "a".repeat(32), stream: "PGW_DATA", release_id: "r", first_seen: "2026-10-16T10:00:00Z", last_seen: "2026-10-16T10:00:00Z", count: 2 },
      { key: "b".repeat(32), stream: "PGW_DATA", release_id: "r", first_seen: "2026-10-16T10:00:00Z", last_seen: "2026-10-16T10:00:00Z", count: 1 },
    ];
    const calls = mockFetch({ "GET partials*": parts, "POST partials/close": (c: { body: { keys?: string[] } }) => ({ closed: c.body.keys?.length ?? 2 }) });
    renderAs(<PartialsPage />, ["operator"]);
    const box = await screen.findByLabelText(`select ${"a".repeat(32)}`);
    await userEvent.click(box);
    await userEvent.click(screen.getByLabelText(`select ${"b".repeat(32)}`));
    await userEvent.click(screen.getByLabelText(`select ${"b".repeat(32)}`));
    await userEvent.click(screen.getByRole("button", { name: "Close selected (1)" }));
    expect(await screen.findByText("1 group(s) closed")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ stream: "PGW_DATA", keys: ["a".repeat(32)] });
    await userEvent.click(screen.getByRole("button", { name: "Close all" }));
    expect(await screen.findByText("2 group(s) closed")).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Stream"));
    await userEvent.type(screen.getByLabelText("Stream"), "X");
    await waitFor(() => expect(last(calls, "partials?")).toContain("stream=X"));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });
});

describe("alarms", () => {
  it("acknowledges and includes cleared alarms", async () => {
    const alarms = [
      { id: "a1", rule: "queue_depth", severity: "major", stream: "S", message: "deep", raised_at: "2026-10-16T10:00:00Z" },
      { id: "a2", rule: "seq_gap", severity: "minor", stream: "S", message: "gap", raised_at: "2026-10-16T10:00:00Z", acked_by: "bob", acked_at: "2026-10-16T11:00:00Z" },
    ];
    const calls = mockFetch({ "GET alarms*": alarms, "POST alarms/a1/ack": { ok: true } });
    renderAs(<AlarmsPage />, ["operator"]);
    const row = (await screen.findByText("deep")).closest("tr")!;
    await userEvent.click(within(row).getByRole("button", { name: "Acknowledge" }));
    await waitFor(() => expect(calls.some((c) => c.path === "alarms/a1/ack")).toBe(true));
    expect(screen.getByText(/bob 2026-10-16 11:00:00Z/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox"));
    await waitFor(() => expect(last(calls, "alarms")).toBe("alarms?all=true"));
  });
});
