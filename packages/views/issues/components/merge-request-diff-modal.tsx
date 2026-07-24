"use client";

import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { DiffFileList } from "@multica/ui/components/ui/diff-viewer/diff-file-list";
import { useMergeRequestDiffs } from "@multica/core/github/queries";
import { ApiError } from "@multica/core/api";
import { useT } from "../../i18n";

interface MergeRequestDiffData {
  issueId?: string;
  prId?: string;
  title?: string;
}

export function MergeRequestDiffModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const d = (data ?? {}) as MergeRequestDiffData;
  const issueId = d.issueId ?? "";
  const prId = d.prId ?? "";
  const title = d.title ?? "";

  const { data: diffData, isLoading, error, refetch } = useMergeRequestDiffs(
    issueId,
    prId,
    true,
  );

  const errorMessage = getErrorMessage(error, t);

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent
        showCloseButton
        className="p-0 gap-0 flex flex-col overflow-hidden !top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2 !max-w-4xl !w-full !h-5/6"
      >
        <DialogHeader className="px-5 pt-5 pb-3 shrink-0">
          <DialogTitle className="truncate">{t(($) => $.merge_request_diff.title, { title })}</DialogTitle>
          <DialogDescription className="sr-only">
            {t(($) => $.merge_request_diff.title, { title })}
          </DialogDescription>
        </DialogHeader>

        <div className="flex-1 min-h-0 overflow-y-auto px-5 pb-5">
          {isLoading ? (
            <DiffLoadingSkeleton />
          ) : errorMessage ? (
            <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
              <p className="text-sm text-muted-foreground">{errorMessage}</p>
              {(!error || !(error instanceof ApiError && (error.status === 400 || error.status === 409))) ? (
                <Button variant="outline" size="sm" onClick={() => refetch()}>
                  {t(($) => $.merge_request_diff.retry)}
                </Button>
              ) : null}
            </div>
          ) : diffData?.files.length === 0 ? (
            <p className="py-12 text-center text-sm text-muted-foreground">
              {t(($) => $.merge_request_diff.empty)}
            </p>
          ) : (
            <DiffFileList files={diffData?.files ?? []} />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function DiffLoadingSkeleton() {
  return (
    <div className="flex flex-col gap-4 py-1">
      <Skeleton className="h-24 w-full" />
      <Skeleton className="h-32 w-full" />
      <Skeleton className="h-24 w-full" />
    </div>
  );
}

function getErrorMessage(
  error: unknown,
  t: ReturnType<typeof useT<"modals">>["t"],
): string | null {
  if (!error) return null;
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return t(($) => $.merge_request_diff.error_409);
    }
    if (error.status === 400) {
      return t(($) => $.merge_request_diff.error_400);
    }
  }
  return t(($) => $.merge_request_diff.error_generic);
}
