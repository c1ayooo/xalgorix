export function subjectKindLabel(t: (key: string) => string, kind: string): string {
  if (!kind) return "—";
  const key = `info.subjectKind.${kind}`;
  const label = t(key);
  return label === key ? kind : label;
}
