import { describe, expect, it } from "vitest"

import { parseUnifiedDiff } from "./parse-unified-diff"

describe("parseUnifiedDiff", () => {
  it("classifies a two-hunk patch correctly", () => {
    const patch = [
      "@@ -1,5 +1,5 @@",
      " context one",
      "-removed one",
      "+added one",
      " context two",
      "@@ -10,3 +10,3 @@",
      "-old line",
      "+new line",
      " context three",
    ].join("\n")

    const lines = parseUnifiedDiff(patch)

    expect(lines).toHaveLength(9)

    expect(lines[0]).toEqual({ type: "hunk", content: "@@ -1,5 +1,5 @@" })
    expect(lines[1]).toEqual({ type: "context", content: " context one" })
    expect(lines[2]).toEqual({ type: "removed", content: "-removed one" })
    expect(lines[3]).toEqual({ type: "added", content: "+added one" })
    expect(lines[4]).toEqual({ type: "context", content: " context two" })

    expect(lines[5]).toEqual({ type: "hunk", content: "@@ -10,3 +10,3 @@" })
    expect(lines[6]).toEqual({ type: "removed", content: "-old line" })
    expect(lines[7]).toEqual({ type: "added", content: "+new line" })
    expect(lines[8]).toEqual({ type: "context", content: " context three" })
  })

  it("returns context lines for a malformed patch without crashing", () => {
    const patch = "no hunk header\nplain context line\n+ looks like added?"

    const lines = parseUnifiedDiff(patch)

    expect(lines).toHaveLength(3)
    expect(lines.every((line) => line.type === "context")).toBe(true)
  })

  it("handles an empty patch", () => {
    expect(parseUnifiedDiff("")).toEqual([])
  })
})
