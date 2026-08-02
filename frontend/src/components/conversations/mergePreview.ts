export function normalizeMergePreview<T extends { summary?: unknown; points?: unknown }>(preview: T) {
  const summary = typeof preview.summary === "string"
    ? preview.summary
    : Array.isArray(preview.points)
      ? preview.points.join("\n\n")
      : "";
  return { ...preview, summary };
}
