"use client";

import { ErrorBox, Page, Table } from "@/components/ui";
import { fmtTime } from "@/lib/format";
import { useApi } from "@/lib/hooks";
import type { Worker } from "@/lib/types";

export default function WorkersPage() {
  const ws = useApi<Worker[]>("workers", 15000);
  return (
    <Page title="Workers" actions={<button onClick={ws.reload}>Refresh</button>}>
      <p className="muted">Components report every 15 seconds; entries older than 10 minutes are hidden.</p>
      <ErrorBox error={ws.error} />
      <Table
        rows={ws.data}
        rowKey={(w) => w.id}
        empty="No component is running"
        columns={[
          { key: "component", label: "Component" },
          { key: "id", label: "Instance", render: (w) => <span className="mono">{w.id}</span> },
          { key: "host", label: "Host" },
          { key: "pid", label: "PID", align: "right" },
          { key: "started", label: "Started", render: (w) => fmtTime(w.started) },
          { key: "seen", label: "Last seen", render: (w) => fmtTime(w.seen) },
          { key: "version", label: "Version", render: (w) => w.info?.version ?? "" },
        ]}
      />
    </Page>
  );
}
