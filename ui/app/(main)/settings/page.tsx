"use client";

import { useState } from "react";
import { Can } from "@/components/Shell";
import { Card, ErrorBox, Field, Notice, Page, Table } from "@/components/ui";
import { api } from "@/lib/api";
import { useAction, useApi } from "@/lib/hooks";

export default function SettingsPage() {
  const s = useApi<Record<string, unknown>>("settings");
  const act = useAction();
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");

  const save = () =>
    act.run(async () => {
      let v: unknown;
      try {
        v = JSON.parse(value);
      } catch {
        throw new Error("value must be JSON (for example 30, \"text\" or {\"files\": 30})");
      }
      await api(`settings/${encodeURIComponent(key)}`, { method: "PUT", body: v });
      s.reload();
      return `Setting ${key} saved`;
    });

  const rows = Object.entries(s.data ?? {}).map(([k, v]) => ({ k, v }));
  return (
    <Page title="Settings">
      <ErrorBox error={s.error || act.error} />
      <Notice text={act.message} />
      <Table
        rows={s.data ? rows : undefined}
        rowKey={(r) => r.k}
        empty="No settings stored; built-in defaults apply"
        columns={[
          { key: "k", label: "Key" },
          { key: "v", label: "Value", render: (r) => <code>{JSON.stringify(r.v)}</code> },
          {
            key: "edit",
            label: "",
            render: (r) => (
              <Can role="admin">
                <button
                  className="link"
                  onClick={() => {
                    setKey(r.k);
                    setValue(JSON.stringify(r.v));
                  }}
                >
                  edit
                </button>
              </Can>
            ),
          },
        ]}
      />
      <Can role="admin">
        <Card title="Set a value">
          <div className="filters">
            <Field label="Key">
              <input value={key} onChange={(e) => setKey(e.target.value)} />
            </Field>
            <Field label="Value (JSON)">
              <input value={value} onChange={(e) => setValue(e.target.value)} />
            </Field>
            <button className="primary" disabled={!key || act.busy} onClick={save}>
              Save
            </button>
          </div>
        </Card>
      </Can>
    </Page>
  );
}
