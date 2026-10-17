"use client";

import Link from "next/link";
import { Card, ErrorBox, Page } from "@/components/ui";
import { useApi } from "@/lib/hooks";
import { kinds } from "@/lib/kinds";

export default function ConfigPage() {
  const counts = useApi<Record<string, number>>("config");
  return (
    <Page title="Configuration objects">
      <p className="muted">
        Every object is versioned. Saving creates a new draft version (optimistic locking: a concurrent edit is refused); streams pick
        the changes up when a release is published.
      </p>
      <ErrorBox error={counts.error} />
      <div className="grid3">
        {kinds.map((k) => (
          <Card key={k.kind} title={k.label}>
            <p className="muted">{k.about}</p>
            <Link href={`/config/${k.kind}`}>{counts.data ? `${counts.data[k.kind] ?? 0} object(s)` : "open"}</Link>
          </Card>
        ))}
      </div>
    </Page>
  );
}
