import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/react"

import { DiffFileList } from "./diff-file-list"
import type { DiffFile } from "./types"

const baseFile = {
  oldPath: "src/before.ts",
  newPath: "src/after.ts",
  status: "modified" as const,
  isGenerated: false,
  collapsed: false,
  tooLarge: false,
}

describe("DiffFileList", () => {
  it("renders the collapsed, too-large and empty-patch notices", () => {
    const files: DiffFile[] = [
      { ...baseFile, patch: "", collapsed: true },
      { ...baseFile, patch: "", tooLarge: true },
      {
        oldPath: "assets/icon.png",
        newPath: "assets/icon.png",
        status: "added",
        patch: "",
        isGenerated: false,
        collapsed: false,
        tooLarge: false,
      },
    ]

    render(<DiffFileList files={files} />)

    expect(document.body.textContent).toContain(
      "Diff collapsed by GitLab — expand on GitLab"
    )
    expect(document.body.textContent).toContain("Diff too large to display")
    expect(document.body.textContent).toContain("Binary file or empty diff")
  })

  it("renders status badges and generated flag", () => {
    const files: DiffFile[] = [
      {
        oldPath: "src/new.ts",
        newPath: "src/new.ts",
        status: "added",
        patch: "@@ -0,0 +1 @@\n+hello",
        isGenerated: true,
        collapsed: false,
        tooLarge: false,
      },
      {
        oldPath: "src/old.ts",
        newPath: "src/renamed.ts",
        status: "renamed",
        patch: "",
        isGenerated: false,
        collapsed: false,
        tooLarge: false,
      },
    ]

    render(<DiffFileList files={files} />)

    expect(screen.getByText("Added")).toBeTruthy()
    expect(screen.getByText("Generated")).toBeTruthy()
    expect(screen.getByText("Renamed")).toBeTruthy()
    expect(document.body.textContent).toContain(
      "src/old.ts → src/renamed.ts"
    )
  })
})
