import { describe, expect, it, afterEach } from "vitest";
import { useModalStore } from "./store";

describe("useModalStore", () => {
  afterEach(() => {
    useModalStore.getState().close();
  });

  it("opens merge-request-diff modal with params", () => {
    useModalStore.getState().open("merge-request-diff", {
      issueId: "issue-1",
      prId: "pr-1",
      title: "Add feature",
    });
    expect(useModalStore.getState().modal).toBe("merge-request-diff");
    expect(useModalStore.getState().data).toEqual({
      issueId: "issue-1",
      prId: "pr-1",
      title: "Add feature",
    });
  });
});
