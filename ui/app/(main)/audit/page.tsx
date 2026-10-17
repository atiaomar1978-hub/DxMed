"use client";

import { useState } from "react";
import { ErrorBox, Field, Page, Table } from "@/components/ui";
import { qs } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { AuditEntry } from "@/lib/types";

export default function AuditPage() {
  const [draft, setDraft] = useState({ actor: "", action: "", kind: "" });
  const [filter, setFilter] = useState(draft);
  const as = useApi<AuditEntry[]>("audit" + qs({ ...filter, limit: 500 }));
  return (
    <Page title="Audit trail">
      <form
        className="filters"
        onSubmit={(e) => {
          e.preventDefault();
          setFilter(draft);
        }}
      >
        <Field label="Actor">
          <input value={draft.actor} onChange={(e) => setDraft({ ...draft, actor: e.target.value })} />
        </Field>
        <Field label="Action">
          <input value={draft.action} onChange={(e) => setDraft({ ...draft, action: e.target.value })} placeholder="e.g. publish" />
        </Field>
        <Field label="Object kind">
          <input value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value })} />
        </Field>
        <button className="primary">Apply</button>
      </form>
      <ErrorBox error={as.error} />
      <Table
        rows={as.data}
        rowKey={(a) => a.id}
        empty="No entries"
        columns={[
          { key: "ts", label: "Time", render: (a) => fmtTime(a.ts) },
          { key: "actor", label: "Actor" },
          { key: "action", label: "Action" },
          { key: "object", label: "Object", render: (a) => `${a.object_kind} ${a.object_name}` },
          { key: "after", label: "Details", render: (a) => (a.after ? <code className="clip">{JSON.stringify(a.after)}</code> : "") },
          { key: "ip", label: "Address" },
        ]}
      />
    </Page>
  );
}
