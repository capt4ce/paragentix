// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render } from "@testing-library/react";
import { api, boardLocation, canComment, closeDetails, columnPatch, eventSide, jobActionsVisible, jobColumn, mergeNotifications, NotificationCenter, parseLocation } from "./src";
describe("workspace URL restoration", () => {
  it("restores list and valid detail tabs", () => {
    expect(parseLocation("?workspaces=1")).toEqual({ view: "workspaces" });
    expect(parseLocation("?workspace=7&tab=Projects")).toEqual({ view: "workspace", workspaceId: 7, tab: "Projects" });
    expect(parseLocation("?workspace=7&tab=wat")).toEqual({ view: "workspace", workspaceId: 7, tab: "Info" });
  });
  it("recognizes invitation links", () => expect(parseLocation("?invite=abc%201")).toEqual({ view: "invitation", token: "abc 1" }));
  it("uses the canonical board history location for restoration", () => {
    expect(boardLocation(42)).toBe("?board=42");
    expect(parseLocation(boardLocation(42))).toEqual({ view: "board", boardId: 42 });
  });
});
describe("column edit", () => {
  it("patches only the project while preserving worktree state", () => expect(columnPatch({projectId:"9",worktreeEnabled:true,worktreeName:"feature-x"})).toEqual({projectId:9}));
});
describe("account menu", () => {
  it("closes native details", () => {
    const d = document.createElement("details"); d.open = true;
    closeDetails({ current: d }); expect(d.open).toBe(false);
  });
});
describe("notification center", () => {
  it("always renders an accessible bell beside the account menu", () => {
    const html = renderToStaticMarkup(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkUnread: () => {}, onLoadMore: () => {} }));
    expect(html).toContain('aria-label="Notifications"');
    expect(html).toContain('class="notification-bell"');
  });
  it("closes when clicking outside", () => {
    const { getByLabelText } = render(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkUnread: () => {}, onLoadMore: () => {} }));
    const details = getByLabelText("Notifications").closest("details")!;
    details.open = true;
    fireEvent.pointerDown(document.body);
    expect(details.open).toBe(false);
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
describe("job comments", () => {
  it("allows replies only for active sessions", () => {
    expect(canComment("in_progress")).toBe(true);
    expect(canComment("blocked")).toBe(true);
    expect(canComment("todo")).toBe(false);
    expect(canComment("done")).toBe(false);
  });
  it("unwraps the job detail API response", async () => {
    const { jobDetail } = await import("./src");
    expect(
      jobDetail({
        job: { state: "blocked", task: "Fix it" },
        events: [{ kind: "output", content: "waiting" }],
      }),
    ).toEqual({
      state: "blocked",
      task: "Fix it",
      events: [{ kind: "output", content: "waiting" }],
    });
  });
});
describe("job actions", () => {
  it.each(["todo", "in_progress", "blocked", "done"])(
    "shows retry and archive for %s jobs",
    (state) =>
      expect(jobActionsVisible(state)).toEqual({ retry: true, archive: true }),
  );
});
describe("chat conversations", () => {
  it("places user input on the right and provider output on the left", () => {
    expect(eventSide("comment")).toBe("sent");
    expect(eventSide("input")).toBe("sent");
    expect(eventSide("output")).toBe("received");
    expect(eventSide("error")).toBe("received");
  });
});
describe("notification paging", () => {
  it("appends only unseen notifications", () => {
    expect(mergeNotifications([{id: 2}], [{id: 2}, {id: 1}])).toEqual([{id: 2}, {id: 1}]);
  });
});
