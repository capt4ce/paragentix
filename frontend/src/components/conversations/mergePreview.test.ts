import { describe, expect, it } from "vitest";
import { normalizeMergePreview } from "./mergePreview";

describe("normalizeMergePreview", () => {
  it("joins legacy points into a non-empty summary", () => {
    expect(normalizeMergePreview({ points: ["First", "Second"] }).summary).toBe("First\n\nSecond");
  });

  it("prefers the current summary", () => {
    expect(normalizeMergePreview({ summary: "Current", points: ["Legacy"] }).summary).toBe("Current");
  });
});
