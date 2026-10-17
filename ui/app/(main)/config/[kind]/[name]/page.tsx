"use client";

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { useSession } from "@/components/Shell";
import { Card, ErrorBox, Notice, Page, Table } from "@/components/ui";
import { api, request } from "@/lib/api";
import { allows, fmtTime } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import { parseDoc } from "@/lib/doc";
import { kindLabel, nameField } from "@/lib/kinds";
import type { ConfigObject } from "@/lib/types";

export default function EditPage() {
  const params = useParams<{ kind: string; name: string }>();
  const kind = params.kind;
  const name = params.name === "_new" ? "" : decodeURIComponent(params.name);
  const router = useRouter();
  const canEdit = allows(useSession().roles, "configurator");
  const field = nameField(kind);
  const [text, setText] = useState("");
  const [etag, setEtag] = useState<string | null>(null);
  const [loadError, setLoadError] = useState("");
  const [tick, setTick] = useState(0);
  const versions = useApi<ConfigObject[]>(name ? `config/${kind}/${encodeURIComponent(name)}/versions` : null);
  const act = useAction();

  useEffect(() => {
    if (!name) {
      setText(JSON.stringify({ [field]: "" }, null, 2));
      return;
    }
    request<ConfigObject>(`config/${kind}/${encodeURIComponent(name)}`)
      .then((r) => {
        setText(JSON.stringify(r.data.doc, null, 2));
        setEtag(r.etag);
        setLoadError("");
      })
      .catch((e) => setLoadError(e.message));
  }, [kind, name, field, tick]);

  const save = () =>
    act.run(async () => {
      const p = parseDoc(text, field, name);
      if (p.error) throw new Error(p.error);
      const target = name || String(p.doc![field]);
      const r = await request<{ version: number }>(`config/${kind}/${encodeURIComponent(target)}`, {
        method: "PUT",
        body: p.doc,
        headers: etag ? { "if-match": etag } : {},
      });
      if (!name) {
        router.push(`/config/${kind}/${encodeURIComponent(target)}`);
        return "created";
      }
      setEtag(r.etag);
      versions.reload();
      return `Saved as version ${r.data.version}. Publish the stream to use it.`;
    });

  const remove = () =>
    act.run(async () => {
      if (!window.confirm(`Delete ${name}? Streams referring to it will fail validation.`)) return;
      await api(`config/${kind}/${encodeURIComponent(name)}`, { method: "DELETE", headers: { "if-match": etag ?? "" } });
      router.push(`/config/${kind}`);
    });

  const showVersion = (v: number) =>
    act.run(async () => {
      const o = await api<ConfigObject>(`config/${kind}/${encodeURIComponent(name)}/versions/${v}`);
      setText(JSON.stringify(o.doc ?? {}, null, 2));
      return `Loaded version ${v} into the editor; save to restore it.`;
    });

  return (
    <Page title={`${kindLabel(kind)}: ${name || "new"}`} actions={<Link href={`/config/${kind}`}>Back to list</Link>}>
      <ErrorBox error={loadError || act.error} />
      <Notice text={act.message} />
      <div className="grid-editor">
        <Card title="Document (JSON)">
          <textarea className="editor" aria-label="document" value={text} onChange={(e) => setText(e.target.value)} readOnly={!canEdit} spellCheck={false} />
          {canEdit && (
            <div className="actions bar">
              <button className="primary" disabled={act.busy} onClick={save}>
                Save draft
              </button>
              <button onClick={() => setTick(tick + 1)} disabled={!name}>
                Revert
              </button>
              {name && (
                <button className="danger" disabled={act.busy} onClick={remove}>
                  Delete
                </button>
              )}
            </div>
          )}
        </Card>
        {name && (
          <Card title="Versions">
            <Table
              rows={versions.data}
              rowKey={(v) => String(v.version)}
              columns={[
                { key: "version", label: "#", align: "right" },
                { key: "by", label: "By", render: (v) => v.updated_by },
                { key: "at", label: "At", render: (v) => fmtTime(v.updated_at) },
                {
                  key: "load",
                  label: "",
                  render: (v) => (
                    <button className="link" onClick={() => showVersion(v.version)}>
                      {v.doc === null || v.doc === undefined ? "deleted" : "load"}
                    </button>
                  ),
                },
              ]}
            />
          </Card>
        )}
      </div>
    </Page>
  );
}
