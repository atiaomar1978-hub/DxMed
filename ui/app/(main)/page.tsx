"use client";

import Link from "next/link";
import { Card, ErrorBox, Page, Stat, Table } from "@/components/ui";
import { fmtNum, fmtTime } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { Dashboard } from "@/lib/types";

const statuses = ["NEW", "LEASED", "COMMITTED", "POSTED", "FAILED", "HELD", "DEAD", "DUPLICATE", "CANCELLED"];

export default function DashboardPage() {
  const d = useApi<Dashboard>("dashboard", 10000);
  const v = d.data?.last_24h;
  const streams = Object.keys({ ...d.data?.queue, ...d.data?.partials }).sort();
  return (
    <Page title="Dashboard" actions={<button onClick={d.reload}>Refresh</button>}>
      <ErrorBox error={d.error} />
      {d.data && v && (
        <>
          <div className="stats">
            <Stat label="Active streams" value={d.data.active_streams} />
            <Stat label="Files (24 h)" value={fmtNum(v.files)} />
            <Stat label="Records in" value={fmtNum(v.in)} />
            <Stat label="Records out" value={fmtNum(v.out)} tone="ok" />
            <Stat label="Rejected" value={fmtNum(v.err)} tone={v.err ? "warn" : ""} />
            <Stat label="Dropped" value={fmtNum(v.drop)} />
            <Stat label="Duplicates" value={fmtNum(v.dup)} />
            <Stat label="Open alarms" value={<Link href="/alarms">{d.data.active_alarms}</Link>} tone={d.data.active_alarms ? "bad" : "ok"} />
            <Stat label="Pending post-actions" value={d.data.pending_post_actions} />
          </div>
          <div className="grid2">
            <Card title="Files by status">
              <div className="chips">
                {statuses.map((s) => (
                  <Link key={s} href={`/files?status=${s}`} className="chip">
                    {s} <b>{fmtNum(d.data!.by_status[s] ?? 0)}</b>
                  </Link>
                ))}
              </div>
            </Card>
            <Card title="Streams">
              <Table
                rows={streams.map((s) => ({ s, q: d.data!.queue[s] ?? 0, p: d.data!.partials[s] ?? 0 }))}
                rowKey={(r) => r.s}
                empty="Queue empty, no pending partials"
                columns={[
                  { key: "s", label: "Stream" },
                  { key: "q", label: "Queued", align: "right" },
                  { key: "p", label: "Pending partials", align: "right" },
                ]}
              />
            </Card>
          </div>
          <Card title="Workers">
            <Table
              rows={d.data.workers ?? []}
              rowKey={(w) => w.id}
              empty="No component has reported in the last minutes"
              columns={[
                { key: "component", label: "Component" },
                { key: "host", label: "Host" },
                { key: "pid", label: "PID", align: "right" },
                { key: "seen", label: "Last seen", render: (w) => fmtTime(w.seen) },
              ]}
            />
          </Card>
        </>
      )}
    </Page>
  );
}
