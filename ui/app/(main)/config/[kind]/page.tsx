"use client";

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useState } from "react";
import { Can } from "@/components/Shell";
import { ErrorBox, Field, Page, Table } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import { kindLabel } from "@/lib/kinds";
import type { ConfigObject } from "@/lib/types";

export default function KindPage() {
  const { kind } = useParams<{ kind: string }>();
  const router = useRouter();
  const list = useApi<ConfigObject[]>(`config/${kind}`);
  const [from, setFrom] = useState("");
  const [name, setName] = useState("");
  const act = useAction();

  const clone = () =>
    act.run(async () => {
      await api(`config/${kind}/${encodeURIComponent(from)}/clone`, { body: { name } });
      router.push(`/config/${kind}/${encodeURIComponent(name)}`);
    });

  return (
    <Page
      title={kindLabel(kind)}
      actions={
        <Can role="configurator">
          <Link href={`/config/${kind}/_new`} className="button">
            New
          </Link>
        </Can>
      }
    >
      <ErrorBox error={list.error || act.error} />
      <Table
        rows={list.data}
        rowKey={(o) => o.name}
        empty="No objects of this kind"
        columns={[
          { key: "name", label: "Name", render: (o) => <Link href={`/config/${kind}/${encodeURIComponent(o.name)}`}>{o.name}</Link> },
          { key: "version", label: "Version", align: "right" },
          { key: "desc", label: "Description", render: (o) => String(o.doc?.description ?? o.doc?.name ?? "") },
          { key: "updated", label: "Updated", render: (o) => `${fmtTime(o.updated_at)} by ${o.updated_by}` },
        ]}
      />
      <Can role="configurator">
        <form
          className="filters"
          onSubmit={(e) => {
            e.preventDefault();
            clone();
          }}
        >
          <Field label="Copy from (template)">
            <select value={from} onChange={(e) => setFrom(e.target.value)} required>
              <option value="">choose…</option>
              {list.data?.map((o) => (
                <option key={o.name} value={o.name}>
                  {o.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="New name">
            <input value={name} onChange={(e) => setName(e.target.value)} required pattern="[A-Za-z0-9._\-]{1,64}" />
          </Field>
          <button className="primary" disabled={act.busy}>
            Create from template
          </button>
        </form>
      </Can>
    </Page>
  );
}
