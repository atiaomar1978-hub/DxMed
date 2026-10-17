"use client";

import { useEffect, useState } from "react";
import { Badge, ErrorBox, Field, Page, Table } from "@/components/ui";
import { base, qs } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { LogEntry } from "@/lib/types";

const MAX = 1000;

export default function LogsPage() {
  const [draft, setDraft] = useState({ level: "", component: "", stream: "", q: "" });
  const [filter, setFilter] = useState(draft);
  const [live, setLive] = useState(false);
  const [tail, setTail] = useState<LogEntry[]>([]);
  const query = qs(filter);
  const hist = useApi<LogEntry[]>(live ? null : `logs${qs({ ...filter, limit: 500 })}`);

  useEffect(() => {
    if (!live) return;
    setTail([]);
    const es = new EventSource(`${base}logs/stream${query}`);
    es.addEventListener("log", (ev) => {
      const e = JSON.parse((ev as MessageEvent).data) as LogEntry;
      setTail((t) => [e, ...t].slice(0, MAX));
    });
    return () => es.close();
  }, [live, query]);

  return (
    <Page
      title="Logs"
      actions={
        <label className="inline">
          <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} /> live tail
        </label>
      }
    >
      <form
        className="filters"
        onSubmit={(e) => {
          e.preventDefault();
          setFilter(draft);
        }}
      >
        <Field label="Level">
          <select value={draft.level} onChange={(e) => setDraft({ ...draft, level: e.target.value })}>
            {["", "DEBUG", "INFO", "WARN", "ERROR"].map((l) => (
              <option key={l} value={l}>
                {l || "any"}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Component">
          <input value={draft.component} onChange={(e) => setDraft({ ...draft, component: e.target.value })} placeholder="any" />
        </Field>
        <Field label="Stream">
          <input value={draft.stream} onChange={(e) => setDraft({ ...draft, stream: e.target.value })} placeholder="any" />
        </Field>
        <Field label="Text">
          <input value={draft.q} onChange={(e) => setDraft({ ...draft, q: e.target.value })} />
        </Field>
        <button className="primary">Apply</button>
      </form>
      <ErrorBox error={hist.error} />
      <Table
        rows={live ? tail : hist.data}
        rowKey={(e, i) => `${e.ts}${i}`}
        empty={live ? "Waiting for new entries…" : "No log entries"}
        columns={[
          { key: "ts", label: "Time", render: (e) => fmtTime(e.ts) },
          { key: "level", label: "Level", render: (e) => <Badge value={e.level} /> },
          { key: "component", label: "Component" },
          { key: "stream", label: "Stream" },
          { key: "msg", label: "Message" },
          { key: "attrs", label: "Attributes", render: (e) => (e.attrs ? <code className="clip">{JSON.stringify(e.attrs)}</code> : "") },
        ]}
      />
    </Page>
  );
}
