export type DiffFileStatus = "added" | "modified" | "deleted" | "renamed"

export interface DiffFile {
  oldPath: string
  newPath: string
  status: DiffFileStatus
  patch: string
  isGenerated: boolean
  collapsed: boolean
  tooLarge: boolean
}

export type DiffLineType = "hunk" | "added" | "removed" | "context"

export interface DiffLine {
  type: DiffLineType
  content: string
}
