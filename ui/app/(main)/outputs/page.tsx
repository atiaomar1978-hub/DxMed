"use client";

import Link from "next/link";
import { useState } from "react";
import { ErrorBox, Field, Page, Table } from "@/components/ui";
import { qs } from "@/lib/api";
import { fmtBytes, fmtNum, fmtTime } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { Output } from "@/lib/types";

export default function OutputsPage() {
  const [draft, setDraft] = useState({ stream: "", route: "" });
  const [filter, setFilter] = useState(draft);
  const out = useApi<Output[]>("outputs" + qs({ ...filter, limit: 200 }));
  return (
    <Page title="Output files" actions={<button onClick={out.reload}>Refresh</button>}>
      <form
        className="filters"
        onSubmit={(e) => {
          e.preventDefault();
          setFilter(draft);
        }}
      >
        <Field label="Stream">
          <input value={draft.stream} onChange={(e) => setDraft({ ...draft, stream: e.target.value })} placeholder="any" />
        </Field>
        <Field label="Route">
          <input value={draft.route} onChange={(e) => setDraft({ ...draft, route: e.target.value })} placeholder="any" />
        </Field>
        <button className="primary">Apply</button>
      </form>
      <ErrorBox error={out.error} />
      <Table
        rows={out.data}
        rowKey={(o) => o.id}
        empty="No output files"
        columns={[
          { key: "name", label: "Name" },
          { key: "stream", label: "Stream" },
          { key: "route", label: "Route" },
          { key: "records", label: "Records", align: "right", render: (o) => fmtNum(o.records) },
          { key: "bytes", label: "Size", align: "right", render: (o) => fmtBytes(o.bytes) },
          { key: "created", label: "Created", render: (o) => fmtTime(o.created_at) },
          { key: "path", label: "Path", render: (o) => <span className="mono">{o.path}</span> },
          { key: "file", label: "Input", render: (o) => (o.file_id ? <Link href={`/files/${o.file_id}`}>file</Link> : "flush") },
        ]}
      />
    </Page>
  );
}
