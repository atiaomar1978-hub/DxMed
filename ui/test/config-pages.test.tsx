import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import EditPage from "@/app/(main)/config/[kind]/[name]/page";
import KindPage from "@/app/(main)/config/[kind]/page";
import ConfigPage from "@/app/(main)/config/page";
import LabPage from "@/app/(main)/lab/page";
import StreamsPage from "@/app/(main)/streams/page";
import { rel1, rel2, report, streams } from "./fixtures";
import { mockFetch, renderAs, reply, type Call } from "./helpers";
import { nav } from "./nav";

describe("streams", () => {
  it("validates, publishes and rolls back", async () => {
    let publish: unknown = { release: rel2, unchanged: true, problems: null };
    const calls = mockFetch({
      "GET streams": streams,
      "GET streams/MSC_ERICSSON/releases": [rel2, rel1],
      "GET streams/PGW_DATA/releases": [],
      "POST streams/MSC_ERICSSON/validate": { ok: false, problems: [{ severity: "error", path: "routes", message: "empty" }] },
      "POST streams/PGW_DATA/validate": { ok: true, problems: null },
      "POST streams/MSC_ERICSSON/publish": () => publish,
      "POST releases/r1/activate": rel1,
    });
    renderAs(<StreamsPage />, ["admin"]);
    const card = (await screen.findByRole("heading", { name: "MSC_ERICSSON" })).closest(".card") as HTMLElement;
    const c = within(card);
    expect(await c.findByText("Activate (rollback)")).toBeInTheDocument();
    expect(c.getByText("voice")).toBeInTheDocument();

    await userEvent.click(c.getByRole("button", { name: "Validate drafts" }));
    expect(await c.findByText("Drafts have errors")).toBeInTheDocument();
    expect(c.getByText("empty")).toBeInTheDocument();

    await userEvent.type(c.getByLabelText("Release note"), "n1");
    await userEvent.click(c.getByRole("checkbox"));
    await userEvent.click(c.getByRole("button", { name: "Publish" }));
    expect(await c.findByText("No change: release #2 stays active")).toBeInTheDocument();
    expect(calls.find((x) => x.path.endsWith("/publish"))!.body).toEqual({ note: "n1", force: true });

    publish = { release: { ...rel2, number: 3 }, unchanged: false, problems: [] };
    await userEvent.click(c.getByRole("button", { name: "Publish" }));
    expect(await c.findByText("Published release #3")).toBeInTheDocument();

    publish = reply(422, { error: "template lab cases fail", details: { problems: null, gate: { basic: ["in 1, want 2"] }, unchanged: false } });
    await userEvent.click(c.getByRole("button", { name: "Publish" }));
    expect(await c.findByText("template lab cases fail")).toBeInTheDocument();
    expect(c.getByText(/in 1, want 2/)).toBeInTheDocument();

    publish = reply(422, { error: "configuration has errors", details: { problems: [{ severity: "warning", path: "p", message: "w" }], unchanged: false } });
    await userEvent.click(c.getByRole("button", { name: "Publish" }));
    expect(await c.findByText("w")).toBeInTheDocument();

    publish = reply(500, { error: "internal error" });
    await userEvent.click(c.getByRole("button", { name: "Publish" }));
    expect(await c.findByText("internal error")).toBeInTheDocument();

    await userEvent.click(c.getByRole("button", { name: "Activate (rollback)" }));
    expect(await c.findByText("Release #1 is active")).toBeInTheDocument();

    const pgw = within((await screen.findByRole("heading", { name: "PGW_DATA" })).closest(".card") as HTMLElement);
    expect(pgw.getByText("none")).toBeInTheDocument();
    await userEvent.click(pgw.getByRole("button", { name: "Validate drafts" }));
    expect(await pgw.findByText("Drafts are valid")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });

  it("offers no force option to publishers and nothing to viewers", async () => {
    mockFetch({ "GET streams": [streams[0]], "GET streams/MSC_ERICSSON/releases": [rel2] });
    const { unmount } = renderAs(<StreamsPage />, ["publisher"]);
    await screen.findByRole("button", { name: "Publish" });
    expect(screen.queryByText(/ignore failing lab cases/)).toBeNull();
    unmount();
    renderAs(<StreamsPage />, ["viewer"]);
    await screen.findByRole("heading", { name: "MSC_ERICSSON" });
    expect(screen.queryByRole("button", { name: "Publish" })).toBeNull();
  });
});

describe("configuration objects", () => {
  it("lists kinds with counts", async () => {
    mockFetch({ "GET config": { route: 8 } });
    renderAs(<ConfigPage />);
    expect(await screen.findByRole("link", { name: "8 object(s)" })).toHaveAttribute("href", "/config/route");
    expect(screen.getAllByRole("link", { name: "0 object(s)" }).length).toBe(8);
  });

  it("lists objects of a kind and copies a template", async () => {
    nav.params = { kind: "route" };
    const objs = [
      { kind: "route", name: "R", version: 2, doc: { code: "R", description: "roaming" }, updated_by: "a", updated_at: "2026-10-16T10:00:00Z" },
      { kind: "route", name: "I", version: 1, doc: { code: "I", name: "Inbound roaming" }, updated_by: "a", updated_at: "2026-10-16T10:00:00Z" },
      { kind: "route", name: "L", version: 1, updated_by: "a", updated_at: "2026-10-16T10:00:00Z" },
    ];
    const calls = mockFetch({ "GET config/route": objs, "POST config/route/R/clone": reply(201, { version: 1 }) });
    renderAs(<KindPage />, ["configurator"]);
    expect(await screen.findByRole("link", { name: "R" })).toHaveAttribute("href", "/config/route/R");
    expect(screen.getByText("roaming")).toBeInTheDocument();
    expect(screen.getByText("Inbound roaming")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New" })).toHaveAttribute("href", "/config/route/_new");
    await userEvent.selectOptions(screen.getByLabelText("Copy from (template)"), "R");
    await userEvent.type(screen.getByLabelText("New name"), "R2");
    await userEvent.click(screen.getByRole("button", { name: "Create from template" }));
    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/config/route/R2"));
    expect(calls.at(-1)!.body).toEqual({ name: "R2" });
  });

  it("edits a document with optimistic locking and versions", async () => {
    nav.params = { kind: "route", name: "R" };
    const doc = { code: "R", dir: "/out" };
    const calls = mockFetch({
      "GET config/route/R": reply(200, { kind: "route", name: "R", version: 2, doc }, { etag: '"2"' }),
      "GET config/route/R/versions": [
        { kind: "route", name: "R", version: 2, doc, updated_by: "a", updated_at: "2026-10-16T10:00:00Z" },
        { kind: "route", name: "R", version: 1, doc: null, updated_by: "a", updated_at: "2026-10-16T09:00:00Z" },
      ],
      "GET config/route/R/versions/1": { kind: "route", name: "R", version: 1, doc: null },
      "GET config/route/R/versions/2": { kind: "route", name: "R", version: 2, doc: { code: "R", dir: "/old" } },
      "PUT config/route/R": (c: Call) => (c.headers["if-match"] === '"2"' ? reply(200, { version: 3 }, { etag: '"3"' }) : reply(412, { error: "changed meanwhile" })),
      "DELETE config/route/R": { ok: true },
    });
    renderAs(<EditPage />, ["configurator"]);
    const editor = screen.getByLabelText("document") as HTMLTextAreaElement;
    await waitFor(() => expect(editor.value).toContain('"dir": "/out"'));

    fireEvent.change(editor, { target: { value: "{" } });
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(/^JSON/)).toBeInTheDocument();

    fireEvent.change(editor, { target: { value: '{"code":"R","dir":"/new"}' } });
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(/Saved as version 3/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("changed meanwhile")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Revert" }));
    await waitFor(() => expect(editor.value).toContain('"dir": "/out"'));

    await userEvent.click(screen.getByRole("button", { name: "deleted" }));
    await waitFor(() => expect(editor.value).toBe("{}"));
    const loads = screen.getAllByRole("button", { name: "load" });
    await userEvent.click(loads[0]);
    await waitFor(() => expect(editor.value).toContain("/old"));

    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/config/route"));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(calls.find((c) => c.method === "DELETE")!.headers["if-match"]).toBe('"2"');
  });

  it("creates a new document", async () => {
    nav.params = { kind: "route", name: "_new" };
    const calls = mockFetch({ "PUT config/route/NEW": reply(201, { version: 1 }, { etag: '"1"' }) });
    renderAs(<EditPage />, ["configurator"]);
    const editor = screen.getByLabelText("document") as HTMLTextAreaElement;
    await waitFor(() => expect(JSON.parse(editor.value)).toEqual({ code: "" }));
    expect(screen.getByRole("button", { name: "Revert" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    fireEvent.change(editor, { target: { value: '{"code":"NEW"}' } });
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/config/route/NEW"));
    expect(calls[0].headers["if-match"]).toBeUndefined();
  });

  it("is read-only for viewers and reports load errors", async () => {
    nav.params = { kind: "stream", name: "X" };
    mockFetch({ "GET config/stream/X/versions": [] });
    renderAs(<EditPage />, ["viewer"]);
    expect(await screen.findByText("no mock for GET config/stream/X")).toBeInTheDocument();
    expect(screen.getByLabelText("document")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
  });
});

describe("template lab", () => {
  it("runs a sample, saves and deletes cases", async () => {
    let rep = report;
    const calls = mockFetch({
      "GET streams": streams,
      "GET lab/cases*": [{ stream: "MSC_ERICSSON", name: "c1", file_name: "f.ber", expected: { counts: report.counts, routes: {} }, updated_by: "a", updated_at: "2026-10-16T10:00:00Z" }],
      "POST lab/run": () => rep,
      "PUT lab/cases": { counts: report.counts },
      "DELETE lab/cases/MSC_ERICSSON/c1": { ok: true },
    });
    renderAs(<LabPage />, ["configurator"]);
    expect(await screen.findByText("c1")).toBeInTheDocument();
    const run = screen.getByRole("button", { name: "Run" });
    expect(run).toBeDisabled();
    fireEvent.change(screen.getByLabelText("sample file"), { target: { files: [] } });
    await userEvent.upload(screen.getByLabelText("sample file"), new File([new Uint8Array([1, 2, 3])], "MSC01_000001.ber"));
    await waitFor(() => expect(run).toBeEnabled());
    await userEvent.click(screen.getByLabelText(/active release instead of drafts/));
    await userEvent.click(screen.getByLabelText(/close open partials/));
    await userEvent.click(run);
    expect(await screen.findByText("Run complete: counts reconcile")).toBeInTheDocument();
    expect(calls.find((c) => c.path === "lab/run")!.body).toEqual({
      stream: "MSC_ERICSSON", file_name: "MSC01_000001.ber", data: "AQID", flush: false, samples: 20, release: "r2",
    });
    expect(screen.getByText("H|x")).toBeInTheDocument();
    expect(screen.getByText("0a0b")).toBeInTheDocument();
    expect(screen.getByText(/careful/)).toBeInTheDocument();

    rep = { ...report, balanced: false, problems: null, routes: null, samples: null, outputs: null };
    await userEvent.click(run);
    expect(await screen.findByText("Run complete: counts do NOT reconcile")).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Case name"), "basic");
    await userEvent.click(screen.getByRole("button", { name: /Save case/ }));
    expect(await screen.findByText(/Case basic saved/)).toBeInTheDocument();
    expect((calls.find((c) => c.method === "PUT")!.body as { release: string }).release).toBe("");

    await userEvent.click(screen.getByRole("button", { name: "delete" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));

    await userEvent.selectOptions(screen.getByLabelText("Stream"), "PGW_DATA");
    await waitFor(() => expect(calls.at(-1)!.path).toBe("lab/cases?stream=PGW_DATA"));
  });

  it("reports a failed stream list and loads no cases", async () => {
    const calls = mockFetch({ "GET streams": reply(500, { error: "streams down" }), "GET lab/cases*": [] });
    renderAs(<LabPage />, ["configurator"]);
    expect(await screen.findByText("streams down")).toBeInTheDocument();
    expect(screen.queryByRole("option")).toBeNull();
    expect(screen.queryByRole("heading", { name: /Saved cases of/ })).toBeNull();
    expect(calls.some((c) => c.path.startsWith("lab/cases"))).toBe(false);
  });
});
