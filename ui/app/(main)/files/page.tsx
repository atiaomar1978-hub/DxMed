"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Badge, ErrorBox, Field, Page, Table } from "@/components/ui";
import { qs } from "@/lib/api";
import { fmtNum, fmtTime } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { FilePage } from "@/lib/types";

const statuses = ["", "NEW", "LEASED", "COMMITTED", "POSTED", "FAILED", "HELD", "DEAD", "DUPLICATE", "CANCELLED"];

function FilesPageInner() {
  const sp = useSearchParams();
  const [filter, setFilter] = useState({ stream: sp?.get("stream") ?? "", status: sp?.get("status") ?? "", q: "" });
  const [draft, setDraft] = useState(filter);
  const [cursors, setCursors] = useState<string[]>([""]);
  const before = cursors[cursors.length - 1];
  const page = useApi<FilePage>("files" + qs({ ...filter, before, limit: 50 }), 15000);

  const apply = () => {
    setFilter(draft);
    setCursors([""]);
  };
  return (
    <Page title="Input files" actions={<button onClick={page.reload}>Refresh</button>}>
      <form
        className="filters"
        onSubmit={(e) => {
          e.preventDefault();
          apply();
        }}
      >
        <Field label="Stream">
          <input value={draft.stream} onChange={(e) => setDraft({ ...draft, stream: e.target.value })} placeholder="any" />
        </Field>
        <Field label="Status">
          <select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value })}>
            {statuses.map((s) => (
              <option key={s} value={s}>
                {s || "any"}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Name contains">
          <input value={draft.q} onChange={(e) => setDraft({ ...draft, q: e.target.value })} />
        </Field>
        <button className="primary">Apply</button>
      </form>
      <ErrorBox error={page.error} />
      <Table
        rows={page.data?.items}
        rowKey={(f) => f.id}
        empty="No files match"
        columns={[
          { key: "name", label: "File", render: (f) => <Link href={`/files/${f.id}`}>{f.name}</Link> },
          { key: "stream", label: "Stream" },
          { key: "status", label: "Status", render: (f) => <Badge value={f.status} /> },
          { key: "in", label: "In", align: "right", render: (f) => fmtNum(f.records_in) },
          { key: "out", label: "Out", align: "right", render: (f) => fmtNum(f.records_out) },
          { key: "err", label: "Err", align: "right", render: (f) => fmtNum(f.records_err) },
          { key: "dup", label: "Dup", align: "right", render: (f) => fmtNum(f.records_dup) },
          { key: "received", label: "Received", render: (f) => fmtTime(f.received_at) },
          { key: "error", label: "Error", render: (f) => f.error_code ?? "" },
        ]}
      />
      <div className="pager">
        <button disabled={cursors.length < 2} onClick={() => setCursors(cursors.slice(0, -1))}>
          Newer
        </button>
        <button disabled={!page.data?.next} onClick={() => setCursors([...cursors, page.data!.next])}>
          Older
        </button>
      </div>
    </Page>
  );
}

export default function FilesPage() {
  return (
    <Suspense>
      <FilesPageInner />
    </Suspense>
  );
}
