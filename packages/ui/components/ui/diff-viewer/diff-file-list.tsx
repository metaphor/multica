import { ChevronDown } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"

import { parseUnifiedDiff } from "./parse-unified-diff"
import type { DiffFile, DiffLine } from "./types"

export interface DiffFileListProps extends React.ComponentProps<"div"> {
  files: DiffFile[]
}

const statusLabels: Record<DiffFile["status"], string> = {
  added: "Added",
  modified: "Modified",
  deleted: "Deleted",
  renamed: "Renamed",
}

const statusVariants = {
  added: "default" as const,
  modified: "secondary" as const,
  deleted: "destructive" as const,
  renamed: "outline" as const,
}

export function DiffFileList({ files, className, ...props }: DiffFileListProps) {
  return (
    <div
      data-slot="diff-file-list"
      className={cn("flex flex-col gap-4", className)}
      {...props}
    >
      {files.map((file, index) => (
        <DiffFileCard key={diffFileKey(file, index)} file={file} />
      ))}
    </div>
  )
}

function DiffFileCard({ file }: { file: DiffFile }) {
  const isEmptyPatch =
    !file.collapsed && !file.tooLarge && file.patch.trim().length === 0

  return (
    <Card data-slot="diff-file-card">
      <Collapsible defaultOpen>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm font-medium">
            <Badge variant={statusVariants[file.status]}>
              {statusLabels[file.status]}
            </Badge>
            <span className="truncate">{diffFileTitle(file)}</span>
            {file.isGenerated && (
              <Badge variant="outline" className="ml-1">
                Generated
              </Badge>
            )}
          </CardTitle>
          <CardDescription className="sr-only">
            {file.oldPath} {file.status === "renamed" ? "to" : ""}{" "}
            {file.newPath}
          </CardDescription>
          <CardAction>
            <CollapsibleTrigger
              className="group inline-flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              aria-label="Toggle diff"
            >
              <ChevronDown className="size-4 transition-transform duration-200 group-data-[state=open]:rotate-180" />
            </CollapsibleTrigger>
          </CardAction>
        </CardHeader>
        {file.collapsed ? (
          <DiffFileNotice className="border-t">
            Diff collapsed by GitLab — expand on GitLab
          </DiffFileNotice>
        ) : file.tooLarge ? (
          <DiffFileNotice className="border-t">
            Diff too large to display
          </DiffFileNotice>
        ) : isEmptyPatch ? (
          <DiffFileNotice className="border-t">
            Binary file or empty diff
          </DiffFileNotice>
        ) : (
          <CollapsibleContent>
            <CardContent className="border-t border-surface-border px-0 py-0">
              <DiffLines patch={file.patch} />
            </CardContent>
          </CollapsibleContent>
        )}
      </Collapsible>
    </Card>
  )
}

function DiffFileNotice({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <CardContent
      className={cn(
        "border-surface-border px-4 py-6 text-center text-sm text-muted-foreground",
        className
      )}
    >
      {children}
    </CardContent>
  )
}

function DiffLines({ patch }: { patch: string }) {
  const lines = parseUnifiedDiff(patch)

  return (
    <div className="overflow-x-auto font-mono text-xs leading-5">
      {lines.map((line, index) => (
        <DiffLineRow key={index} line={line} />
      ))}
    </div>
  )
}

function DiffLineRow({ line }: { line: DiffLine }) {
  return (
    <div
      data-slot="diff-line"
      data-type={line.type}
      className={cn(
        "whitespace-pre px-4 py-0.5",
        line.type === "added" && "bg-success/10",
        line.type === "removed" && "bg-destructive/10",
        line.type === "hunk" && "bg-muted text-muted-foreground"
      )}
    >
      {line.content}
    </div>
  )
}

function diffFileTitle(file: DiffFile): string {
  if (file.status === "renamed") {
    return `${file.oldPath} → ${file.newPath}`
  }
  return file.newPath || file.oldPath
}

function diffFileKey(file: DiffFile, index: number): string {
  return `${file.oldPath}:${file.newPath}:${file.status}:${index}`
}
