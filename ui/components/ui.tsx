"use client";

import type { ReactNode } from "react";
import { toneOf } from "@/lib/format";

export function Badge({ value }: { value: string }) {
  return <span className={`badge ${toneOf(value)}`}>{value}</span>;
}

export function ErrorBox({ error }: { error: string }) {
  return error ? (
    <div className="alert bad" role="alert">
      {error}
    </div>
  ) : null;
}

export function Notice({ text }: { text: string }) {
  return text ? (
    <div className="alert ok" role="status">
      {text}
    </div>
  ) : null;
}

export function Page({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <section>
      <header className="page-head">
        <h1>{title}</h1>
        <div className="actions">{actions}</div>
      </header>
      {children}
    </section>
  );
}

export function Card({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <div className="card">
      {title && <h2>{title}</h2>}
      {children}
    </div>
  );
}

export interface Column<T> {
  key: string;
  label: string;
  render?: (row: T) => ReactNode;
  align?: "right";
}

export function Table<T>({ rows, columns, empty = "Nothing to show", rowKey }: {
  rows: T[] | undefined;
  columns: Column<T>[];
  empty?: string;
  rowKey: (row: T, i: number) => string;
}) {
  if (!rows) return <p className="muted">Loading…</p>;
  if (rows.length === 0) return <p className="muted">{empty}</p>;
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} className={c.align}>
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={rowKey(r, i)}>
              {columns.map((c) => (
                <td key={c.key} className={c.align}>
                  {c.render ? c.render(r) : String((r as Record<string, unknown>)[c.key] ?? "")}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
    </label>
  );
}

export function Stat({ label, value, tone = "" }: { label: string; value: ReactNode; tone?: string }) {
  return (
    <div className={`stat ${tone}`}>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}

export function Json({ value }: { value: unknown }) {
  return <pre className="json">{JSON.stringify(value, null, 2)}</pre>;
}
