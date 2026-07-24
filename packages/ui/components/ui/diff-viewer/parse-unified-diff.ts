import type { DiffLine, DiffLineType } from "./types"

export function parseUnifiedDiff(patch: string): DiffLine[] {
  if (!patch) return []

  const lines = patch.split(/\r?\n/)
  const hasHunk = lines.some((line) => line.startsWith("@@"))

  return lines.map((line): DiffLine => {
    if (!hasHunk) return { type: "context", content: line }
    const type = classifyDiffLine(line)
    return { type, content: line }
  })
}

function classifyDiffLine(line: string): DiffLineType {
  if (line.startsWith("@@")) return "hunk"
  if (line.startsWith("+")) return "added"
  if (line.startsWith("-")) return "removed"
  return "context"
}
