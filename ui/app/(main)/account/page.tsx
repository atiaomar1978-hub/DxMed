"use client";

import { useState, type FormEvent } from "react";
import { useSession } from "@/components/Shell";
import { Card, ErrorBox, Field, Page } from "@/components/ui";
import { api } from "@/lib/api";
import { useAction } from "@/lib/hooks";

export default function AccountPage() {
  const me = useSession();
  const act = useAction();
  const [form, setForm] = useState({ current: "", next: "", again: "" });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    act.run(async () => {
      if (form.next !== form.again) throw new Error("the new passwords differ");
      await api("auth/password", { body: { current: form.current, new: form.next } });
      window.location.assign("/login");
    });
  };

  return (
    <Page title="My account">
      <Card>
        <p>
          Signed in as <b>{me.username}</b> with roles {me.roles.join(", ")}.
        </p>
      </Card>
      <Card title="Change password">
        <ErrorBox error={act.error} />
        <form className="stack" onSubmit={submit}>
          <Field label="Current password">
            <input type="password" value={form.current} onChange={(e) => setForm({ ...form, current: e.target.value })} required />
          </Field>
          <Field label="New password (12+ characters, 3 character classes)">
            <input type="password" value={form.next} onChange={(e) => setForm({ ...form, next: e.target.value })} required />
          </Field>
          <Field label="Repeat new password">
            <input type="password" value={form.again} onChange={(e) => setForm({ ...form, again: e.target.value })} required />
          </Field>
          <button className="primary" disabled={act.busy}>
            Change password (signs you out)
          </button>
        </form>
      </Card>
    </Page>
  );
}
