"use client";

import { useState } from "react";
import { Badge, Card, ErrorBox, Field, Json, Notice, Page, Stat, Table } from "@/components/ui";
import { api, qs, toBase64 } from "@/lib/api";
import { fmtNum, fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import type { LabCase, LabReport, StreamInfo } from "@/lib/types";

export default function LabPage() {
  const streams = useApi<StreamInfo[]>("streams");
  const [picked, setStream] = useState("");
  const stream = picked || streams.data?.[0]?.name || "";
  const [file, setFile] = useState<{ name: string; data: string } | null>(null);
  const [useActive, setUseActive] = useState(false);
  const [flush, setFlush] = useState(true);
  const [caseName, setCaseName] = useState("");
  const [report, setReport] = useState<LabReport | null>(null);
  const cases = useApi<LabCase[]>(stream ? "lab/cases" + qs({ stream }) : null);
  const act = useAction();
  const active = streams.data?.find((s) => s.name === stream)?.active;

  const pick = async (f: File | undefined) => {
    if (!f) return;
    setFile({ name: f.name, data: toBase64(new Uint8Array(await f.arrayBuffer())) });
  };

  const input = () => ({ stream, file_name: file!.name, data: file!.data, flush, samples: 20, release: useActive && active ? active.id : "" });

  const run = () =>
    act.run(async () => {
      const r = await api<LabReport>("lab/run", { body: input() });
      setReport(r);
      return r.balanced ? "Run complete: counts reconcile" : "Run complete: counts do NOT reconcile";
    });

  const save = () =>
    act.run(async () => {
      await api("lab/cases", { method: "PUT", body: { ...input(), name: caseName, release: "" } });
      cases.reload();
      return `Case ${caseName} saved; it now gates every publish of ${stream}.`;
    });

  const remove = (c: LabCase) =>
    act.run(async () => {
      await api(`lab/cases/${c.stream}/${encodeURIComponent(c.name)}`, { method: "DELETE" });
      cases.reload();
    });

  const c = report?.counts;
  return (
    <Page title="Template Lab">
      <p className="muted">
        Run a sample file through the current drafts (or the active release) without touching production: decoding, validation, rules,
        routing and output previews. Saved cases become regression gates for publishing.
      </p>
      <Card>
        <div className="filters">
          <Field label="Stream">
            <select value={stream} onChange={(e) => setStream(e.target.value)}>
              {(streams.data ?? []).map((s) => (
                <option key={s.name}>{s.name}</option>
              ))}
            </select>
          </Field>
          <Field label="Sample file">
            <input type="file" aria-label="sample file" onChange={(e) => pick(e.target.files?.[0])} />
          </Field>
          <label className="inline">
            <input type="checkbox" checked={useActive} onChange={(e) => setUseActive(e.target.checked)} /> active release instead of drafts
          </label>
          <label className="inline">
            <input type="checkbox" checked={flush} onChange={(e) => setFlush(e.target.checked)} /> close open partials at the end
          </label>
          <button className="primary" disabled={!file || act.busy} onClick={run}>
            Run
          </button>
        </div>
        <ErrorBox error={streams.error || act.error} />
        <Notice text={act.message} />
      </Card>

      {report && c && (
        <>
          {report.problems && report.problems.length > 0 && <Json value={report.problems} />}
          <div className="stats">
            <Stat label="In" value={fmtNum(c.in)} />
            <Stat label="Out" value={fmtNum(c.out)} tone="ok" />
            <Stat label="Rejected" value={fmtNum(c.err)} />
            <Stat label="Dropped" value={fmtNum(c.drop)} />
            <Stat label="Held" value={fmtNum(c.held)} />
            <Stat label="Duplicates" value={fmtNum(c.dup)} />
            <Stat label="Merged" value={fmtNum(c.merged)} />
            <Stat label="Reconciled" value={<Badge value={report.balanced ? "COMMITTED" : "ERROR"} />} />
          </div>
          <Card title="Records per route">
            <Table
              rows={Object.entries(report.routes ?? {}).map(([route, n]) => ({ route, n }))}
              rowKey={(r) => r.route}
              columns={[
                { key: "route", label: "Route" },
                { key: "n", label: "Records", align: "right" },
              ]}
            />
          </Card>
          <Card title="Sample records">
            <Table
              rows={report.samples ?? []}
              rowKey={(s) => String(s.seq)}
              columns={[
                { key: "seq", label: "Seq", align: "right" },
                { key: "record_type", label: "Type" },
                { key: "call_type", label: "Call type" },
                { key: "stage", label: "Stage" },
                { key: "action", label: "Action" },
                { key: "reason", label: "Reason" },
                { key: "routes", label: "Routes", render: (s) => (s.routes ?? []).join(", ") },
                { key: "fields", label: "Fields", render: (s) => <code className="clip">{JSON.stringify(s.fields)}</code> },
              ]}
            />
          </Card>
          {report.outputs?.map((o) => (
            <Card key={o.route} title={`Output preview: ${o.route} (${o.records} records${o.truncated ? ", truncated" : ""})`}>
              <pre className="json">{o.binary ? o.hex : o.text}</pre>
            </Card>
          ))}
          <Card title="Save as regression case">
            <div className="filters">
              <Field label="Case name">
                <input value={caseName} onChange={(e) => setCaseName(e.target.value)} />
              </Field>
              <button disabled={!caseName || act.busy} onClick={save}>
                Save case (expect the drafts&apos; result)
              </button>
            </div>
          </Card>
        </>
      )}

      {stream && (
        <Card title={`Saved cases of ${stream}`}>
          <Table
            rows={cases.data}
            rowKey={(x) => x.name}
            empty="No cases yet"
            columns={[
              { key: "name", label: "Case" },
              { key: "file_name", label: "File" },
              { key: "exp", label: "Expected", render: (x) => <code>{JSON.stringify(x.expected.counts)}</code> },
              { key: "upd", label: "Updated", render: (x) => `${fmtTime(x.updated_at)} by ${x.updated_by}` },
              {
                key: "del",
                label: "",
                render: (x) => (
                  <button className="link" onClick={() => remove(x)}>
                    delete
                  </button>
                ),
              },
            ]}
          />
        </Card>
      )}
    </Page>
  );
}
