// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render } from "@testing-library/react";
import { api, boardLocation, canComment, closeDetails, columnAnchor, columnPatch, eventSide, jobActionsVisible, jobColumn, JobCard, mergeNotifications, NotificationCenter, parseLocation, DialogShell } from "./src";
import { cn } from "./src/lib/utils";
import { StatusBadge } from "./src/components/jobs/StatusBadge";
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
  it("links navigation to a column", () => expect(columnAnchor(7)).toBe("column-7"));
});
describe("account menu", () => {
  it("links the Paragentix wordmark to the app homepage", () => {
    expect(readFileSync("src/App.tsx", "utf8")).toMatch(/<a href=\{base\} aria-label="Paragentix home">\s*Paragentix\s*<\/a>/);
  });
  it("closes native details", () => {
    const d = document.createElement("details"); d.open = true;
    closeDetails({ current: d }); expect(d.open).toBe(false);
  });
  it("includes the Settings action and settings form", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    expect(app).toMatch(/>\s*Settings\s*<\/button>/);
    expect(app).toContain("Hermes URL");
    expect(app).not.toContain("Codex");
    expect(app).not.toContain("Claude Code");
    expect(app).not.toContain("default_cli");
    expect(app).not.toContain("cli_tool");
  });
});
describe("notification center", () => {
  it("always renders an accessible bell beside the account menu", () => {
    const html = renderToStaticMarkup(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkUnread: () => {}, onLoadMore: () => {} }));
    expect(html).toContain('aria-label="Notifications"');
    expect(html).toContain('notification-bell');
  });
  it("closes when clicking outside", () => {
    const { getByLabelText } = render(createElement(NotificationCenter, { notifications: [], unread: 0, more: false, onOpen: () => {}, onMarkUnread: () => {}, onLoadMore: () => {} }));
    const trigger = getByLabelText("Notifications");
    fireEvent.pointerDown(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
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
    expect(canComment("done")).toBe(true);
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
  it("places user input on the right and provider output on the left", () => {
    expect(eventSide("comment")).toBe("sent");
    expect(eventSide("input")).toBe("sent");
    expect(eventSide("output")).toBe("received");
    expect(eventSide("error")).toBe("received");
  });
  it("labels blocked-session input and gives the timeline room", () => {
    const app = readFileSync("src/App.tsx", "utf8");
    const css = readFileSync("src/index.css", "utf8");
    expect(app).toContain("Blocked-session input");
    expect(css).toMatch(/\.conversation\{[^}]*min-height:min\(420px,50dvh\)/);
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
    expect(app).toMatch(/>\s*\+ Add job\s*<\/button>/);
  });
});
