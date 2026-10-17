"use client";

import { useState } from "react";
import { Can } from "@/components/Shell";
import { Badge, ErrorBox, Page, Table } from "@/components/ui";
import { api, qs } from "@/lib/api";
import { fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import type { Alarm } from "@/lib/types";

export default function AlarmsPage() {
  const [all, setAll] = useState(false);
  const as = useApi<Alarm[]>("alarms" + qs({ all }), 15000);
  const act = useAction();
  const ack = (id: string) =>
    act.run(async () => {
      await api(`alarms/${id}/ack`, { method: "POST" });
      as.reload();
    });
  return (
    <Page
      title="Alarms"
      actions={
        <label className="inline">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> include cleared
        </label>
      }
    >
      <ErrorBox error={as.error || act.error} />
      <Table
        rows={as.data}
        rowKey={(a) => a.id}
        empty="No alarms"
        columns={[
          { key: "severity", label: "Severity", render: (a) => <Badge value={a.severity} /> },
          { key: "rule", label: "Rule" },
          { key: "stream", label: "Stream" },
          { key: "message", label: "Message" },
          { key: "raised", label: "Raised", render: (a) => fmtTime(a.raised_at) },
          { key: "cleared", label: "Cleared", render: (a) => fmtTime(a.cleared_at) },
          {
            key: "ack",
            label: "Acknowledged",
            render: (a) =>
              a.acked_by ? (
                `${a.acked_by} ${fmtTime(a.acked_at)}`
              ) : (
                <Can role="operator">
                  <button disabled={act.busy} onClick={() => ack(a.id)}>
                    Acknowledge
                  </button>
                </Can>
              ),
          },
        ]}
      />
    </Page>
  );
}
