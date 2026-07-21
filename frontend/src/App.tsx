import React, { useCallback, useEffect, useId, useRef, useState } from "react";
import { StatusBadge } from "@/components/jobs/StatusBadge";
import { api, base } from "@/lib/api";
import { boardLocation, parseLocation, projectLocation } from "@/lib/routes";
import { Auth } from "@/components/Auth";
import { DialogShell } from "@/components/DialogShell";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NotificationCenter } from "@/components/NotificationCenter";
import { AsyncButton } from "@/components/AsyncButton";
import { runWithToast, Toast, type ToastMessage } from "@/components/Toast";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { Archive, Copy, Paperclip, Pencil, Plus, Send } from "lucide-react";
export async function jobColumn<T>(columns: T[], create: () => Promise<T>) {
  return columns.at(-1) ?? (await create());
}
export const jobActionsVisible = (_state: string) => ({
  retry: true,
  archive: true,
});
export async function archiveColumn(id: number) {
  await api("/columns/" + id, { method: "DELETE" });
}
export const eventSide = (kind: string) =>
  kind === "comment" || kind === "input" ? "sent" : "received";
export const isConversationEvent = (kind: string) =>
  kind === "comment" || kind === "input" || kind === "reply";
export const eventLabel = (kind: string) => ({
  status: "Status",
  retry: "Retry",
  archive: "Archive",
}[kind] ?? (kind === "error" ? "Error" : "Agent"));
const timelineLinkPattern = /\[([^\]]+)]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s<]+)/g;
const trailingUrlPunctuation = /[.,!?;:)\]}]+$/;
export function TimelineContent({ content }: { content: string }) {
  const parts: React.ReactNode[] = [];
  let lastIndex = 0;
  for (const match of content.matchAll(timelineLinkPattern)) {
    const index = match.index;
    parts.push(content.slice(lastIndex, index));
    const structuredUrl = match[2];
    const rawUrl = structuredUrl ?? match[3];
    const punctuation = structuredUrl ? "" : rawUrl.match(trailingUrlPunctuation)?.[0] ?? "";
    const url = rawUrl.slice(0, rawUrl.length - punctuation.length);
    parts.push(
      <a key={index} href={url} target="_blank" rel="noopener noreferrer">
        {structuredUrl ? match[1] : url}
      </a>,
      punctuation,
    );
    lastIndex = index + match[0].length;
  }
  parts.push(content.slice(lastIndex));
  return <>{parts}</>;
}
export const mergeNotifications = (current: any[], incoming: any[]) => [
  ...current,
  ...incoming.filter((x) => !current.some((y) => y.id === x.id)),
];
export const canComment = (state: string) =>
  state === "in_progress" || state === "blocked" || state === "done";
export const canEditDoneDefinition = (job: any) =>
  job.state === "todo" && job.attempt_count === 0;
export const jobDetail = (x: any) => x.job
  ? { ...x.job, events: x.events, session_id: x.session_id }
  : x;
export const columnPatch = (form: any) => ({
  name: form.name,
  projectId: Number(form.projectId),
});
export const columnAnchor = (id: number) => `column-${id}`;
export function moveColumn<T>(columns: T[], from: number, to: number) {
  if (from === to || from < 0 || to < 0 || from >= columns.length || to >= columns.length) return columns;
  const reordered = [...columns];
  const [column] = reordered.splice(from, 1);
  reordered.splice(to, 0, column);
  return reordered;
}
export const filterProjectJobs = (jobs: any[], status: string, search: string) => {
  const query = search.trim().toLocaleLowerCase();
  return jobs.filter((job) =>
    (status === "all" || job.state === status) &&
    (!query || job.task.toLocaleLowerCase().includes(query)));
};
export function WorkspaceUserStatus({ status }: { status: "invited" | "member" }) {
  return <Badge className={status === "invited" ? "border-yellow-600 bg-yellow-100 text-yellow-800" : "border-green-600 bg-green-100 text-green-800"}>{status === "invited" ? "Invited" : "Member"}</Badge>;
}
export const invitationSessionAction = (sessionEmail: string, invitationEmail: string) =>
  sessionEmail.trim().toLowerCase() === invitationEmail.trim().toLowerCase() ? "show" : "logout";
export const invitationEmailValid = (email: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
export function InvitationDialog({ invitation, close, accept }: { invitation: any; close: () => void; accept: () => Promise<void> | void }) {
  const accepted = invitation.status === "accepted";
  return <DialogShell title="Workspace invitation" close={close}>
    <p>You were invited to {invitation.workspaceName}.</p>
    <AsyncButton disabled={accepted} onClick={accept}>{accepted ? "Already accepted" : "Accept invitation"}</AsyncButton>
  </DialogShell>;
}
export function jobCreationRequest(form: { task: string; doneDefinition?: string; files?: File[] }): RequestInit {
	validateAttachments(form.files || []);
  if (form.files?.length) {
    const body = new FormData();
    body.set("task", form.task);
    body.set("doneDefinition", form.doneDefinition || "");
    form.files.forEach((file) => body.append("files", file));
    return { method: "POST", body };
  }
  return {
    method: "POST",
    body: JSON.stringify({ task: form.task, doneDefinition: form.doneDefinition }),
  };
}
export const MAX_ATTACHMENTS = 20;
export const MAX_ATTACHMENT_SIZE = 20 * 1024 * 1024;
export function validateAttachments(files: File[]) {
  if (files.length > MAX_ATTACHMENTS) throw Error(`At most ${MAX_ATTACHMENTS} files may be attached`);
  if (files.some((file) => file.size > MAX_ATTACHMENT_SIZE)) throw Error("Each attachment must be 20 MB or smaller");
}
export function replyRequest(comment: string, files: File[]): RequestInit {
  validateAttachments(files);
  if (!files.length) return { method: "POST", body: JSON.stringify({ comment }) };
  const body = new FormData();
  body.set("comment", comment);
  files.forEach((file) => body.append("files", file));
  return { method: "POST", body };
}
const abbreviatedJobTask = (task: string) => {
  const words = task.trim().split(/\s+/);
  if (words.length <= 15 && task.length <= 60) return task;
  return words.slice(0, 15).join(" ").slice(0, 60).trimEnd() + "...";
};
export function JobCard({
  job,
  open,
  archive,
}: {
  job: any;
  open: () => void;
  archive: () => Promise<void>;
}) {
  const creatorTooltipId = useId();
  return (
    <article className={"job " + job.state}>
      <button type="button" className="job-open" onClick={open}>
        <b title={job.task}>{abbreviatedJobTask(job.task)}</b>
        <StatusBadge state={job.state} />
      </button>
      <span className="job-creator">
        <button
          type="button"
          className="creator-avatar"
          aria-label={job.creatorName}
          aria-describedby={creatorTooltipId}
          onClick={(e) => {
            e.stopPropagation();
            e.currentTarget.focus();
          }}
        >
          {job.creatorName?.trim().charAt(0).toUpperCase()}
        </button>
        <span id={creatorTooltipId} role="tooltip" className="creator-tooltip">
          {job.creatorName}
        </span>
      </span>
      <AsyncButton
        type="button"
        className="job-archive danger"
        aria-label={`Archive ${job.task}`}
        title="Archive job"
        onClick={async (e) => {
          e.stopPropagation();
          await archive();
        }}
      >
        <Archive size={16} />
      </AsyncButton>
    </article>
  );
}
export function JobDetailMeta({ job, notify = () => {} }: { job: any; notify?: (toast: ToastMessage) => void }) {
  return (
    <>
      <p className="job-inspector-meta">
        <b>{job.state}</b> · attempt {job.attempt_count}
      </p>
      {job.session_id && (
        <div className="job-inspector-session">
          <span>Session ID: <code>{job.session_id.slice(0, 7)}</code></span>
          <Button
            type="button"
            variant="outline"
            size="icon"
            aria-label="Copy session ID"
            title="Copy session ID"
            onClick={() => runWithToast(
              () => navigator.clipboard.writeText(job.session_id),
              notify,
              "SessionID copied",
              "Failed to copy SessionID",
            )}
          >
            <Copy />
          </Button>
        </div>
      )}
    </>
  );
}
export function DoneDefinitionField({ job, value, onChange }: { job: any; value: string; onChange: (value: string) => void }) {
  return canEditDoneDefinition(job) ? (
    <label className="job-inspector-section">
      Done definition
      <textarea value={value} onChange={(e) => onChange(e.target.value)} />
    </label>
  ) : (
    <section className="job-inspector-section">
      <h3>Done definition</h3>
      <p>{job.done_definition}</p>
    </section>
  );
}
function JobDetail({
  job,
  close,
  refresh,
  notify,
}: {
  job: any;
  close: () => void;
  refresh: () => void;
  notify: (toast: ToastMessage) => void;
}) {
  const [d, setD] = useState<any>(),
    [done, setDone] = useState(job.done_definition),
    [input, setInput] = useState(""),
    [comment, setComment] = useState(""),
    [commentFiles, setCommentFiles] = useState<File[]>([]),
    [sending, setSending] = useState(false),
    [commentError, setCommentError] = useState(""),
    j = d ?? job;
  useEffect(() => {
    const loadDetail = () =>
      api("/jobs/" + job.id).then((x) => {
        const detail = jobDetail(x);
        setD(detail);
        setDone(detail.done_definition);
      });
    loadDetail();
    const es = new EventSource(base + "api/jobs/" + job.id + "/stream");
    es.onmessage = loadDetail;
    return () => es.close();
  }, [job.id]);
  const action = async (a: string, body = {}) => {
    await runWithToast(async () => {
      await api(`/jobs/${job.id}/${a}`, {
        method: "POST",
        body: JSON.stringify(body),
      });
      refresh();
      close();
    }, notify, `Job ${job.id} ${a === "retry" ? "retried" : a}`, `Failed to ${a} job ${job.id}`);
  };
  return (
    <DialogShell title="Job detail" close={close} inspector>
      <JobDetailMeta job={j} notify={notify} />
      <section className="job-inspector-section">
        <h3>Task</h3>
        <p>{j.task}</p>
      </section>
      <DoneDefinitionField job={j} value={done} onChange={setDone} />
      {canEditDoneDefinition(j) && (
        <AsyncButton
          onClick={async () => {
            await api("/jobs/" + job.id, {
              method: "PATCH",
              body: JSON.stringify({ done_definition: done }),
            });
            refresh();
          }}
        >
          Save changes
        </AsyncButton>
      )}
      {j.warning && <p role="alert">{j.warning}</p>}
      {!j.archived && <div className="job-inspector-actions">
        <AsyncButton onClick={() => action("retry")}>Retry job</AsyncButton>
        <AsyncButton
          className="danger"
          onClick={async () => {
            await runWithToast(async () => {
              await api("/jobs/" + job.id, { method: "DELETE" });
              refresh();
              setD(jobDetail(await api("/jobs/" + job.id)));
            }, notify, `Job ${job.id} archived`, `Failed to archive job ${job.id}`);
          }}
        >
          Archive job
        </AsyncButton>
      </div>}
      <h3>Timeline</h3>
      <div className="conversation">
        {j.events?.length ? (
          j.events.map((e: any) => (
            <div key={e.id} className={`${isConversationEvent(e.kind) ? `bubble ${eventSide(e.kind)}` : "timeline-entry"} ${e.kind}`}>
              <small>
                {eventSide(e.kind) === "sent"
                  ? "You"
                  : eventLabel(e.kind)}
              </small>
              <span><TimelineContent content={e.content} /></span>
            </div>
          ))
        ) : (
          <p>No output yet</p>
        )}
      </div>
      {canComment(j.state) && (
        <div className="commentbox">
          <div className="commentbox-row">
            <button
              type="button"
              className={buttonVariants({ variant: "outline", size: "icon" })}
              aria-label="Add files"
              title="Add files"
              onClick={() => document.getElementById(`reply-files-${job.id}`)?.click()}
            >
              <Paperclip />
            </button>
            <input
              id={`reply-files-${job.id}`}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                const files = Array.from(e.target.files || []);
                try { validateAttachments(files); setCommentFiles(files); setCommentError(""); }
                catch (error) { setCommentFiles([]); setCommentError(String(error)); }
              }}
            />
            <textarea
              aria-label="Reply to session"
              maxLength={4000}
              placeholder="Reply to session"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
            />
            <button
              type="button"
              className={buttonVariants({ size: "icon" })}
              aria-label={sending ? "Sending reply" : "Send reply"}
              title="Send reply"
              aria-busy={sending || undefined}
              disabled={sending || (!comment.trim() && !commentFiles.length)}
              onClick={async () => {
                setSending(true);
                setCommentError("");
                try {
                  await api(`/jobs/${job.id}/comment`, replyRequest(comment, commentFiles));
                  setComment("");
                  setCommentFiles([]);
                  setD(jobDetail(await api("/jobs/" + job.id)));
                } catch (e) {
                  setCommentError(String(e));
                } finally {
                  setSending(false);
                }
              }}
            >
              <Send />
            </button>
          </div>
          {commentFiles.length > 0 && <small>{commentFiles.length} file{commentFiles.length === 1 ? "" : "s"} attached</small>}
          {commentError && <p role="alert">{commentError}</p>}
        </div>
      )}
      {j.state === "blocked" && (
        <div className="actions">
          <label>
            Blocked-session input
            <input
              placeholder="Answer the agent's request…"
              value={input}
              onChange={(e) => setInput(e.target.value)}
            />
          </label>
          <AsyncButton onClick={() => action("input", { input })}>Send input</AsyncButton>
          <AsyncButton onClick={() => action("approve")}>Approve</AsyncButton>
          <AsyncButton onClick={() => action("cancel")}>Cancel to todo</AsyncButton>
        </div>
      )}
    </DialogShell>
  );
}
export function closeDetails(ref: { current: HTMLDetailsElement | null }) {
  if (ref.current) ref.current.open = false;
}

export function useJobDetailHistory(open: boolean, close: () => void) {
  useEffect(() => {
    if (!open) return;
    history.pushState(history.state, "", location.href);
    const pop = () => close();
    addEventListener("popstate", pop);
    return () => removeEventListener("popstate", pop);
  }, [open]);
}

export function App() {
  const [me, setMe] = useState<any>(),
    [ws, setWs] = useState<any[]>([]),
    [boards, setBoards] = useState<any[]>([]),
    [board, setBoard] = useState<any>(),
    [cols, setCols] = useState<any[]>([]),
    [view, setView] = useState("board"),
    [detail, setDetail] = useState<any>(),
    [tab, setTab] = useState("Info"),
    [items, setItems] = useState<any[]>([]),
    [dialog, setDialog] = useState(""),
    [form, setForm] = useState<any>({}),
    [settings, setSettings] = useState<any>(),
    [error, setError] = useState(""),
    [notifications, setNotifications] = useState<any[]>([]),
    [notificationMore, setNotificationMore] = useState(false),
    [unread, setUnread] = useState(0),
    [jobStatus, setJobStatus] = useState("all"),
    [jobSearch, setJobSearch] = useState(""),
    [loadingTab, setLoadingTab] = useState(""),
    [reorderAnnouncement, setReorderAnnouncement] = useState(""),
    [job, setJob] = useState<any>(),
    [invitation, setInvitation] = useState<any>(),
    [toast, setToast] = useState<ToastMessage>();
  const dismissToast = useCallback(() => setToast(undefined), []);
  const menu = useRef<HTMLDetailsElement>(null);
  const draggedColumn = useRef<number | null>(null);
  const reorderColumns = async (from: number, to: number) => {
    const previous = cols;
    const reordered = moveColumn(previous, from, to);
    if (reordered === previous || !board) return;
    setCols(reordered);
    setReorderAnnouncement(`${reordered[to].name} moved to position ${to + 1} of ${reordered.length}.`);
    try {
      await api(`/boards/${board.id}/columns`, {
        method: "PATCH",
        body: JSON.stringify({ columnIds: reordered.map((column) => column.id) }),
      });
    } catch (e) {
      setCols(previous);
      setReorderAnnouncement("Column order was not changed.");
      setToast({ message: `Failed to reorder columns: ${String(e)}`, type: "error" });
    }
  };
  useJobDetailHistory(!!job, () => setJob(undefined));
  const load = async () => {
    const w = await api("/workspaces"),
      b = await api("/boards");
    setWs(w);
    setBoards(b);
    const route = parseLocation(location.search);
    const active =
      b.find((x: any) => x.id === ((route as any).boardId || board?.id)) ||
      b[0];
    setBoard(active);
    setCols(active ? await api(`/boards/${active.id}/columns`) : []);
  };
  const restore = async () => {
    const route = parseLocation(location.search);
    setView(route.view);
    if (route.view === "workspace") {
      const d = await api("/workspaces/" + route.workspaceId);
      setDetail(d);
      setTab(route.tab!);
      if (route.tab === "Settings")
        setSettings(await api(`/workspaces/${route.workspaceId}/settings`));
      setItems(
        route.tab === "Info" || route.tab === "Settings"
          ? []
          : await api(
              `/workspaces/${route.workspaceId}/${route.tab!.toLowerCase()}`,
            ),
      );
    } else if (route.view === "projects") {
      setItems(await api("/projects"));
      setDetail(undefined);
    } else if (route.view === "project") {
      setDetail(await api("/projects/" + route.projectId));
      setJobStatus("all");
      setJobSearch("");
    }
  };
  useEffect(() => {
    api("/auth/me")
      .then(async (x) => {
        setMe(x);
        try {
          await load();
          const route = parseLocation(location.search);
          if (route.view === "invitation") {
            const pending = await api(`/invitations/${encodeURIComponent(route.token!)}`);
            if (invitationSessionAction(x.email, pending.email) === "logout") {
              await api("/auth/logout", { method: "POST" });
              location.reload();
              return;
            }
            setInvitation({ ...pending, token: route.token });
            await restore();
          } else {
            await restore();
            const pending = await api("/invitations/active");
            if (pending) setInvitation(pending);
          }
        } catch (e) {
          setError(String(e));
        }
      })
      .catch(() => setMe(false));
  }, []);
  useEffect(() => {
    const outside = (e: PointerEvent) => {
      if (menu.current && !menu.current.contains(e.target as Node))
        closeDetails(menu);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, []);
  useEffect(() => {
    const pop = () => restore().catch((e) => setError(String(e)));
    addEventListener("popstate", pop);
    return () => removeEventListener("popstate", pop);
  }, []);
  useEffect(() => {
    if (!me) return;
    const poll = () =>
      api("/notifications?limit=10")
        .then((p) => {
          setNotifications(p.notifications);
          setNotificationMore(p.has_more);
          setUnread(p.unread);
        })
        .catch(() => {});
    poll();
    const timer = setInterval(poll, 2000);
    return () => clearInterval(timer);
  }, [me]);
  const openWorkspace = async (w: any) => {
    setDetail(await api("/workspaces/" + w.id));
    setView("workspace");
    setTab("Info");
    setItems([]);
    history.pushState({}, "", `?workspace=${w.id}&tab=Info`);
  };
  const chooseTab = async (t: string) => {
    setLoadingTab(t);
    setTab(t);
    history.pushState({}, "", `?workspace=${detail.id}&tab=${t}`);
    try {
      if (t === "Settings")
        setSettings(await api(`/workspaces/${detail.id}/settings`));
      setItems(
        t === "Info" || t === "Settings"
          ? []
          : await api(`/workspaces/${detail.id}/${t.toLowerCase()}`),
      );
    } finally {
      setLoadingTab("");
    }
  };
  const submit = async () => {
    if (dialog === "invite" && !invitationEmailValid(form.email || "")) {
      setToast({ message: "Invitation email invalid", type: "error" });
      return;
    }
    try {
      if (dialog === "workspace")
        await api("/workspaces", {
          method: "POST",
          body: JSON.stringify({ name: form.name }),
        });
      if (dialog === "edit workspace")
        await api("/workspaces/" + detail.id, {
          method: "PATCH",
          body: JSON.stringify({ name: form.name }),
        });
      if (dialog === "project")
        await api(`/workspaces/${detail.id}/projects`, {
          method: "POST",
          body: JSON.stringify(form),
        });
      if (dialog === "edit project")
        await api("/projects/" + form.id, {
          method: "PATCH",
          body: JSON.stringify(form),
        });
      if (dialog === "board")
        await api("/boards", {
          method: "POST",
          body: JSON.stringify({
            name: form.name,
            workspaceId: Number(form.workspaceId),
          }),
        });
      if (dialog === "column")
        await api(`/boards/${board.id}/columns`, {
          method: "POST",
          body: JSON.stringify({
            name: form.name,
            projectId: Number(form.projectId),
            worktreeEnabled: !!form.worktreeEnabled,
            worktreeName: form.worktreeName || "",
          }),
        });
      if (dialog === "job")
        await api(`/columns/${form.columnId}/jobs`, {
          ...jobCreationRequest(form),
        });
      if (dialog === "edit column")
        await api(`/columns/${form.id}`, {
          method: "PATCH",
          body: JSON.stringify(columnPatch(form)),
        });
      if (dialog === "invite")
        await api(`/workspaces/${detail.id}/invitations`, {
          method: "POST",
          body: JSON.stringify({ email: form.email }),
        });
      if (dialog === "remove")
        await api(`/workspaces/${detail.id}/members/${form.id}`, {
          method: "DELETE",
        });
      setDialog("");
      setForm({});
      await load();
      if (view === "workspace") await chooseTab(tab);
      if (dialog === "invite")
        setToast({ message: `Invitation sent to ${form.email}`, type: "success" });
    } catch (e) {
      if (dialog === "invite") {
        const detail = e instanceof Error ? e.message : String(e);
        setToast({ message: `Failed to send invitation: ${detail}`, type: "error" });
      } else setError(String(e));
    }
  };
  const route = parseLocation(location.search);
  if (me === undefined) return null;
  if (!me)
    return (
      <Auth
        invitation={route.view === "invitation" ? route.token : undefined}
      />
    );
  const openNotification = async (n: any) => {
    await api(`/notifications/${n.id}`, {
      method: "PATCH",
      body: JSON.stringify({ read: true }),
    });
    setNotifications(
      notifications.map((x) => (x.id === n.id ? { ...x, read: true } : x)),
    );
    setUnread(Math.max(0, unread - Number(!n.read)));
    if (n.job_id) setJob(jobDetail(await api(`/jobs/${n.job_id}`)));
    if (n.invitation_id) setInvitation(await api(`/invitations/id/${n.invitation_id}`));
  };
  return (
    <>
      <header>
        <h1>
          <a href={base} aria-label="Paragentix home">
            Paragentix
          </a>
        </h1>
        <nav className="board-controls">
          {view === "board" && (
            <>
              <div className="board-select-row">
                <select
                  aria-label="Workspace board"
                  value={board?.id || ""}
                  onChange={async (e) => {
                    const b = boards.find((x) => x.id === Number(e.target.value));
                    setBoard(b);
                    setCols(await api(`/boards/${b.id}/columns`));
                  }}
                >
                  {boards.map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.workspaceName} / {b.name}
                    </option>
                  ))}
                </select>
                <button
                  className={buttonVariants({ variant: "outline", size: "icon" })}
                  aria-label="create board"
                  title="create board"
                  onClick={() => {
                    setForm({ workspaceId: ws[0]?.id });
                    setDialog("board");
                  }}
                >
                  <Plus size={18} />
                </button>
              </div>
              <button
                disabled={!cols.length}
                onClick={() => {
                  setForm({ columnId: cols[0]?.id, task: "", doneDefinition: "", chooseColumn: true });
                  setDialog("job");
                }}
              >
                Create job
              </button>
            </>
          )}
        </nav>
        <div className="header-actions">
          <NotificationCenter
            notifications={notifications}
            unread={unread}
            more={notificationMore}
            onOpen={openNotification}
            onMarkRead={async () => {
              await api("/notifications/mark-read", {
                method: "POST",
                body: "{}",
              });
              setNotifications(
                notifications.map((n) => ({ ...n, read: true })),
              );
              setUnread(0);
            }}
            onLoadMore={async () => {
              setNotificationMore(false);
              const p = await api(
                `/notifications?limit=10&before=${notifications.at(-1)?.id}`,
              );
              setNotifications(
                mergeNotifications(notifications, p.notifications),
              );
              setNotificationMore(p.has_more);
            }}
          />
          <details ref={menu} className="account">
            <summary aria-label="Account menu">
              <span>{me.email[0].toUpperCase()}</span>
            </summary>
            <div className="accountmenu">
              <strong>{me.email}</strong>
              <AsyncButton
                onClick={async () => {
                  closeDetails(menu);
                  setItems(await api("/projects"));
                  setDetail(undefined);
                  setView("projects");
                  history.pushState({}, "", "?projects=1");
                }}
              >
                Projects
              </AsyncButton>
              <button
                onClick={() => {
                  closeDetails(menu);
                  setView("workspaces");
                  history.pushState({}, "", "?workspaces=1");
                }}
              >
                Workspaces
              </button>
              <button
                onClick={() => {
                  closeDetails(menu);
                  setDialog("profile");
                }}
              >
                Profile
              </button>
              <AsyncButton
                onClick={async () => {
                  closeDetails(menu);
                  await api("/auth/logout", { method: "POST" });
                  location.reload();
                }}
              >
                Sign out
              </AsyncButton>
            </div>
          </details>
        </div>
      </header>
      {view === "projects" && (
        <main className="page">
          <div className="pagehead"><h2>Projects</h2></div>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Project</th><th>Workspace</th><th>Directory</th><th>Columns</th><th>Jobs</th></tr></thead>
              <tbody>{items.map((p) => (
                <tr key={p.id} className="clickable-row">
                  <td><AsyncButton className="link" onClick={async () => {
                    setView("project");
                    history.pushState({}, "", projectLocation(p.id));
                    try { setDetail(await api(`/projects/${p.id}`)); } catch (e) { setError(String(e)); }
                    setJobStatus("all"); setJobSearch("");
                  }}>{p.name}</AsyncButton></td><td>{p.workspaceName}</td><td><code>{p.directory}</code></td><td>{p.columnCount}</td><td>{p.jobCount}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </main>
      )}
      {view === "project" && detail && (
        <main className="page">
          <AsyncButton className="back" onClick={async () => { setItems(await api("/projects")); setDetail(undefined); setView("projects"); history.pushState({}, "", "?projects=1"); }}>← Projects</AsyncButton>
          <section className="panel project-details"><h2>{detail.name}</h2><span>Workspace: {detail.workspaceName}</span><code>{detail.directory}</code></section>
          <div className="pagehead"><h3>Jobs</h3><div className="job-filters">
            <Input aria-label="Search job tasks" placeholder="Search task…" value={jobSearch} onChange={(e) => setJobSearch(e.target.value)} />
            <select aria-label="Filter jobs by status" value={jobStatus} onChange={(e) => setJobStatus(e.target.value)}><option value="all">All statuses</option><option value="todo">Todo</option><option value="in_progress">In progress</option><option value="blocked">Blocked</option><option value="done">Done</option></select>
          </div></div>
          <div className="table-wrap"><table><thead><tr><th>Task</th><th>Status</th><th>Board / column</th><th>Updated</th></tr></thead><tbody>
            {filterProjectJobs(detail.jobs, jobStatus, jobSearch).map((j) => <tr key={j.id}><td>{j.task}</td><td><StatusBadge state={j.state} /></td><td>{j.boardName} / {j.columnName}</td><td>{j.updated_at}</td></tr>)}
          </tbody></table>{!filterProjectJobs(detail.jobs, jobStatus, jobSearch).length && <p className="empty">No jobs match these filters.</p>}</div>
        </main>
      )}
      {view === "workspaces" && (
        <main className="page">
          <div className="pagehead">
            <h2>Workspaces</h2>
            <button
              onClick={() => {
                setForm({});
                setDialog("workspace");
              }}
            >
              New workspace
            </button>
          </div>
          {ws.map((w) => (
            <section
              className="panel clickable-row"
              key={w.id}
              role="button"
              tabIndex={0}
              onClick={() => openWorkspace(w)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") openWorkspace(w);
              }}
            >
              <h3>{w.name}</h3>
              <p>
                {w.role} · {w.projectCount} projects · {w.memberCount} users
              </p>
            </section>
          ))}
        </main>
      )}
      {view === "workspace" && detail && (
        <main className="page">
          <button
            onClick={() => {
              setView("workspaces");
              history.pushState({}, "", "?workspaces=1");
            }}
          >
            ← Workspaces
          </button>
          <h2>{detail.name}</h2>
          <Tabs value={tab} onValueChange={chooseTab}>
            <TabsList>
              {["Info", "Projects", "Boards", "Users", "Settings"].map((t) => (
                <TabsTrigger key={t} value={t} disabled={!!loadingTab} aria-busy={loadingTab === t || undefined}>
                  {loadingTab === t ? `${t}…` : t}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          {tab === "Info" && (
            <section className="panel">
              <p>Role: {detail.role}</p>
              <p>
                {detail.projectCount} projects · {detail.memberCount} users
              </p>
              {detail.role === "owner" && (
                <button
                  onClick={() => {
                    setForm({ name: detail.name });
                    setDialog("edit workspace");
                  }}
                >
                  Edit workspace
                </button>
              )}
            </section>
          )}
          {tab === "Projects" && (
            <>
              <div className="pagehead">
                <h3>Projects</h3>
                {detail.role === "owner" && (
                  <button
                    onClick={() => {
                      setForm({});
                      setDialog("project");
                    }}
                  >
                    New project
                  </button>
                )}
              </div>
              {items.map((p) => (
                <section className="panel">
                  <b>{p.name}</b>
                  <code>{p.directory}</code>
                  {detail.role === "owner" && (
                    <button
                      onClick={() => {
                        setForm(p);
                        setDialog("edit project");
                      }}
                    >
                      Edit
                    </button>
                  )}
                </section>
              ))}
            </>
          )}
          {tab === "Boards" &&
            items.map((b) => (
              <section className="panel">
                <b>{b.name}</b>
                <span>{b.columnCount} columns</span>
                <AsyncButton
                  onClick={async () => {
                    setBoard(boards.find((x) => x.id === b.id));
                    setView("board");
                    history.pushState({}, "", boardLocation(b.id));
                    setCols(await api(`/boards/${b.id}/columns`));
                  }}
                >
                  Open board
                </AsyncButton>
              </section>
            ))}
          {tab === "Users" && (
            <>
              <div className="pagehead">
                <h3>Users</h3>
                {detail.role === "owner" && (
                  <button
                    onClick={() => {
                      setForm({});
                      setDialog("invite");
                    }}
                  >
                    Invite user
                  </button>
                )}
              </div>
              {items.map((m) => (
                <section className="panel" key={`${m.status}-${m.id ?? m.email}`}>
                  <b>{m.email}</b>
                  <WorkspaceUserStatus status={m.status} />
                  {m.status === "member" && <span>{m.role} · joined {m.joinedAt}</span>}
                  {detail.role === "owner" && m.status === "member" && m.id !== me.id && (
                    <button
                      onClick={() => {
                        setForm(m);
                        setDialog("remove");
                      }}
                    >
                      Remove
                    </button>
                  )}
                </section>
              ))}
            </>
          )}
          {tab === "Settings" && settings && (
            <section className="panel">
              <label>
                Hermes URL
                <input
                  type="url"
                  required
                  disabled={detail.role !== "owner"}
                  value={settings.hermes_url || ""}
                  onChange={(e) => setSettings({ ...settings, hermes_url: e.target.value })}
                />
              </label>
              <label>
                Hermes API key
                <input
                  type="password"
                  required={!settings.hermes_api_key_set}
                  disabled={detail.role !== "owner"}
                  placeholder={settings.hermes_api_key_set ? "Saved — leave blank to keep" : "Required"}
                  value={settings.hermes_api_key || ""}
                  onChange={(e) => setSettings({ ...settings, hermes_api_key: e.target.value })}
                />
              </label>
              <label>
                Hermes model
                <input
                  required
                  disabled={detail.role !== "owner"}
                  value={settings.hermes_model || "hermes-agent"}
                  onChange={(e) => setSettings({ ...settings, hermes_model: e.target.value })}
                />
              </label>
              {detail.role === "owner" && (
                <AsyncButton onClick={async () => {
                  try {
                    setSettings(await api(`/workspaces/${detail.id}/settings`, {
                      method: "PATCH",
                      body: JSON.stringify(settings),
                    }));
                  } catch (e) {
                    setError(String(e));
                  }
                }}>Save</AsyncButton>
              )}
            </section>
          )}
        </main>
      )}
      {view === "board" && (
        <>
          <nav className="column-nav" aria-label="Focus column">
            <span id="column-reorder-help" className="sr-only">
              Drag column titles to reorder them, or press Alt with Left or Right arrow.
            </span>
            <span className="sr-only" role="status" aria-live="polite">{reorderAnnouncement}</span>
            {cols.map((c, index) => (
              <button
                key={c.id}
                draggable
                aria-describedby="column-reorder-help"
                aria-keyshortcuts="Alt+ArrowLeft Alt+ArrowRight"
                onDragStart={(event) => {
                  draggedColumn.current = index;
                  event.dataTransfer.effectAllowed = "move";
                  event.dataTransfer.setData("text/plain", String(c.id));
                }}
                onDragOver={(event) => {
                  event.preventDefault();
                  event.dataTransfer.dropEffect = "move";
                }}
                onDrop={(event) => {
                  event.preventDefault();
                  const from = draggedColumn.current;
                  draggedColumn.current = null;
                  if (from !== null) void reorderColumns(from, index);
                }}
                onDragEnd={() => { draggedColumn.current = null; }}
                onKeyDown={(event) => {
                  if (!event.altKey) return;
                  const offset = event.key === "ArrowLeft" ? -1 : event.key === "ArrowRight" ? 1 : 0;
                  if (offset && index + offset >= 0 && index + offset < cols.length) {
                    event.preventDefault();
                    void reorderColumns(index, index + offset);
                  }
                }}
                onClick={() =>
                  document
                    .getElementById(columnAnchor(c.id))
                    ?.scrollIntoView({
                      behavior: "smooth",
                      inline: "center",
                      block: "nearest",
                    })
                }
              >
                {c.name}
              </button>
            ))}
            {board && (
              <AsyncButton
                className={buttonVariants({ variant: "outline", size: "icon" })}
                aria-label="Add column"
                title="Add column"
                onClick={async () => {
                  const projects = await api(
                    `/workspaces/${board.workspaceId}/projects`,
                  );
                  setForm({
                    projects,
                    projectId: projects[0]?.id,
                    worktreeEnabled: false,
                  });
                  setDialog("column");
                }}
              >
                <Plus size={18} />
              </AsyncButton>
            )}
          </nav>
          <main className="board">
            {cols.map((c) => (
              <section id={columnAnchor(c.id)} key={c.id} className="lane">
                <div className="lanehead">
                  <b>{c.name}</b>
                  <span className="lane-actions">
                    <AsyncButton
                      aria-label={`Edit ${c.name}`}
                      title="Edit column"
                      onClick={async () => {
                        setForm({
                          ...c,
                          projects: await api(
                            `/workspaces/${board.workspaceId}/projects`,
                          ),
                        });
                        setDialog("edit column");
                      }}
                    >
                      <Pencil size={16} />
                    </AsyncButton>
                    <AsyncButton
                      className="danger"
                      aria-label={`Archive ${c.name}`}
                      title="Archive column"
                      onClick={async () => {
                        await runWithToast(async () => {
                          await archiveColumn(c.id);
                          await load();
                        }, setToast, `Column ${c.name} archived`, `Failed to archive column ${c.name}`);
                      }}
                    >
                      <Archive size={16} />
                    </AsyncButton>
                  </span>
                </div>
                <small>
                  Project: {c.projectName}
                  {c.worktreeEnabled ? ` · Worktree: ${c.worktreeName}` : ""}
                </small>
                {c.jobs?.map((j: any) => (
                  <JobCard
                    key={j.id}
                    job={j}
                    open={() => setJob(j)}
                    archive={async () => {
                      await runWithToast(async () => {
                        await api(`/jobs/${j.id}`, { method: "DELETE" });
                        await load();
                      }, setToast, `Job ${j.id} archived`, `Failed to archive job ${j.id}`);
                    }}
                  />
                ))}
                <button
                  className="add"
                  onClick={() => {
                    setForm({ columnId: c.id, task: "", doneDefinition: "" });
                    setDialog("job");
                  }}
                >
                  + Add job
                </button>
              </section>
            ))}
          </main>
        </>
      )}
      {dialog && (
        <DialogShell title={dialog} close={() => setDialog("")}>
          {error && <p role="alert">{error}</p>}
          {dialog === "profile" ? (
            <p>{me.email}</p>
          ) : dialog === "remove" ? (
            <>
              <p>Remove {form.email} from this workspace?</p>
              <AsyncButton onClick={submit}>Confirm removal</AsyncButton>
            </>
          ) : (
            <>
              {dialog === "invite" ? (
                <label>
                  Email
                  <input
                    type="email"
                    required
                    value={form.email || ""}
                    onChange={(e) =>
                      setForm({ ...form, email: e.target.value })
                    }
                  />
                </label>
              ) : dialog === "job" ? (
                <>
                  {form.chooseColumn && (
                    <label>
                      Column
                      <select
                        required
                        value={form.columnId || ""}
                        onChange={(e) => setForm({ ...form, columnId: e.target.value })}
                      >
                        <option value="">Select…</option>
                        {cols.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                      </select>
                    </label>
                  )}
                  <label>
                    Task
                    <textarea
                      required
                      value={form.task || ""}
                      onChange={(e) =>
                        setForm({ ...form, task: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    Done definition
                    <textarea
                      value={form.doneDefinition || ""}
                      onChange={(e) =>
                        setForm({ ...form, doneDefinition: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    Additional context files
                    <input
                      type="file"
                      multiple
                      onChange={(e) =>
                        setForm({ ...form, files: Array.from(e.target.files || []) })
                      }
                    />
                    <small>Up to 20 files of any type, 20 MB each.</small>
                  </label>
                </>
              ) : (
                <label>
                  Name
                  <input
                    required
                    value={form.name || ""}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                  />
                </label>
              )}
              {["project", "edit project"].includes(dialog) && (
                <label>
                  Directory
                  <input
                    required
                    value={form.directory || ""}
                    onChange={(e) =>
                      setForm({ ...form, directory: e.target.value })
                    }
                  />
                </label>
              )}
              {dialog === "board" && (
                <label>
                  Workspace
                  <select
                    value={form.workspaceId || ""}
                    onChange={(e) =>
                      setForm({ ...form, workspaceId: e.target.value })
                    }
                  >
                    {ws.map((w) => (
                      <option value={w.id}>{w.name}</option>
                    ))}
                  </select>
                </label>
              )}
              {["column", "edit column"].includes(dialog) && (
                <>
                  <label>
                    Project
                    <select
                      required
                      value={form.projectId || ""}
                      onChange={(e) =>
                        setForm({ ...form, projectId: e.target.value })
                      }
                    >
                      <option value="">Select…</option>
                      {form.projects?.map((p: any) => (
                        <option value={p.id}>{p.name}</option>
                      ))}
                    </select>
                  </label>
                  <label>
                    <input
                      type="checkbox"
                      checked={!!form.worktreeEnabled}
                      onChange={(e) =>
                        setForm({ ...form, worktreeEnabled: e.target.checked })
                      }
                    />{" "}
                    Git worktree
                  </label>
                  <label>
                    Worktree name
                    <input
                      disabled={!form.worktreeEnabled}
                      value={form.worktreeName || ""}
                      onChange={(e) =>
                        setForm({ ...form, worktreeName: e.target.value })
                      }
                    />
                  </label>
                </>
              )}
              <AsyncButton
                disabled={(dialog === "column" && !form.projectId) || (dialog === "job" && !form.columnId)}
                onClick={submit}
              >
                Save
              </AsyncButton>
            </>
          )}
        </DialogShell>
      )}
      {job && (
        <JobDetail
          job={job}
          close={() => history.back()}
          refresh={async () => setJob(jobDetail(await api(`/jobs/${job.id}`)))}
          notify={setToast}
        />
      )}
      {invitation && <InvitationDialog invitation={invitation} close={() => setInvitation(undefined)} accept={async () => {
        const path = invitation.token ? `/invitations/${encodeURIComponent(invitation.token)}` : `/invitations/id/${invitation.id}`;
        await api(path, { method: "POST" });
        setInvitation(undefined);
        setView("board");
        history.pushState({}, "", base);
        await load();
      }} />}
      <Toast toast={toast} onDismiss={dismissToast} />
    </>
  );
}
