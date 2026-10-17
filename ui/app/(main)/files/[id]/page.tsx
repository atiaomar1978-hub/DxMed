"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { Can } from "@/components/Shell";
import { Badge, Card, ErrorBox, Notice, Page, Stat, Table } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtBytes, fmtDetail, fmtNum, fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import type { FileDetail, MedFile } from "@/lib/types";

// actions an operator may apply per status (internal/store fileActions)
const allowed: Record<string, string[]> = {
  reprocess: ["COMMITTED", "POSTED", "FAILED", "DEAD", "DUPLICATE", "CANCELLED"],
  hold: ["NEW", "FAILED"],
  release: ["HELD"],
  cancel: ["NEW", "HELD", "FAILED", "DEAD"],
};

export default function FileDetailPage() {
  const { id } = useParams<{ id: string }>();
  const d = useApi<FileDetail>(`files/${id}`);
  const act = useAction();
  const f = d.data?.file;

  const run = (action: string) =>
    act.run(async () => {
      const r = await api<MedFile>(`files/${id}/actions`, { body: { action } });
      d.reload();
      return `${action}: file is now ${r.status}`;
    });

  return (
    <Page title={f ? f.name : "File"} actions={<Link href="/files">All files</Link>}>
      <ErrorBox error={d.error || act.error} />
      <Notice text={act.message} />
      {d.data && f && (
        <>
          <div className="stats">
            <Stat label="Status" value={<Badge value={f.status} />} />
            <Stat label="In" value={fmtNum(f.records_in)} />
            <Stat label="Out" value={fmtNum(f.records_out)} tone="ok" />
            <Stat label="Rejected" value={fmtNum(f.records_err)} />
            <Stat label="Dropped" value={fmtNum(f.records_drop)} />
            <Stat label="Held" value={fmtNum(f.records_held)} />
            <Stat label="Duplicates" value={fmtNum(f.records_dup)} />
            <Stat label="Attempts" value={f.attempts} />
          </div>
          <Can role="operator">
            <div className="actions bar">
              {Object.entries(allowed).map(([a, from]) => (
                <button key={a} disabled={act.busy || !from.includes(f.status)} onClick={() => run(a)}>
                  {a}
                </button>
              ))}
            </div>
          </Can>
          <div className="grid2">
            <Card title="File">
              <dl className="kv">
                <dt>Stream</dt>
                <dd>{f.stream}</dd>
                <dt>Exchange</dt>
                <dd>{f.exchange_id}</dd>
                <dt>Path</dt>
                <dd className="mono">{f.path}</dd>
                <dt>Size</dt>
                <dd>{fmtBytes(f.size)}</dd>
                <dt>SHA-256</dt>
                <dd className="mono">{f.sha256}</dd>
                <dt>Received</dt>
                <dd>{fmtTime(f.received_at)}</dd>
                <dt>Committed</dt>
                <dd>{fmtTime(f.committed_at)}</dd>
                <dt>Release</dt>
                <dd className="mono">{f.release_id}</dd>
                {f.error && (
                  <>
                    <dt>Error</dt>
                    <dd>
                      {f.error_code}: {f.error}
                    </dd>
                  </>
                )}
              </dl>
            </Card>
            <Card title="History">
              <Table
                rows={d.data.events}
                rowKey={(e, i) => `${e.ts}${i}`}
                columns={[
                  { key: "ts", label: "Time", render: (e) => fmtTime(e.ts) },
                  { key: "status", label: "Status", render: (e) => <Badge value={e.status} /> },
                  { key: "actor", label: "By" },
                  { key: "detail", label: "Detail" },
                ]}
              />
            </Card>
          </div>
          <Card title="Statistics (stage / call type / action / reason)">
            <Table
              rows={d.data.stats}
              rowKey={(s, i) => `${s.stage}${s.reason}${i}`}
              columns={[
                { key: "stage", label: "Stage" },
                { key: "call_type", label: "Call type" },
                { key: "action", label: "Action" },
                { key: "reason", label: "Reason" },
                { key: "count", label: "Records", align: "right", render: (s) => fmtNum(s.count) },
                { key: "duration", label: "Duration (s)", align: "right" },
                { key: "volume", label: "Volume", align: "right" },
              ]}
            />
          </Card>
          <Card title="Output files">
            <Table
              rows={d.data.outputs}
              rowKey={(o) => o.id}
              columns={[
                { key: "route", label: "Route" },
                { key: "name", label: "Name" },
                { key: "records", label: "Records", align: "right", render: (o) => fmtNum(o.records) },
                { key: "bytes", label: "Size", align: "right", render: (o) => fmtBytes(o.bytes) },
                { key: "visible", label: "Visible", render: (o) => (o.visible ? "yes" : "pending") },
              ]}
            />
          </Card>
          <Card title="Rejected records (first 100)">
            <Table
              rows={d.data.errors}
              rowKey={(e) => `${e.seq}`}
              columns={[
                { key: "seq", label: "Seq", align: "right" },
                { key: "offset", label: "Offset", align: "right" },
                { key: "stage", label: "Stage" },
                { key: "reason", label: "Reason" },
                { key: "detail", label: "Detail", render: (e) => <code>{fmtDetail(e.detail)}</code> },
              ]}
            />
            <Link href={`/errors?file_id=${f.id}`}>All rejected records of this file</Link>
          </Card>
        </>
      )}
    </Page>
  );
}
