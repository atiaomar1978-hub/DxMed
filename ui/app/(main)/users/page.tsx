"use client";

import { useState, type FormEvent } from "react";
import { Card, ErrorBox, Field, Notice, Page, Table } from "@/components/ui";
import { api } from "@/lib/api";
import { fmtTime, roles } from "@/lib/format";
import { useAction, useApi } from "@/lib/hooks";
import type { User } from "@/lib/types";

function RolePicker({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  return (
    <span className="chips">
      {roles.map((r) => (
        <label key={r} className="inline">
          <input
            type="checkbox"
            aria-label={r}
            checked={value.includes(r)}
            onChange={(e) => onChange(e.target.checked ? [...value, r] : value.filter((x) => x !== r))}
          />{" "}
          {r}
        </label>
      ))}
    </span>
  );
}

export default function UsersPage() {
  const us = useApi<User[]>("users");
  const act = useAction();
  const [form, setForm] = useState({ username: "", password: "", roles: ["viewer"] });

  const create = (e: FormEvent) => {
    e.preventDefault();
    act.run(async () => {
      await api("users", { body: form });
      setForm({ username: "", password: "", roles: ["viewer"] });
      us.reload();
      return `User ${form.username} created`;
    });
  };

  const update = (u: User, change: Partial<{ roles: string[]; disabled: boolean; password: string }>) =>
    act.run(async () => {
      await api(`users/${encodeURIComponent(u.username)}`, { method: "PUT", body: { roles: u.roles, disabled: u.disabled, ...change } });
      us.reload();
      return `User ${u.username} updated`;
    });

  const reset = (u: User) => {
    const pw = window.prompt(`New password for ${u.username} (12+ characters, 3 character classes)`);
    if (pw) update(u, { password: pw });
  };

  return (
    <Page title="Users">
      <ErrorBox error={us.error || act.error} />
      <Notice text={act.message} />
      <Table
        rows={us.data}
        rowKey={(u) => u.username}
        columns={[
          { key: "username", label: "User" },
          { key: "roles", label: "Roles", render: (u) => <RolePicker value={u.roles} onChange={(r) => update(u, { roles: r })} /> },
          {
            key: "disabled",
            label: "Enabled",
            render: (u) => (
              <input type="checkbox" aria-label={`enable ${u.username}`} checked={!u.disabled} onChange={(e) => update(u, { disabled: !e.target.checked })} />
            ),
          },
          { key: "last", label: "Last login", render: (u) => fmtTime(u.last_login) },
          {
            key: "pw",
            label: "",
            render: (u) => (
              <button className="link" onClick={() => reset(u)}>
                reset password
              </button>
            ),
          },
        ]}
      />
      <Card title="Add user">
        <form className="filters" onSubmit={create}>
          <Field label="User name">
            <input value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} required />
          </Field>
          <Field label="Initial password">
            <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} required />
          </Field>
          <RolePicker value={form.roles} onChange={(r) => setForm({ ...form, roles: r })} />
          <button className="primary" disabled={act.busy}>
            Create
          </button>
        </form>
      </Card>
    </Page>
  );
}
