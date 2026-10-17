import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import AccountPage from "@/app/(main)/account/page";
import AuditPage from "@/app/(main)/audit/page";
import LogsPage from "@/app/(main)/logs/page";
import ReportsPage from "@/app/(main)/reports/page";
import SettingsPage from "@/app/(main)/settings/page";
import UsersPage from "@/app/(main)/users/page";
import WorkersPage from "@/app/(main)/workers/page";
import { assigned, mockFetch, renderAs, reply } from "./helpers";

const last = (calls: { path: string }[], prefix: string) => calls.filter((c) => c.path.startsWith(prefix)).at(-1)!.path;

describe("reports", () => {
  it("switches reports, filters and links the CSV export", async () => {
    const calls = mockFetch({
      "GET reports/volume*": [
        { day: "2026-10-16T00:00:00Z", stream: "S", files: 1, in: 10, out: 8, err: 1, drop: 1, held: 0, dup: 0 },
        { day: "2026-10-15T00:00:00Z", stream: "S", files: 1, in: 10, out: 1, err: 0, drop: 0, held: 0, dup: 0 },
      ],
      "GET reports/routes*": [{ day: "2026-10-16T00:00:00Z", stream: "S", route: "R", files: 2, records: 30, bytes: 4096 }],
      "GET reports/reasons*": [{ stream: "S", stage: "MAIN", call_type: "MO", action: "DROP", reason: "ZERO", count: 4, duration: "0", volume: "0" }],
    });
    renderAs(<ReportsPage />);
    expect(await screen.findByText("yes")).toBeInTheDocument();
    expect(screen.getByText("NO")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Download CSV" }).getAttribute("href")).toMatch(/^\/bff\/reports\/volume\?from=.*&format=csv$/);
    await userEvent.click(screen.getByRole("tab", { name: "routes" }));
    expect(await screen.findByText("4.0 KiB")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "reasons" }));
    expect(await screen.findByText("ZERO")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Stream"), "S");
    await userEvent.clear(screen.getByLabelText("From"));
    await userEvent.clear(screen.getByLabelText("To"));
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "reports/reasons")).toBe("reports/reasons?stream=S"));
    expect(screen.getByRole("link", { name: "Download CSV" })).toHaveAttribute("href", "/bff/reports/reasons?stream=S&format=csv");
    await userEvent.type(screen.getByLabelText("From"), "2026-10-01");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "reports/reasons")).toBe("reports/reasons?stream=S&from=2026-10-01"));
  });

  it("shows an empty period", async () => {
    mockFetch({ "GET reports/volume*": reply(200, []) });
    renderAs(<ReportsPage />);
    expect(await screen.findByText("No data in the period")).toBeInTheDocument();
    mockFetch({ "GET reports/volume*": reply(500, { error: "x" }) });
  });
});

class FakeEventSource {
  static last: FakeEventSource;
  listeners: Record<string, (e: MessageEvent) => void> = {};
  closed = false;
  constructor(public url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(name: string, fn: (e: MessageEvent) => void) {
    this.listeners[name] = fn;
  }
  close() {
    this.closed = true;
  }
}

describe("logs", () => {
  it("searches history and tails live entries", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const calls = mockFetch({
      "GET logs*": [
        { ts: "2026-10-16T10:00:00Z", level: "ERROR", component: "med-engine", stream: "S", msg: "boom", attrs: { file: "x" } },
        { ts: "2026-10-16T10:00:01Z", level: "INFO", component: "med-api", msg: "ok" },
      ],
    });
    renderAs(<LogsPage />);
    expect(await screen.findByText("boom")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Level"), "ERROR");
    await userEvent.type(screen.getByLabelText("Component"), "med-engine");
    await userEvent.type(screen.getByLabelText("Stream"), "S");
    await userEvent.type(screen.getByLabelText("Text"), "bo");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "logs")).toBe("logs?level=ERROR&component=med-engine&stream=S&q=bo&limit=500"));

    await userEvent.click(screen.getByLabelText(/live tail/));
    expect(await screen.findByText("Waiting for new entries…")).toBeInTheDocument();
    const es = FakeEventSource.last;
    expect(es.url).toBe("/bff/logs/stream?level=ERROR&component=med-engine&stream=S&q=bo");
    act(() => es.listeners.log(new MessageEvent("log", { data: JSON.stringify({ ts: "2026-10-16T11:00:00Z", level: "WARN", component: "c", msg: "live one" }) })));
    expect(await screen.findByText("live one")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText(/live tail/));
    expect(es.closed).toBe(true);
    vi.unstubAllGlobals();
  });
});

describe("workers and audit", () => {
  it("lists workers", async () => {
    mockFetch({
      "GET workers": [
        { id: "med-engine@h:1", component: "med-engine", host: "h", pid: 1, started: "2026-10-16T09:00:00Z", seen: "2026-10-16T10:00:00Z", info: { version: "1.0" } },
        { id: "med-api@h:2", component: "med-api", host: "h", pid: 2, started: "2026-10-16T09:00:00Z", seen: "2026-10-16T10:00:00Z" },
      ],
    });
    renderAs(<WorkersPage />);
    expect(await screen.findByText("1.0")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });

  it("filters the audit trail", async () => {
    const calls = mockFetch({
      "GET audit*": [
        { id: "1", ts: "2026-10-16T10:00:00Z", actor: "bob", action: "publish", object_kind: "stream", object_name: "S", after: { n: 2 }, ip: "1.1.1.1" },
        { id: "2", ts: "2026-10-16T10:00:00Z", actor: "bob", action: "login", object_kind: "user", object_name: "bob", ip: "1.1.1.1" },
      ],
    });
    renderAs(<AuditPage />, ["publisher"]);
    expect(await screen.findByText('{"n":2}')).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Actor"), "bob");
    await userEvent.type(screen.getByLabelText("Action"), "publish");
    await userEvent.type(screen.getByLabelText("Object kind"), "stream");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(last(calls, "audit")).toBe("audit?actor=bob&action=publish&kind=stream&limit=500"));
  });
});

describe("users", () => {
  it("creates users, changes roles, disables and resets passwords", async () => {
    const calls = mockFetch({
      "GET users": [
        { username: "ann", roles: ["viewer"], disabled: false, created_at: "2026-10-16T10:00:00Z", last_login: "2026-10-16T11:00:00Z" },
      ],
      "POST users": reply(201, { username: "zed" }),
      "PUT users/ann": { ok: true },
    });
    renderAs(<UsersPage />);
    const row = (await screen.findByText("ann")).closest("tr")!;
    const r = within(row);
    await userEvent.click(r.getByLabelText("operator"));
    await waitFor(() => expect(calls.at(-2)!.body).toEqual({ roles: ["viewer", "operator"], disabled: false }));
    await userEvent.click(r.getByLabelText("viewer"));
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT").at(-1)!.body).toEqual({ roles: [], disabled: false }));
    await userEvent.click(r.getByLabelText("enable ann"));
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT").at(-1)!.body).toEqual({ roles: ["viewer"], disabled: true }));
    const prompt = vi.spyOn(window, "prompt").mockReturnValueOnce(null).mockReturnValueOnce("New-Passw0rd!!");
    await userEvent.click(r.getByRole("button", { name: "reset password" }));
    await userEvent.click(r.getByRole("button", { name: "reset password" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT").at(-1)!.body).toEqual({ roles: ["viewer"], disabled: false, password: "New-Passw0rd!!" }));
    expect(prompt).toHaveBeenCalledTimes(2);
    expect(await screen.findByText("User ann updated")).toBeInTheDocument();

    const add = within(screen.getByRole("heading", { name: "Add user" }).closest(".card") as HTMLElement);
    await userEvent.type(add.getByLabelText("User name"), "zed");
    await userEvent.type(add.getByLabelText("Initial password"), "Zed-Passw0rd!!");
    await userEvent.click(add.getByLabelText("admin"));
    await userEvent.click(add.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("User zed created")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ username: "zed", password: "Zed-Passw0rd!!", roles: ["viewer", "admin"] });
  });
});

describe("settings", () => {
  it("shows and edits settings", async () => {
    const calls = mockFetch({ "GET settings": { retention: { files: 30 } }, "PUT settings/retention": { ok: true } });
    renderAs(<SettingsPage />);
    expect(await screen.findByText('{"files":30}')).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "edit" }));
    expect(screen.getByLabelText("Key")).toHaveValue("retention");
    await userEvent.clear(screen.getByLabelText("Value (JSON)"));
    await userEvent.type(screen.getByLabelText("Value (JSON)"), "not json");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/value must be JSON/)).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Value (JSON)"));
    await userEvent.type(screen.getByLabelText("Value (JSON)"), "7");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Setting retention saved")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")!.body).toBe(7);
  });

  it("is read-only for viewers", async () => {
    mockFetch({ "GET settings": {} });
    renderAs(<SettingsPage />, ["viewer"]);
    expect(await screen.findByText(/built-in defaults apply/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });
});

describe("account", () => {
  it("changes the password", async () => {
    let fail = true;
    const calls = mockFetch({ "POST auth/password": () => (fail ? reply(403, { error: "current password is wrong" }) : { ok: true }) });
    renderAs(<AccountPage />, ["viewer"], "vic");
    expect(screen.getByText("vic")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Current password"), "old");
    await userEvent.type(screen.getByLabelText(/^New password/), "Brand-New-Passw0rd");
    await userEvent.type(screen.getByLabelText("Repeat new password"), "different");
    await userEvent.click(screen.getByRole("button", { name: /Change password/ }));
    expect(await screen.findByText("the new passwords differ")).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Repeat new password"));
    await userEvent.type(screen.getByLabelText("Repeat new password"), "Brand-New-Passw0rd");
    await userEvent.click(screen.getByRole("button", { name: /Change password/ }));
    expect(await screen.findByText("current password is wrong")).toBeInTheDocument();
    fail = false;
    await userEvent.click(screen.getByRole("button", { name: /Change password/ }));
    await waitFor(() => expect(assigned()).toEqual(["/login"]));
    expect(calls.at(-1)!.body).toEqual({ current: "old", new: "Brand-New-Passw0rd" });
  });
});
