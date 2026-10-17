// Configuration kinds (internal/config Kinds) with a short description for the UI.
export const kinds: { kind: string; label: string; about: string }[] = [
  { kind: "stream", label: "Streams", about: "Source directory, decoding template, rules, routes and duplicate policy of a CDR flow." },
  { kind: "decoding_template", label: "Decoding templates", about: "ASN.1 BER / HEX / CSV layouts mapping tags or offsets to fields." },
  { kind: "field_dictionary", label: "Field dictionaries", about: "Field types and validation rules (mandatory, length, range, pattern)." },
  { kind: "rule_set", label: "Rule sets", about: "Readg-style staged filters: PRE, MAIN, COL, UNCOL with actions and routes." },
  { kind: "transform_group", label: "Transform groups", about: "Field enrichment and normalisation steps applied by rules." },
  { kind: "lookup_table", label: "Lookup tables", about: "Prefix, range and exact match reference data (home PLMN, MVNO ranges…)." },
  { kind: "merge_profile", label: "Merge profiles", about: "Partial record correlation keys, end condition and time-out." },
  { kind: "route", label: "Routes", about: "Output directory, file naming, rolling and post-actions per route code." },
  { kind: "output_template", label: "Output templates", about: "CSV, fixed width, JSON lines or BER encoders with header and trailer." },
];

export function kindLabel(kind: string): string {
  return kinds.find((k) => k.kind === kind)?.label ?? kind;
}

// nameField is the identifying property of a document (routes use "code").
export function nameField(kind: string): string {
  return kind === "route" ? "code" : "name";
}
