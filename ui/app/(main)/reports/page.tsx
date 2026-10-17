"use client";

import { useState } from "react";
import { Card, ErrorBox, Field, Page, Table, type Column } from "@/components/ui";
import { base, qs } from "@/lib/api";
import { dayInput, fmtBytes, fmtNum } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { RouteRow, StatRow, VolumeRow } from "@/lib/types";

const day = (r: { day: string }) => r.day.slice(0, 10);
const n = (k: string) => (r: object) => fmtNum((r as Record<string, number>)[k]);

const reports = {
  volume: {
    title: "Volume and reconciliation per day",
    columns: [
      { key: "day", label: "Day", render: day },
      { key: "stream", label: "Stream" },
      ...["files", "in", "out", "err", "drop", "held", "dup"].map((k) => ({ key: k, label: k, align: "right" as const, render: n(k) })),
      {
        key: "bal",
        label: "Balanced",
        render: (r: VolumeRow) => (r.in === r.out + r.err + r.drop + r.held + r.dup ? "yes" : "NO"),
      },
    ] as Column<VolumeRow>[],
  },
  routes: {
    title: "Output per route",
    columns: [
      { key: "day", label: "Day", render: day },
      { key: "stream", label: "Stream" },
      { key: "route", label: "Route" },
      { key: "files", label: "Files", align: "right", render: n("files") },
      { key: "records", label: "Records", align: "right", render: n("records") },
      { key: "bytes", label: "Size", align: "right", render: (r: RouteRow) => fmtBytes(r.bytes) },
    ] as Column<RouteRow>[],
  },
  reasons: {
    title: "Records by stage, action and reason",
    columns: [
      { key: "stream", label: "Stream" },
      { key: "stage", label: "Stage" },
      { key: "call_type", label: "Call type" },
      { key: "action", label: "Action" },
      { key: "reason", label: "Reason" },
      { key: "count", label: "Records", align: "right", render: n("count") },
      { key: "duration", label: "Duration (s)", align: "right" },
      { key: "volume", label: "Volume", align: "right" },
    ] as Column<StatRow>[],
  },
};

type Name = keyof typeof reports;

export default function ReportsPage() {
  const [name, setName] = useState<Name>("volume");
  const [draft, setDraft] = useState({ stream: "", from: dayInput(-7), to: dayInput(0) });
  const [filter, setFilter] = useState(draft);
  const query = qs({ stream: filter.stream, from: filter.from, to: filter.to ? `${filter.to}T23:59` : "" });
  const data = useApi<object[]>(`reports/${name}${query}`);
  const r = reports[name];
  return (
    <Page
      title="Reports"
      actions={
        <a className="button" href={`${base}reports/${name}${query}${query ? "&" : "?"}format=csv`} download>
          Download CSV
        </a>
      }
    >
      <div className="tabs" role="tablist">
        {(Object.keys(reports) as Name[]).map((k) => (
          <button key={k} role="tab" aria-selected={k === name} className={k === name ? "active" : ""} onClick={() => setName(k)}>
            {k}
          </button>
        ))}
      </div>
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
        <Field label="From">
          <input type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </Field>
        <Field label="To">
          <input type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </Field>
        <button className="primary">Apply</button>
      </form>
      <ErrorBox error={data.error} />
      <Card title={r.title}>
        <Table rows={data.data} rowKey={(_, i) => String(i)} columns={r.columns as Column<object>[]} empty="No data in the period" />
      </Card>
    </Page>
  );
}
