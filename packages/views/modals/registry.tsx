"use client";

import { useState, type ReactNode } from "react";
import { useModalStore } from "@multica/core/modals";
import { CreateIssueDialog } from "./create-issue-dialog";
import { CreateProjectModal } from "./create-project";
import { CreateSquadModal } from "./create-squad";
import { FeedbackModal } from "./feedback";
import { SetParentIssueModal } from "./set-parent-issue";
import { MarkDuplicateIssueModal } from "./mark-duplicate-issue";
import { AddChildIssueModal } from "./add-child-issue";
import { AddBlockerIssueModal } from "./add-blocker-issue";
import { AddProjectIssueModal } from "./add-project-issue";
import { RenameIssueModal } from "./rename-issue";
import { RenameProjectModal } from "./rename-project";
import { getProjectFoldersExtension } from "../extensions";
import { DeleteIssueConfirmModal } from "./delete-issue-confirm";
import { MergeRequestDiffModal } from "../issues/components/merge-request-diff-modal";
import { RunConfirmModal } from "./run-confirm";
import { WorkflowHandoffModal } from "./workflow-handoff";
import { LimitRecoveryDialog } from "./limit-recovery-dialog";

type ActiveModalName = NonNullable<ReturnType<typeof useModalStore.getState>["modal"]>;

type RetainedModal = {
  modal: ActiveModalName;
  data: Record<string, unknown> | null;
  sessionKey: number;
  closing: boolean;
};

export function ModalRegistry() {
  const modal = useModalStore((s) => s.modal);
  const data = useModalStore((s) => s.data);
  const close = useModalStore((s) => s.close);
  const [retained, setRetained] = useState<RetainedModal | null>(null);

  if (modal !== null) {
    if (
      retained === null ||
      retained.closing ||
      retained.modal !== modal ||
      retained.data !== data
    ) {
      setRetained({
        modal,
        data,
        sessionKey: (retained?.sessionKey ?? 0) + 1,
        closing: false,
      });
    }
  } else if (retained !== null && !retained.closing) {
    setRetained({ ...retained, closing: true });
  }

  const snapshot: RetainedModal | null =
    modal !== null
      ? retained !== null &&
        !retained.closing &&
        retained.modal === modal &&
        retained.data === data
        ? retained
        : {
            modal,
            data,
            sessionKey: (retained?.sessionKey ?? 0) + 1,
            closing: false,
          }
      : retained;

  const isOpen = modal !== null;

  let activeModal: ReactNode = null;
  switch (modal) {
    // Both modal types open the same shell so the in-modal mode switch is
    // instant — only the inner panel swaps, the Dialog Root stays mounted.
    case "create-issue":
      activeModal = (
        <CreateIssueDialog
          onClose={close}
          initialMode="manual"
          data={data}
        />
      );
      break;
    case "quick-create-issue":
      activeModal = (
        <CreateIssueDialog
          onClose={close}
          initialMode="agent"
          data={data}
        />
      );
      break;
    case "create-project":
      activeModal = <CreateProjectModal onClose={close} />;
      break;
    case "create-squad":
      activeModal = <CreateSquadModal onClose={close} />;
      break;
    case "feedback":
      activeModal = <FeedbackModal onClose={close} data={data} />;
      break;
    case "issue-set-parent":
      activeModal = <SetParentIssueModal onClose={close} data={data} />;
      break;
    case "issue-mark-duplicate":
      activeModal = <MarkDuplicateIssueModal onClose={close} data={data} />;
      break;
    case "issue-add-child":
      activeModal = <AddChildIssueModal onClose={close} data={data} />;
      break;
    case "project-add-issue":
      activeModal = <AddProjectIssueModal onClose={close} data={data} />;
      break;
    case "issue-rename":
      activeModal = <RenameIssueModal onClose={close} data={data} />;
      break;
    case "issue-delete-confirm":
      activeModal = <DeleteIssueConfirmModal onClose={close} data={data} />;
      break;
    case "issue-run-confirm":
      activeModal = <RunConfirmModal onClose={close} data={data} />;
      break;
    case "merge-request-diff":
      activeModal = <MergeRequestDiffModal onClose={close} data={data} />;
      break;
    case "issue-workflow-handoff":
      activeModal = <WorkflowHandoffModal onClose={close} data={data} />;
      break;
  }

  const FoldersHost = getProjectFoldersExtension()?.Host;

  return (
    <>
      {activeModal}
      <LimitRecoveryDialog />
      {FoldersHost && <FoldersHost />}
    </>
  );
}
