"use client";

import { useState } from "react";
import { Can } from "@/components/Shell";
import { ErrorBox, Field, Notice, Page, Table } from "@/components/ui";
import { api, qs } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import type { Partial } from "@/lib/types";

export default function PartialsPage() {
  const [stream, setStream] = useState("PGW_DATA");
  const ps = useApi<Partial[]>("partials" + qs({ stream, limit: 500 }));
  const [picked, setPicked] = useState<string[]>([]);
  const act = useAction();

  const close = (body: { keys?: string[]; all?: boolean }) =>
    act.run(async () => {
      const r = await api<{ closed: number }>("partials/close", { body: { stream, ...body } });
      setPicked([]);
      ps.reload();
      return `${r.closed} group(s) closed`;
    });

  const toggle = (k: string) => setPicked(picked.includes(k) ? picked.filter((x) => x !== k) : [...picked, k]);
  return (
    <Page title="Pending partial records" actions={<button onClick={ps.reload}>Refresh</button>}>
      <div className="filters">
        <Field label="Stream">
          <input value={stream} onChange={(e) => setStream(e.target.value)} />
        </Field>
        <Can role="operator">
          <button disabled={act.busy || picked.length === 0} onClick={() => close({ keys: picked })}>
            Close selected ({picked.length})
          </button>
          <button className="danger" disabled={act.busy || !ps.data?.length} onClick={() => close({ all: true })}>
            Close all
          </button>
        </Can>
      </div>
      <ErrorBox error={ps.error || act.error} />
      <Notice text={act.message} />
      <Table
        rows={ps.data}
        rowKey={(p) => p.key}
        empty="No pending partials"
        columns={[
          {
            key: "pick",
            label: "",
            render: (p) => <input type="checkbox" aria-label={`select ${p.key}`} checked={picked.includes(p.key)} onChange={() => toggle(p.key)} />,
          },
          { key: "key", label: "Group key", render: (p) => <span className="mono">{p.key}</span> },
          { key: "count", label: "Partials", align: "right" },
          { key: "first", label: "First seen", render: (p) => fmtTime(p.first_seen) },
          { key: "last", label: "Last seen", render: (p) => fmtTime(p.last_seen) },
        ]}
      />
    </Page>
  );
}
