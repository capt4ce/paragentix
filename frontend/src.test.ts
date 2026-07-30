// @vitest-environment jsdom
import { afterEach, describe, it, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { createElement, useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { api, App, AsyncButton, boardLocation, canComment, clearJobDraft, closeDetails, columnAnchor, columnPatch, ConversationBranchTree, ConversationBubble, conversationEventsBelongTo, conversationLocation, conversationReplyRequest, CreateBranchesDialog, DoneDefinitionField, eventSide, filterProjectJobs, initialConversationSelection, invitationEmailValid, invitationSessionAction, InvitationDialog, isConversationEvent, JobConversationProgress, JobTimeline, jobActionsVisible, jobColumn, jobCreationRequest, jobDraftKey, JobCard, JobDetailMeta, loadJobDraft, mergeNotifications, MergeReviewDialog, moveColumn, NotificationCenter, parseLocation, projectLocation, replyRequest, runWithToast, saveJobDraft, DialogShell, TimelineContent, Toast, useJobDetailHistory, validateAttachments, WorkspaceUserStatus } from "./src";
import { cn } from "./src/lib/utils";
import { StatusBadge } from "./src/components/jobs/StatusBadge";
import { submitFormShortcut } from "./src/lib/forms";
afterEach(cleanup);
describe("form submit shortcut", () => {
  it.each([{ ctrlKey: true }, { metaKey: true }])("submits for Ctrl/Cmd+Enter", modifier => {
    const submit = vi.fn((event: Event) => event.preventDefault());
    const screen = render(createElement("form", { onSubmit: submit, onKeyDown: submitFormShortcut }, createElement("textarea", { "aria-label": "Task" })));
    fireEvent.keyDown(screen.getByLabelText("Task"), { key: "Enter", ...modifier });
    expect(submit).toHaveBeenCalledOnce();
  });
  it("does not submit for plain or Shift+Enter", () => {
    const submit = vi.fn((event: Event) => event.preventDefault());
    const screen = render(createElement("form", { onSubmit: submit, onKeyDown: submitFormShortcut }, createElement("textarea", { "aria-label": "Task" })));
    fireEvent.keyDown(screen.getByLabelText("Task"), { key: "Enter" });
    fireEvent.keyDown(screen.getByLabelText("Task"), { key: "Enter", shiftKey: true });
    fireEvent.keyDown(screen.getByLabelText("Task"), { key: "Enter", ctrlKey: true, shiftKey: true });
    fireEvent.keyDown(screen.getByLabelText("Task"), { key: "Enter", metaKey: true, altKey: true });
    expect(submit).not.toHaveBeenCalled();
  });
});
describe("Mission Control foundation", () => {
  it("uses the accessible Radix dialog inspector", () => {
    const { getByRole, getByLabelText } = render(createElement(DialogShell, { title: "Inspector", close: vi.fn(), inspector: true }, "detail"));
    expect(getByRole("dialog").classList.contains("inspector")).toBe(true);
    expect(getByLabelText("Close")).toBeTruthy();
  });
  it("merges utility classes", () => expect(cn("a", false && "b", "c")).toBe("a c"));
  it("renders status as text, not color alone", () => {
    expect(renderToStaticMarkup(createElement(StatusBadge, { state: "in_progress" }))).toContain("In progress");
  });
});
describe("async button", () => {
  it("immediately announces and prevents duplicate actions while pending, then resets", async () => {
    let finish!: () => void;
    const action = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const { getByRole } = render(createElement(AsyncButton, { className: "h-20 w-full", onClick: action }, "Save"));
    const button = getByRole("button", { name: "Save" });

    fireEvent.click(button);
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(button.getAttribute("aria-busy")).toBe("true");
    expect(button.getAttribute("aria-label")).toBe("Save");
    expect(button.textContent).toBe("");
    expect(button.className).toContain("h-20 w-full");
    expect(button.className).toContain("items-center");
    expect(button.className).toContain("justify-center");
    expect(button.querySelector("svg")?.classList.contains("animate-spin")).toBe(true);
    fireEvent.click(button);
    expect(action).toHaveBeenCalledOnce();

    finish();
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
    expect(button.hasAttribute("aria-busy")).toBe(false);
    expect(button.textContent).toBe("Save");
  });

  it("resets after an action handles an API error", async () => {
    const action = vi.fn(async () => {
      try { await Promise.reject(new Error("failed")); } catch {}
    });
    const { getByRole } = render(createElement(AsyncButton, { onClick: action }, "Retry"));
    const button = getByRole("button", { name: "Retry" });

    fireEvent.click(button);
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
    expect(button.textContent).toBe("Retry");
  });
});
describe("workspace URL restoration", () => {
  it("restores list and valid detail tabs", () => {
    expect(parseLocation("?workspaces=1")).toEqual({ view: "workspaces" });
    expect(parseLocation("?workspace=7&tab=Projects")).toEqual({ view: "workspace", workspaceId: 7, tab: "Projects" });
    expect(parseLocation("?workspace=7&tab=Settings")).toEqual({ view: "workspace", workspaceId: 7, tab: "Settings" });
    expect(parseLocation("?workspace=7&tab=wat")).toEqual({ view: "workspace", workspaceId: 7, tab: "Info" });
  });
  it("recognizes invitation links", () => expect(parseLocation("?invite=abc%201")).toEqual({ view: "invitation", token: "abc 1" }));
  it("uses the canonical board history location for restoration", () => {
    expect(boardLocation(42)).toBe("?board=42");
    expect(parseLocation(boardLocation(42))).toEqual({ view: "board", boardId: 42 });
  });
});
describe("conversation branching", () => {
  const conversations = [
    { id: 1, title: "Main", parentConversationId: null, status: "active" },
    { id: 2, title: "SQLite option", parentConversationId: 1, status: "waiting" },
    { id: 3, title: "Nested fallback", parentConversationId: 2, status: "ready_to_merge" },
  ];
  const progress = {
    total: 5, resolved: 3, active: 1, waiting: 1, readyToMerge: 0, merged: 3,
    actionable: [{ id: 2, title: "SQLite option", status: "waiting", breadcrumb: "Main / SQLite option" }],
  };

  it("restores the dedicated conversation route and builds its native new-tab URL", () => {
    expect(conversationLocation(42, 7)).toBe("?job=42&conversation=7");
    expect(parseLocation("?job=42&conversation=7")).toEqual({ view: "conversation", jobId: 42, conversationId: 7 });
    expect(parseLocation("?job=42")).toEqual({ view: "conversation", jobId: 42 });
  });

  it("never combines a job header with a conversation belonging to another job", () => {
    expect(initialConversationSelection(99, conversations)).toBe(1);
    expect(initialConversationSelection(3, conversations)).toBe(3);
    expect(conversationEventsBelongTo(2, 3)).toBe(false);
    expect(conversationEventsBelongTo(3, 3)).toBe(true);
  });

  it("applies the established attachment limits to conversation replies", () => {
    const oversized = new File([new Uint8Array(20 * 1024 * 1024 + 1)], "large.bin");
    expect(() => conversationReplyRequest("", [oversized])).toThrow("20 MB");
    const files = Array.from({ length: 21 }, (_, index) => new File(["x"], `${index}.txt`));
    expect(() => conversationReplyRequest("", files)).toThrow("At most 20");
    expect(conversationReplyRequest("plain", [])).toEqual({
      method: "POST",
      body: JSON.stringify({ comment: "plain" }),
    });
  });

  it("renders Alternative B segmented progress with accessible aggregate text", () => {
    const html = renderToStaticMarkup(createElement(JobConversationProgress, { progress, compact: true }));
    expect(html).toContain("3/5 resolved");
    expect(html).toContain("1 active, 1 waiting, 0 ready to merge, 3 merged");
    expect((html.match(/conversation-progress-segment/g) || []).length).toBe(5);
  });

  it("shows at most three actionable rows in expanded progress", () => {
    const many = { ...progress, actionable: Array.from({ length: 5 }, (_, id) => ({ id, title: `Fork ${id}`, status: "waiting", breadcrumb: `Main / Fork ${id}` })) };
    const { getAllByRole, getByText } = render(createElement(JobConversationProgress, { progress: many, jobId: 42 }));
    expect(getAllByRole("link", { name: /Fork/ })).toHaveLength(3);
    expect(getByText("View all 5 conversations")).toBeTruthy();
  });

  it("nests and collapses the conversation tree", () => {
    const screen = render(createElement(ConversationBranchTree, { conversations, activeId: 3, onSelect: vi.fn() }));
    expect(screen.getByRole("button", { name: "Nested fallback" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Collapse SQLite option" }));
    expect(screen.queryByRole("button", { name: "Nested fallback" })).toBeNull();
  });

  it("creates trimmed sibling replies in one dialog submission and supports plus/remove", async () => {
    const create = vi.fn(async () => {});
    const screen = render(createElement(CreateBranchesDialog, { open: true, onOpenChange: vi.fn(), onCreate: create }));
    fireEvent.change(screen.getByLabelText("Opening reply 1"), { target: { value: " first " } });
    fireEvent.click(screen.getByRole("button", { name: "Add another branch" }));
    fireEvent.change(screen.getByLabelText("Opening reply 2"), { target: { value: "second" } });
    expect(screen.getByRole("button", { name: "Remove branch 2" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Create branches" }));
    await waitFor(() => expect(create).toHaveBeenCalledWith(["first", "second"]));
  });

  it("offers fork from each bubble and passes that event as the fork point", () => {
    const fork = vi.fn();
    const screen = render(createElement(ConversationBubble, { event: { id: 9, kind: "reply", content: "Answer" }, onFork: fork }));
    fireEvent.pointerDown(screen.getByRole("button", { name: "Conversation actions" }));
    fireEvent.click(screen.getByText("Fork conversation"));
    expect(fork).toHaveBeenCalledWith(9);
  });

  it("supports editable important points in merge review", async () => {
    const confirm = vi.fn(async () => {});
    const screen = render(createElement(MergeReviewDialog, { open: true, onOpenChange: vi.fn(), points: ["First", "Second"], onConfirm: confirm }));
    fireEvent.change(screen.getByLabelText("Important point 1"), { target: { value: "Edited" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove important point 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm merge" }));
    await waitFor(() => expect(confirm).toHaveBeenCalledWith(["Edited"]));
  });

  it("keeps the inspector timeline and adds the new-tab link above it", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    expect(app).toMatch(/target="_blank"\s+rel="noopener noreferrer"/);
    expect(app.indexOf("View conversation detail")).toBeLessThan(app.indexOf("<h3>Timeline</h3>"));
    const css = readFileSync("src/index.css", "utf8");
    expect(css).toMatch(/@media\(max-width:700px\)[\s\S]*conversation-tree-sheet/);
  });
});
describe("project navigation and jobs", () => {
	it("builds multipart job requests when files are attached", () => {
		const files = [new File(["alpha"], "a.txt"), new File(["beta"], "b.md")];
		const request = jobCreationRequest({ task: "Review", doneDefinition: "Report", files });
		expect(request.body).toBeInstanceOf(FormData);
		const body = request.body as FormData;
		expect(body.get("task")).toBe("Review");
		expect(body.getAll("files")).toEqual(files);
		expect(request.headers).toBeUndefined();
	});
	it("enforces the shared file count and per-file size limits", () => {
		expect(() => validateAttachments(Array.from({ length: 21 }, (_, i) => new File(["x"], `${i}.bin`)))).toThrow("At most 20 files");
		expect(() => validateAttachments([new File([new Uint8Array(20 * 1024 * 1024 + 1)], "large.bin")])).toThrow("20 MB");
	});
  it("restores project list and detail URLs", () => {
    expect(parseLocation("?projects=1")).toEqual({ view: "projects" });
    expect(projectLocation(12)).toBe("?project=12");
    expect(parseLocation("?project=12")).toEqual({ view: "project", projectId: 12 });
  });
  it("filters status and searches task text case-insensitively", () => {
    const jobs = [{ task: "Fix Login", state: "todo" }, { task: "Ship dashboard", state: "done" }];
    expect(filterProjectJobs(jobs, "todo", " login ")).toEqual([jobs[0]]);
    expect(filterProjectJobs(jobs, "all", "DASH")).toEqual([jobs[1]]);
    expect(filterProjectJobs(jobs, "blocked", "")).toEqual([]);
  });
});
describe("column edit", () => {
  it("patches the edited name and project while preserving worktree state", () => expect(columnPatch({name:"Review",projectId:"9",worktreeEnabled:true,worktreeName:"feature-x"})).toEqual({name:"Review",projectId:9}));
  it("links navigation to a column", () => expect(columnAnchor(7)).toBe("column-7"));
});
describe("column reorder", () => {
  it("moves a dragged column without mutating the current order", () => {
    const columns = [{ id: 1 }, { id: 2 }, { id: 3 }];
    expect(moveColumn(columns, 2, 0).map((column) => column.id)).toEqual([3, 1, 2]);
    expect(columns.map((column) => column.id)).toEqual([1, 2, 3]);
  });
});
describe("account menu", () => {
  it("links the Paragentix wordmark to the app homepage", () => {
    expect(readFileSync("src/App.tsx", "utf8")).toMatch(/<a href=\{base\} aria-label="Paragentix home">\s*Paragentix\s*<\/a>/);
  });
  it("closes native details", () => {
    const d = document.createElement("details"); d.open = true;
    closeDetails({ current: d }); expect(d.open).toBe(false);
  });
  it("keeps Hermes settings in workspace detail rather than the account menu", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    const accountMenu = app.slice(app.indexOf('className="accountmenu"'), app.indexOf('</details>', app.indexOf('className="accountmenu"')));
    expect(accountMenu).not.toMatch(/>\s*Settings\s*<\/(?:button|AsyncButton)>/);
    expect(app).toContain('["Info", "Projects", "Boards", "Users", "Settings"]');
    expect(app).toContain("Hermes URL");
    expect(app).not.toContain("Codex");
    expect(app).not.toContain("Claude Code");
    expect(app).not.toContain("default_cli");
    expect(app).not.toContain("cli_tool");
  });
});
describe("workspace list", () => {
  it("opens detail from the workspace record without a separate button", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    expect(app).toMatch(/<section[^>]+onClick=\{\(\) => openWorkspace\(w\)\}/);
    expect(app).not.toContain("Open workspace");
  });
});
describe("workspace users", () => {
  it("shows invited and accepted member statuses with distinct chips", () => {
    const invited = renderToStaticMarkup(createElement(WorkspaceUserStatus, { status: "invited" }));
    const member = renderToStaticMarkup(createElement(WorkspaceUserStatus, { status: "member" }));
    expect(invited).toContain("Invited");
    expect(invited).toContain("yellow");
    expect(member).toContain("Member");
    expect(member).toContain("green");
  });
});
describe("notification center", () => {
  it("renders lifecycle action prominently and the job title separately", async () => {
    const screen = render(createElement(NotificationCenter, { notifications: [{ id: 1, action: "Ready for review", job_title: "Safe job title", created_at: "now" }], unread: 1, more: false, onOpen: () => {}, onMarkRead: () => {}, onLoadMore: () => {} }));
    fireEvent.pointerDown(screen.getByLabelText("Notifications"));
    expect(await screen.findByText("Ready for review")).toBeTruthy();
    expect(screen.getByText("Safe job title")).toBeTruthy();
  });
  it("offers to mark all notifications read", async () => {
    const onMarkRead = vi.fn();
    const { findByText, getByLabelText, queryByText, unmount } = render(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkRead, onLoadMore: () => {} }));
    fireEvent.pointerDown(getByLabelText("Notifications"));
    fireEvent.click(await findByText("Mark Read"));
    expect(queryByText("Mark unread")).toBeNull();
    expect(onMarkRead).toHaveBeenCalledOnce();
    unmount();
  });
  it("always renders an accessible bell beside the account menu", () => {
    const html = renderToStaticMarkup(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkRead: () => {}, onLoadMore: () => {} }));
    expect(html).toContain('aria-label="Notifications"');
    expect(html).toContain('notification-bell');
  });
  it.each([[1, "1"], [9, "9"], [10, "9+"], [42, "9+"]])("shows unread count %i as %s", (unread, count) => {
    const html = renderToStaticMarkup(createElement(NotificationCenter, { notifications: [], unread, more: false, onOpen: () => {}, onMarkRead: () => {}, onLoadMore: () => {} }));
    expect(html).toContain(`<b>${count}</b>`);
  });
  it("keeps the icon button compact when its badge is visible", () => {
    const css = readFileSync("src/index.css", "utf8");
    expect(css).toMatch(/\.notification-bell\{[^}]*margin:0[^}]*padding:0/);
  });
  it("closes when clicking outside", () => {
    const { getByLabelText } = render(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkRead: () => {}, onLoadMore: () => {} }));
    const trigger = getByLabelText("Notifications");
    fireEvent.pointerDown(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("approved job workflow UX", () => {
  it("scopes serializable drafts by user, board, and entry and excludes files", () => {
    localStorage.clear();
    const top = jobDraftKey("user@example.com", 3, "top");
    const column = jobDraftKey("user@example.com", 3, "column:9");
    expect(top).not.toBe(column);
    saveJobDraft(top, { task: "top task", doneDefinition: "done", columnId: 7, files: [new File(["x"], "secret.txt")] });
    saveJobDraft(column, { task: "column task", doneDefinition: "", columnId: 9 });
    expect(loadJobDraft(top)).toEqual({ task: "top task", doneDefinition: "done", columnId: 7 });
    expect(loadJobDraft(column)).toEqual({ task: "column task", doneDefinition: "", columnId: 9 });
    clearJobDraft(top);
    expect(loadJobDraft(top)).toBeNull();
    expect(loadJobDraft(column)?.task).toBe("column task");
  });

  it("prevents only outside dismissal when requested", () => {
    const close = vi.fn();
    const screen = render(createElement(DialogShell, { title: "Create job", close, preventOutsideClose: true }, "draft"));
    fireEvent.pointerDown(document.querySelector("[data-radix-dialog-overlay]")!);
    expect(close).not.toHaveBeenCalled();
    fireEvent.click(screen.getByLabelText("Close"));
    expect(close).toHaveBeenCalledOnce();
  });

  it("uses persisted title as visible identity while retaining prompt in detail", () => {
    const html = renderToStaticMarkup(createElement(JobCard, { job: { title: "Hermes title", task: "Private detailed prompt", state: "in_review", creatorName: "A" }, open: () => {}, archive: async () => {} }));
    expect(html).toContain("Hermes title");
    expect(html).not.toContain(">Private detailed prompt<");
    expect(renderToStaticMarkup(createElement(StatusBadge, { state: "in_review" }))).toContain("In review");
  });

  it("bounds task and reply composers and exposes all timeline navigation controls", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    const css = readFileSync("src/index.css", "utf8");
    expect(app).toContain('maxLength={4000}');
    expect(css).toMatch(/job-task-input[^}]*max-height/);
    const screen = render(createElement(JobTimeline, { state: "in_review", events: [
      { id: 1, kind: "reply", content: "First" },
      { id: 2, kind: "comment", content: "Feedback" },
      { id: 3, kind: "reply", content: "Second" },
    ] }));
    for (const label of ["Go to top", "Go to bottom", "Previous reply", "Next reply"]) {
      expect(screen.getByRole("button", { name: label })).toBeTruthy();
    }
  });
});
describe("toast feedback", () => {
  it("announces success and errors with the appropriate live-region roles", () => {
    const success = render(createElement(Toast, { toast: { message: "Job 42 archived", type: "success" } }));
    expect(success.getByRole("status").textContent).toBe("Job 42 archived");
    success.unmount();
    const failure = render(createElement(Toast, { toast: { message: "Failed to archive job 42", type: "error" } }));
    expect(failure.getByRole("alert").textContent).toBe("Failed to archive job 42");
    expect(failure.getByRole("alert").classList.contains("error")).toBe(true);
  });
  it("reports exact success feedback and sensible operation failures", async () => {
    const notify = vi.fn();
    await runWithToast(vi.fn(async () => {}), notify, "Job 42 retried", "Failed to retry job 42");
    expect(notify).toHaveBeenLastCalledWith({ message: "Job 42 retried", type: "success" });

    await runWithToast(vi.fn(async () => { throw new Error("locked"); }), notify, "Column Done archived", "Failed to archive column Done");
    expect(notify).toHaveBeenLastCalledWith({ message: "Failed to archive column Done: locked", type: "error" });
  });
  it("dismisses itself after four seconds", () => {
    vi.useFakeTimers();
    const onDismiss = vi.fn();
    const toast = render(createElement(Toast, { toast: { message: "Saved", type: "success" }, onDismiss }));
    vi.advanceTimersByTime(3999);
    expect(onDismiss).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(onDismiss).toHaveBeenCalledOnce();
    toast.unmount();
    vi.useRealTimers();
  });
});
describe("workspace invitation modal", () => {
  it("closes and navigates home after accepting an invitation", async () => {
    history.replaceState({}, "", "/?invite=token");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), location.origin).pathname;
      const body = path.endsWith("/auth/me")
        ? { id: 2, email: "member@example.com" }
        : path.endsWith("/invitations/token")
          ? init?.method === "POST" ? { ok: true } : { id: 7, email: "member@example.com", workspaceName: "Team", status: "pending" }
          : path.endsWith("/notifications")
            ? { notifications: [], has_more: false, unread: 0 }
            : [];
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    }));

    const screen = render(createElement(App));
    fireEvent.click(await screen.findByRole("button", { name: "Accept invitation" }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Workspace invitation" })).toBeNull());
    expect(location.pathname + location.search).toBe("/");
    screen.unmount();
    vi.unstubAllGlobals();
  });
  it("offers acceptance for a pending invitation", () => {
    const { getByRole } = render(createElement(InvitationDialog, { invitation: { id: 7, workspaceName: "Team", status: "pending" }, close: vi.fn(), accept: vi.fn() }));
    const action = getByRole("button", { name: "Accept invitation" }) as HTMLButtonElement;
    expect(action.disabled).toBe(false);
  });
  it("shows the exact disabled accepted state", () => {
    const { getByRole } = render(createElement(InvitationDialog, { invitation: { id: 7, workspaceName: "Team", status: "accepted" }, close: vi.fn(), accept: vi.fn() }));
    const action = getByRole("button", { name: "Already accepted" }) as HTMLButtonElement;
    expect(action.disabled).toBe(true);
    expect(action.textContent).toBe("Already accepted");
  });
  it("keeps matching sessions and logs out mismatched sessions", () => {
    expect(invitationSessionAction(" User@Example.com ", "user@example.com")).toBe("show");
    expect(invitationSessionAction("other@example.com", "user@example.com")).toBe("logout");
  });
  it("uses corrected invalid-email feedback", () => {
    expect(invitationEmailValid("person@example.com")).toBe(true);
    expect(invitationEmailValid("not an email")).toBe(false);
    expect(readFileSync("src/App.tsx", "utf8")).toContain("Invitation email invalid");
  });
});
describe("api", () => {
  it("surfaces backend errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: "locked" }), {
            status: 409,
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );
    await expect(api("/jobs/1")).rejects.toThrow("locked");
  });
});
describe("jobColumn", () => {
  it("returns the last existing column without creating one", async () => {
    const create = vi.fn();
    expect(await jobColumn([{ id: 2 }, { id: 7 }], create)).toEqual({ id: 7 });
    expect(create).not.toHaveBeenCalled();
  });
  it("creates a normal generated-name column for an empty board", async () => {
    const create = vi.fn(async () => ({ id: 9, name: "quiet-fox" }));
    expect(await jobColumn([], create)).toEqual({ id: 9, name: "quiet-fox" });
    expect(create).toHaveBeenCalledOnce();
  });
});
describe("board job creation", () => {
  it("keeps column selection conditional on the board-level job flow", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    expect(app).toContain('aria-label="create board"');
    expect(app).toContain('title="create board"');
    expect(app).toContain('openJobDialog("top", undefined, true)');
    expect(app).toMatch(/form\.chooseColumn && \([\s\S]*?>\s*Column\s*<select/);
    expect(app).toContain('&lt;New Column&gt;');
    expect(app).toMatch(/form\.chooseColumn && form\.newColumn && \([\s\S]*?>\s*Projects\s*<select/);
    expect(app).toContain("openJobDialog(`column:${c.id}`, c.id)");
  });
});
describe("job comments", () => {
	it("builds multipart replies with files", () => {
		const file = new File([new Uint8Array([255, 0])], "sample.bin");
		const request = replyRequest("Inspect", [file]);
		expect(request.body).toBeInstanceOf(FormData);
		expect((request.body as FormData).get("comment")).toBe("Inspect");
		expect((request.body as FormData).getAll("files")).toEqual([file]);
	});
  it("uses a compact accessible reply composer", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    expect(app).toContain('placeholder="Reply to session"');
    expect(app).toContain('aria-label="Add files"');
    expect(app).toContain("<Paperclip />");
    expect(app).toContain("<Send />");
    expect(app).not.toContain(">Send comment<");
  });
  it("allows replies only for active sessions", () => {
    expect(canComment("in_progress")).toBe(true);
    expect(canComment("blocked")).toBe(true);
    expect(canComment("todo")).toBe(false);
    expect(canComment("done")).toBe(true);
  });
  it("unwraps the job detail API response", async () => {
    const { jobDetail } = await import("./src");
    const detail = jobDetail({
      job: { state: "done", task: "Fix it" },
      events: [{ kind: "output", content: "finished" }],
      session_id: "session-123",
    });
    expect(jobDetail(detail)).toEqual({
      state: "done",
      task: "Fix it",
      events: [{ kind: "output", content: "finished" }],
      session_id: "session-123",
    });
  });
});
describe("job detail session", () => {
  it.each([
    ["todo", 0, true],
    ["todo", 1, false],
    ["in_progress", 1, false],
    ["done", 1, false],
  ])("renders done definition editing for %s with %i attempts: %s", (state, attempt_count, editable) => {
    const view = render(createElement(DoneDefinitionField, {
      job: { state, attempt_count, done_definition: "Tests pass" },
      value: "Tests pass",
      onChange: vi.fn(),
    }));
    expect(view.queryByRole("textbox") !== null).toBe(editable);
    expect(view.getByText("Done definition")).toBeTruthy();
    if (!editable) expect(view.getByText("Tests pass").tagName).toBe("P");
  });
  it("shows a shortened session ID separately and reports copying the full ID", async () => {
    const writeText = vi.fn(async () => {}), notify = vi.fn();
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const { container, getByRole } = render(createElement(JobDetailMeta, { job: { state: "in_progress", attempt_count: 2, session_id: "session-123" }, notify }));

    expect(container.querySelector(".job-inspector-session")).toBeTruthy();
    expect(getByRole("code").textContent).toBe("session");
    expect(container.textContent).not.toContain("session-123");
    fireEvent.click(getByRole("button", { name: "Copy session ID" }));
    expect(writeText).toHaveBeenCalledWith("session-123");
    await waitFor(() => expect(notify).toHaveBeenCalledWith({ message: "SessionID copied", type: "success" }));
  });
});
describe("job detail history", () => {
  it("closes an open job detail on Back without leaving the underlying page", async () => {
    history.replaceState({}, "", "?board=42");
    const pushState = vi.spyOn(history, "pushState");
    const Harness = () => {
      const [open, setOpen] = useState(true);
      useJobDetailHistory(open, () => setOpen(false));
      return open ? createElement("div", { role: "dialog" }, "Job detail") : null;
    };
    const { queryByText, unmount } = render(createElement(Harness));

    expect(pushState).toHaveBeenCalledWith({}, "", location.href);
    expect(location.search).toBe("?board=42");
    dispatchEvent(new PopStateEvent("popstate"));

    await waitFor(() => expect(queryByText("Job detail")).toBeNull());
    expect(location.search).toBe("?board=42");
    unmount();
    pushState.mockRestore();
  });
});
describe("job actions", () => {
  it("abbreviates long task text while exposing the full task", () => {
    const wordLimitedTask = "one two three four five six seven eight nine ten a b c d e f";
    const characterLimitedTask = "abcdefgh ijklmnop qrstuvwx yzabcdef ghijklmn opqrstuv wxyzabcd efghijkl";
    const props = { open: vi.fn(), archive: vi.fn(async () => {}) };
    const { container, rerender } = render(createElement(JobCard, {
      job: { task: wordLimitedTask, state: "todo" },
      ...props,
    }));

    let task = container.querySelector(".job-open b")!;
    expect(task.textContent).toBe("one two three four five six seven eight nine ten a b c d e...");
    expect(task.getAttribute("title")).toBe(wordLimitedTask);

    rerender(createElement(JobCard, {
      job: { task: characterLimitedTask, state: "todo" },
      ...props,
    }));
    task = container.querySelector(".job-open b")!;
    expect(task.textContent).toMatch(/^.{1,60}\.\.\.$/);
    expect(task.getAttribute("title")).toBe(characterLimitedTask);
  });
  it("shows the creator avatar with an accessible tooltip", () => {
    const { getByRole, unmount } = render(createElement(JobCard, {
      job: { id: 7, task: "Ship it", state: "done", creatorName: "Ada Lovelace" },
      open: vi.fn(),
      archive: vi.fn(async () => {}),
    }));
    const avatar = getByRole("button", { name: "Ada Lovelace" });
    const tooltip = getByRole("tooltip");
    expect(avatar.textContent).toBe("A");
    expect(avatar.getAttribute("aria-describedby")).toBe(tooltip.id);
    expect(tooltip.textContent).toBe("Ada Lovelace");
    fireEvent.click(avatar);
    expect(document.activeElement).toBe(avatar);
    unmount();
  });
  it.each(["todo", "in_progress", "blocked", "done"])(
    "shows retry and archive for %s jobs",
    (state) =>
      expect(jobActionsVisible(state)).toEqual({ retry: true, archive: true }),
  );
  it("archives from the card without opening it", () => {
    const open = vi.fn(), archive = vi.fn(async () => {});
    const { getByLabelText } = render(createElement(JobCard, { job: { task: "Ship it", state: "done" }, open, archive }));
    const button = getByLabelText("Archive Ship it");
    expect(button.getAttribute("title")).toBe("Archive job");
    fireEvent.click(button);
    expect(open).not.toHaveBeenCalled();
    expect(archive).toHaveBeenCalledOnce();
  });
});
describe("chat conversations", () => {
  it("groups consecutive intermediary events in a closed activity section with the latest preview", () => {
    const { container, getByText } = render(createElement(JobTimeline, {
      state: "done",
      events: [
        { id: 1, kind: "intermediary", content: "Inspecting the repository", created_at: "14:30" },
        { id: 2, kind: "intermediary", content: "Running focused tests", created_at: "14:31" },
        { id: 3, kind: "reply", content: "Final response", created_at: "14:32" },
      ],
    }));
    const activity = container.querySelector("details");
    expect(activity).toBeTruthy();
    expect(activity?.hasAttribute("open")).toBe(false);
    expect(getByText("2 processing updates · Latest: Running focused tests")).toBeTruthy();
    expect(activity?.textContent).toContain("14:30");
    expect(activity?.textContent).toContain("Inspecting the repository");
    expect(getByText("Final response").closest(".bubble.received")).toBeTruthy();
  });

  it("does not merge intermediary groups across comment or status boundaries", () => {
    const { container } = render(createElement(JobTimeline, {
      state: "done",
      events: [
        { id: 1, kind: "intermediary", content: "First run update" },
        { id: 2, kind: "comment", content: "Continue" },
        { id: 3, kind: "intermediary", content: "Second run update" },
        { id: 4, kind: "status", content: "Status changed" },
        { id: 5, kind: "intermediary", content: "Third run update" },
      ],
    }));
    expect(container.querySelectorAll("details")).toHaveLength(3);
  });

  it("shows an accessible provider processing indicator for in-progress jobs", () => {
    const { container, getByRole } = render(createElement(JobTimeline, {
      state: "in_progress",
      events: [{ id: 1, kind: "comment", content: "Please continue" }],
    }));
    const status = getByRole("status");
    expect(status.textContent).toContain("Provider is processing…");
    expect(status.getAttribute("aria-live")).toBe("polite");
    expect(status).toBe(container.querySelector(".processing-indicator"));
    expect(status.querySelector(".processing-dots")?.getAttribute("aria-hidden")).toBe("true");
  });

  it.each(["todo", "blocked", "done"])("does not show processing for %s jobs", (state) => {
    const { queryByRole } = render(createElement(JobTimeline, { state, events: [] }));
    expect(queryByRole("status")).toBeNull();
  });

  it("places user input on the right and provider output on the left", () => {
    expect(eventSide("comment")).toBe("sent");
    expect(eventSide("input")).toBe("sent");
    expect(eventSide("output")).toBe("received");
    expect(eventSide("error")).toBe("received");
  });
  it("labels job lifecycle events in the timeline", async () => {
    const { eventLabel } = await import("./src");
    expect(eventLabel("status")).toBe("Status");
    expect(eventLabel("retry")).toBe("Retry");
    expect(eventLabel("archive")).toBe("Archive");
  });
  it("labels blocked-session input and gives the timeline room", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    const css = readFileSync("src/index.css", "utf8");
    expect(app).toContain("Blocked-session input");
    expect(css).toMatch(/\.conversation\{[^}]*min-height:min\(420px,50dvh\)/);
  });
  it("safely renders plain and structured timeline links", () => {
    const { container, getByRole } = render(createElement(TimelineContent, {
      content: "See https://example.test/log. [details](https://example.test/details) [unsafe](javascript:alert(1))",
    }));
    const links = container.querySelectorAll("a");
    expect(links).toHaveLength(2);
    expect(links[0].getAttribute("href")).toBe("https://example.test/log");
    expect(links[0].getAttribute("target")).toBe("_blank");
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer");
    expect(getByRole("link", { name: "details" }).getAttribute("href")).toBe("https://example.test/details");
    expect(container.textContent).toContain("[unsafe](javascript:alert(1))");
  });
  it("preserves timeline text around a parenthesized public URL", () => {
    const content = "Public link: (https://dev.ahsanworks.com/)\nReady";
    const { container, getByRole } = render(createElement(TimelineContent, { content }));

    expect(getByRole("link", { name: "https://dev.ahsanworks.com/" }).getAttribute("href"))
      .toBe("https://dev.ahsanworks.com/");
    expect(container.textContent).toBe(content);
  });
  it("renders replies as bubbles and job history as subdued text", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    const css = readFileSync("src/index.css", "utf8");
    const entryRule = css.match(/\.timeline-entry\{([^}]*)\}/)?.[1] ?? "";
    expect(isConversationEvent("comment")).toBe(true);
    expect(isConversationEvent("reply")).toBe(true);
    expect(isConversationEvent("status")).toBe(false);
    expect(isConversationEvent("output")).toBe(false);
    expect(app).toContain('isConversationEvent(event.kind) ? `bubble ${eventSide(event.kind)}` : "timeline-entry"');
    expect(css).toMatch(/\.bubble\.received\{[^}]*background:/);
    expect(css).toMatch(/\.bubble\.sent\{[^}]*background:/);
    expect(entryRule).toContain("color:#95a4b8");
    expect(entryRule).not.toMatch(/background|border-radius/);
    expect(css).toMatch(/\.timeline-entry a\{[^}]*color:[^}]*text-decoration:underline[^}]*\}\.timeline-entry a:hover\{[^}]*color:[^}]*\}\.timeline-entry a:focus-visible\{[^}]*outline:/);
  });
});
describe("notification paging", () => {
  it("appends only unseen notifications", () => {
    expect(mergeNotifications([{id: 2}], [{id: 2}, {id: 1}])).toEqual([{id: 2}, {id: 1}]);
  });
});
describe("mobile board controls", () => {
  const app = readFileSync("src/App.tsx", "utf8");
  const css = readFileSync("src/index.css", "utf8");
  it("keeps the job inspector anchored and opaque", () => {
    expect(css).toMatch(/\.inspector\{[^}]*--tw-translate-x:0[^}]*--tw-translate-y:0[^}]*transform:none/);
    expect(css).toMatch(/\.inspector\{[^}]*background:#11182a[^}]*box-shadow/);
  });
  it("keeps dropdown positioning owned by Radix", () => {
    const rule = css.match(/\.notificationmenu\{([^}]*)\}/)?.[1] ?? "";
    expect(rule).not.toMatch(/position:absolute|right:0|top:/);
  });
  it("makes mobile dialogs fit and scroll inside the visual viewport", () => {
    const baseModal = css.match(/\.modal\{([^}]*)\}/)?.[1] ?? "";
    expect(baseModal).not.toContain("position:relative");
    expect(css).toMatch(/@media\(max-width:600px\)[\s\S]*?\.modal\{[^}]*left:\.5rem[^}]*right:\.5rem[^}]*--tw-translate-x:0[^}]*--tw-translate-y:0[^}]*transform:none[^}]*overflow-y:auto/);
  });
  it("renders an add-job control in every column", () => {
    expect(app).toContain('className="add"');
    expect(app).toMatch(/\+ Add job\s*<\/button>/);
  });
});
