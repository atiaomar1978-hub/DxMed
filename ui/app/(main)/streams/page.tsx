"use client";

import Link from "next/link";
import { useState } from "react";
import { Can, useSession } from "@/components/Shell";
import { Badge, Card, ErrorBox, Field, Notice, Page, Table } from "@/components/ui";
import { api, ApiError } from "@/lib/api";
import { allows, fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import type { Problem, PublishResult, Release, StreamInfo } from "@/lib/types";

function Problems({ list }: { list: Problem[] }) {
  return (
    <ul className="problems">
      {list.map((p, i) => (
        <li key={i} className={p.severity}>
          <b>{p.severity}</b> <span className="mono">{p.path}</span> {p.message}
        </li>
      ))}
    </ul>
  );
}

function StreamPanel({ s, onChange }: { s: StreamInfo; onChange: () => void }) {
  const session = useSession();
  const rel = useApi<Release[]>(`streams/${s.name}/releases`);
  const act = useAction();
  const [note, setNote] = useState("");
  const [force, setForce] = useState(false);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [gate, setGate] = useState<Record<string, string[]>>({});

  const validate = () =>
    act.run(async () => {
      const r = await api<{ ok: boolean; problems: Problem[] | null }>(`streams/${s.name}/validate`, { method: "POST" });
      setProblems(r.problems ?? []);
      return r.ok ? "Drafts are valid" : "Drafts have errors";
    });

  const publish = () =>
    act.run(async () => {
      setGate({});
      try {
        const r = await api<PublishResult>(`streams/${s.name}/publish`, { body: { note, force } });
        setProblems(r.problems ?? []);
        rel.reload();
        onChange();
        return r.unchanged ? `No change: release #${r.release!.number} stays active` : `Published release #${r.release!.number}`;
      } catch (e) {
        const d = e instanceof ApiError ? (e.details as PublishResult | undefined) : undefined;
        if (d) {
          setProblems(d.problems ?? []);
          setGate(d.gate ?? {});
        }
        throw e;
      }
    });

  const activate = (r: Release) =>
    act.run(async () => {
      await api(`releases/${r.id}/activate`, { method: "POST" });
      rel.reload();
      onChange();
      return `Release #${r.number} is active`;
    });

  return (
    <Card title={s.name}>
      <p className="muted">{String(s.doc.description ?? "")}</p>
      <p>
        Active release: {s.active ? <b>#{s.active.number}</b> : <i>none</i>} · queued {s.queue} · pending partials {s.partials} ·{" "}
        <Link href={`/config/stream/${s.name}`}>edit stream</Link>
      </p>
      <ErrorBox error={act.error} />
      <Notice text={act.message} />
      {problems.length > 0 && <Problems list={problems} />}
      {Object.keys(gate).length > 0 && (
        <ul className="problems">
          {Object.entries(gate).map(([c, fails]) => (
            <li key={c} className="error">
              lab case <b>{c}</b>: {fails.join("; ")}
            </li>
          ))}
        </ul>
      )}
      <div className="actions bar">
        <Can role="configurator">
          <button disabled={act.busy} onClick={validate}>
            Validate drafts
          </button>
        </Can>
        <Can role="publisher">
          <Field label="Release note">
            <input value={note} onChange={(e) => setNote(e.target.value)} />
          </Field>
          {allows(session.roles, "admin") && (
            <label className="inline">
              <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} /> ignore failing lab cases
            </label>
          )}
          <button className="primary" disabled={act.busy} onClick={publish}>
            Publish
          </button>
        </Can>
      </div>
      <Table
        rows={rel.data}
        rowKey={(r) => r.id}
        empty="No releases"
        columns={[
          { key: "number", label: "#", align: "right" },
          { key: "active", label: "State", render: (r) => (r.active ? <Badge value="ACTIVE" /> : "") },
          { key: "published", label: "Published", render: (r) => `${fmtTime(r.published_at)} by ${r.published_by}` },
          { key: "note", label: "Note" },
          { key: "sha", label: "Bundle hash", render: (r) => <span className="mono">{r.sha256.slice(0, 12)}</span> },
          {
            key: "act",
            label: "",
            render: (r) =>
              r.active ? null : (
                <Can role="publisher">
                  <button disabled={act.busy} onClick={() => activate(r)}>
                    Activate (rollback)
                  </button>
                </Can>
              ),
          },
        ]}
      />
    </Card>
  );
}

export default function StreamsPage() {
  const ss = useApi<StreamInfo[]>("streams");
  return (
    <Page title="Streams & releases" actions={<button onClick={ss.reload}>Refresh</button>}>
      <p className="muted">
        A stream release is an immutable bundle of every configuration object the stream uses. Editing objects changes drafts only;
        publishing validates the drafts, runs the saved Template Lab cases and activates the new bundle.
      </p>
      <ErrorBox error={ss.error} />
      {ss.data?.map((s) => <StreamPanel key={s.name} s={s} onChange={ss.reload} />)}
    </Page>
  );
}
