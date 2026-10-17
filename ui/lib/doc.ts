// parseDoc checks the editor text; it returns the document or an error message.
export function parseDoc(text: string, field: string, name: string): { doc?: Record<string, unknown>; error?: string } {
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch (e) {
    return { error: `JSON: ${(e as Error).message}` };
  }
  if (!doc || typeof doc !== "object" || Array.isArray(doc)) return { error: "the document must be a JSON object" };
  const d = doc as Record<string, unknown>;
  if (name && d[field] !== name) return { error: `"${field}" must stay "${name}" (use "Create from template" to copy)` };
  if (!name && !d[field]) return { error: `"${field}" is required` };
  return { doc: d };
}
