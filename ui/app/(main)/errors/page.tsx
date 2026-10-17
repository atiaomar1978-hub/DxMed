"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { ErrorBox, Field, Page, Table } from "@/components/ui";
import { qs } from "@/lib/api";
import { fmtDetail } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { ErrorRecord } from "@/lib/types";

function ErrorsPageInner() {
  const sp = useSearchParams();
  const [draft, setDraft] = useState({ stream: "", reason: "", file_id: sp?.get("file_id") ?? "" });
  const [filter, setFilter] = useState(draft);
  const errs = useApi<ErrorRecord[]>("errors" + qs({ ...filter, limit: 500 }));
  return (
    <Page title="Rejected records" actions={<button onClick={errs.reload}>Refresh</button>}>
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
        <Field label="Reason">
          <input value={draft.reason} onChange={(e) => setDraft({ ...draft, reason: e.target.value })} placeholder="any" />
        </Field>
        <Field label="File id">
          <input value={draft.file_id} onChange={(e) => setDraft({ ...draft, file_id: e.target.value })} />
        </Field>
        <button className="primary">Apply</button>
      </form>
      <ErrorBox error={errs.error} />
      <Table
        rows={errs.data}
        rowKey={(e, i) => `${e.file_id}${e.seq}${i}`}
        empty="No rejected records"
        columns={[
          { key: "stream", label: "Stream" },
          { key: "file", label: "File", render: (e) => (e.file_id ? <Link href={`/files/${e.file_id}`}>open</Link> : "") },
          { key: "seq", label: "Seq", align: "right" },
          { key: "offset", label: "Offset", align: "right" },
          { key: "stage", label: "Stage" },
          { key: "reason", label: "Reason" },
          { key: "detail", label: "Detail", render: (e) => <code>{fmtDetail(e.detail)}</code> },
          { key: "raw", label: "Raw (hex)", render: (e) => <span className="mono clip">{e.raw_hex}</span> },
        ]}
      />
    </Page>
  );
}

export default function ErrorsPage() {
  return (
    <Suspense>
      <ErrorsPageInner />
    </Suspense>
  );
}
