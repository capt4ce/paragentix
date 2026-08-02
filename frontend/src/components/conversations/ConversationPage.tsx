import React, { useEffect, useMemo, useRef, useState } from "react";
import { api, base } from "@/lib/api";
import { submitFormShortcut } from "@/lib/forms";
import { conversationLocation } from "@/lib/routes";
import { DialogShell } from "@/components/DialogShell";
import { StatusBadge } from "@/components/jobs/StatusBadge";
import { Button } from "@/components/ui/button";
import { normalizeMergePreview } from "@/components/conversations/mergePreview";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ChevronDown, ChevronLeft, ChevronRight, Menu, Minus, MoreHorizontal, Paperclip, Plus, Send } from "lucide-react";

export type ConversationRecord = {
  id: number;
  jobId?: number;
  parentConversationId: number | null;
  forkEventId?: number | null;
  title: string;
  status: string;
};

export type ConversationProgressData = {
  total: number;
  resolved: number;
  active: number;
  waiting: number;
  readyToMerge: number;
  merged: number;
  actionable?: Array<{ id: number; title: string; status: string; breadcrumb: string }>;
};

const statusText = (status: string) => status.replaceAll("_", " ");
const MAX_CONVERSATION_ATTACHMENTS = 20;
const MAX_CONVERSATION_ATTACHMENT_SIZE = 20 * 1024 * 1024;

export function initialConversationSelection(requestedId: number | undefined, conversations: ConversationRecord[]) {
  if (requestedId && conversations.some((conversation) => conversation.id === requestedId)) return requestedId;
  return conversations.find((conversation) => conversation.parentConversationId === null)?.id ?? conversations[0]?.id ?? 0;
}

export const conversationEventsBelongTo = (requestedConversationId: number, activeConversationId: number) =>
  requestedConversationId === activeConversationId;

export function conversationReplyRequest(comment: string, files: File[]): RequestInit {
  if (files.length > MAX_CONVERSATION_ATTACHMENTS) throw Error(`At most ${MAX_CONVERSATION_ATTACHMENTS} files may be attached`);
  if (files.some((file) => file.size > MAX_CONVERSATION_ATTACHMENT_SIZE)) throw Error("Each attachment must be 20 MB or smaller");
  if (!files.length) return { method: "POST", body: JSON.stringify({ comment }) };
  const body = new FormData();
  body.set("comment", comment);
  files.forEach((file) => body.append("files", file));
  return { method: "POST", body };
}

export function JobConversationProgress({
  progress,
  compact = false,
  jobId,
}: {
  progress?: ConversationProgressData;
  compact?: boolean;
  jobId?: number;
}) {
  if (!progress?.total) return null;
  const attention = progress.waiting + progress.readyToMerge;
  const segments = [
    ...Array(progress.merged).fill("merged"),
    ...Array(attention).fill("attention"),
    ...Array(Math.max(0, progress.total - progress.merged - attention)).fill("active"),
  ];
  const accessible = `${progress.active} active, ${progress.waiting} waiting, ${progress.readyToMerge} ready to merge, ${progress.merged} merged`;
  return (
    <section className={`conversation-progress ${compact ? "compact" : "expanded"}`} aria-label={`Conversation progress: ${accessible}`}>
      {!compact && <h3>Conversation progress</h3>}
      <div className="conversation-progress-summary">
        <b>{progress.resolved}/{progress.total} resolved</b>
        <span className="sr-only">{accessible}</span>
        <div className="conversation-progress-bar" aria-hidden="true">
          {segments.map((segment, index) => <i key={index} className={`conversation-progress-segment ${segment}`} />)}
        </div>
      </div>
      {!compact && (
        <>
          <p>{progress.active} active · {progress.waiting} waiting · {progress.readyToMerge} ready to merge · {progress.merged} merged</p>
          <div className="conversation-progress-actions">
            {progress.actionable?.slice(0, 3).map((fork) => (
              <a key={fork.id} href={conversationLocation(jobId!, fork.id)} target="_blank" rel="noopener noreferrer">
                <span>{fork.breadcrumb}</span><b>{statusText(fork.status)}</b>
              </a>
            ))}
          </div>
          <a className="conversation-view-all" href={conversationLocation(jobId!)} target="_blank" rel="noopener noreferrer">
            View all {progress.total} conversations
          </a>
        </>
      )}
    </section>
  );
}

type TreeProps = {
  conversations: ConversationRecord[];
  activeId: number;
  onSelect: (id: number) => void;
};

export function ConversationBranchTree({ conversations, activeId, onSelect }: TreeProps) {
  const [collapsed, setCollapsed] = useState<Set<number>>(new Set());
  const children = useMemo(() => {
    const grouped = new Map<number | null, ConversationRecord[]>();
    conversations.forEach((conversation) => {
      const list = grouped.get(conversation.parentConversationId) ?? [];
      list.push(conversation);
      grouped.set(conversation.parentConversationId, list);
    });
    return grouped;
  }, [conversations]);
  const branch = (conversation: ConversationRecord) => {
    const descendants = children.get(conversation.id) ?? [];
    const closed = collapsed.has(conversation.id);
    return (
      <li key={conversation.id}>
        <div className="conversation-tree-row">
          {descendants.length ? (
            <button
              type="button"
              className="conversation-tree-toggle"
              aria-label={`${closed ? "Expand" : "Collapse"} ${conversation.title}`}
              onClick={() => setCollapsed((current) => {
                const next = new Set(current);
                if (closed) next.delete(conversation.id); else next.add(conversation.id);
                return next;
              })}
            >
              {closed ? <ChevronRight /> : <ChevronDown />}
            </button>
          ) : <span className="conversation-tree-spacer" />}
          <button
            type="button"
            className={conversation.id === activeId ? "active" : ""}
            aria-label={conversation.title}
            aria-current={conversation.id === activeId ? "page" : undefined}
            onClick={() => onSelect(conversation.id)}
          >
            <span>{conversation.title}</span>
            <small>{statusText(conversation.status)}</small>
          </button>
        </div>
        {!closed && descendants.length > 0 && <ul>{descendants.map(branch)}</ul>}
      </li>
    );
  };
  return <nav className="conversation-tree" aria-label="Conversations"><ul>{(children.get(null) ?? []).map(branch)}</ul></nav>;
}

export function MobileConversationDrawer({
  open,
  conversations,
  activeId,
  onClose,
  onSelect,
}: {
  open: boolean;
  conversations: ConversationRecord[];
  activeId: number;
  onClose: () => void;
  onSelect: (id: number) => void;
}) {
  const active = conversations.find((conversation) => conversation.id === activeId);
  return (
    <DialogShell
      open={open}
      close={onClose}
      title="Conversations"
      className="conversation-tree-sheet"
      description={active && (
        <span className="conversation-drawer-context">
          <span>Active conversation</span>
          <b>{active.title}</b>
          <span className="conversation-drawer-status">{statusText(active.status)}</span>
        </span>
      )}
    >
      <ConversationBranchTree
        conversations={conversations}
        activeId={activeId}
        onSelect={(id) => {
          onSelect(id);
          onClose();
        }}
      />
    </DialogShell>
  );
}

export function CreateBranchesDialog({
  open,
  onOpenChange,
  onCreate,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreate: (replies: string[]) => Promise<void>;
}) {
  const [replies, setReplies] = useState([""]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (open) {
      setReplies([""]);
      setError("");
    }
  }, [open]);
  const create = async () => {
    const trimmed = replies.map((reply) => reply.trim());
    if (trimmed.some((reply) => !reply)) {
      setError("Every branch needs an opening reply.");
      return;
    }
    setSaving(true);
    setError("");
    try {
      await onCreate(trimmed);
      onOpenChange(false);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setSaving(false);
    }
  };
  return (
    <DialogShell
      open={open}
      close={() => onOpenChange(false)}
      title="Create conversation branches"
      description="Each opening reply creates a sibling branch from the same point."
      error={error}
      footer={<>
        <Button type="button" variant="outline" disabled={saving} onClick={() => onOpenChange(false)}>Cancel</Button>
        <Button type="button" disabled={saving} aria-busy={saving || undefined} aria-label={saving ? "Creating branches" : undefined} onClick={create}>
          {saving ? "Creating…" : "Create branches"}
        </Button>
      </>}
    >
      <div className="branch-replies">
        {replies.map((reply, index) => (
          <div key={index} className="branch-reply">
            <label>Opening reply {index + 1}
              <textarea
                aria-label={`Opening reply ${index + 1}`}
                maxLength={4000}
                value={reply}
                onChange={(event) => setReplies(replies.map((value, i) => i === index ? event.target.value : value))}
              />
            </label>
            {replies.length > 1 && (
              <Button type="button" variant="outline" size="icon" aria-label={`Remove branch ${index + 1}`} onClick={() => setReplies(replies.filter((_, i) => i !== index))}>
                <Minus />
              </Button>
            )}
          </div>
        ))}
        <Button type="button" variant="outline" aria-label="Add another branch" disabled={replies.length >= 10 || saving} onClick={() => setReplies([...replies, ""])}>
          <Plus /> Add another branch
        </Button>
      </div>
    </DialogShell>
  );
}

export function MergeReviewDialog({
  open,
  onOpenChange,
  summary,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  summary: string;
  onConfirm: (summary: string) => Promise<void>;
}) {
  const [edited, setEdited] = useState(summary);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => { if (open) { setEdited(summary); setError(""); } }, [open, summary]);
  const merge = async () => {
    const approved = edited.trim();
    if (!approved) {
      setError("Keep a conversation summary.");
      return;
    }
    setSaving(true);
    setError("");
    try {
      await onConfirm(approved);
      onOpenChange(false);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setSaving(false);
    }
  };
  return (
    <DialogShell
      open={open}
      close={() => onOpenChange(false)}
      title="Merge back to parent"
      description="Review the conversation summary that will be appended to the direct parent."
      error={error}
      footer={<>
        <Button type="button" variant="outline" disabled={saving} onClick={() => onOpenChange(false)}>Cancel</Button>
        <Button type="button" disabled={saving} aria-busy={saving || undefined} aria-label={saving ? "Merging" : undefined} onClick={merge}>
          {saving ? "Merging…" : "Confirm merge"}
        </Button>
      </>}
    >
      <label className="merge-summary-field">Conversation summary
        <textarea aria-label="Conversation summary" maxLength={20000} value={edited} onChange={(event) => setEdited(event.target.value)} />
      </label>
    </DialogShell>
  );
}

export function ConversationBubble({ event, onFork, readOnly = false }: { event: any; onFork: (eventId: number) => void; readOnly?: boolean }) {
  const sent = event.kind === "comment" || event.kind === "input";
  if (event.kind === "merge") {
    let card: any;
    try { card = JSON.parse(event.content); } catch { card = undefined; }
    const summary = typeof card?.summary === "string" ? card.summary : Array.isArray(card?.points) ? card.points.join("\n\n") : "";
    if (card) return <article className="merge-card"><b>Merged from {card.sourceTitle}</b><small>{card.author} · {card.createdAt}</small><p className="merge-summary">{summary}</p></article>;
  }
  const conversational = ["comment", "input", "reply", "output"].includes(event.kind);
  return (
    <div className={conversational ? `bubble ${sent ? "sent" : "received"} conversation-bubble` : `timeline-entry ${event.kind}`}>
      <small>{sent ? "You" : conversational ? "Agent" : statusText(event.kind)}</small>
      <span>{event.content}</span>
      {conversational && !readOnly && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="ghost" size="icon" className="bubble-menu" aria-label="Conversation actions"><MoreHorizontal /></Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuItem onSelect={() => onFork(event.id)}>Fork conversation</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

export function ConversationPage({ jobId, initialConversationId }: { jobId: number; initialConversationId?: number }) {
  const [job, setJob] = useState<any>();
  const [conversations, setConversations] = useState<ConversationRecord[]>([]);
  const [activeId, setActiveId] = useState(initialConversationId ?? 0);
  const [events, setEvents] = useState<any[]>([]);
  const [treeCollapsed, setTreeCollapsed] = useState(false);
  const [mobileTree, setMobileTree] = useState(false);
  const [forkPoint, setForkPoint] = useState<{ conversationId: number; eventId: number }>();
  const [mergePreview, setMergePreview] = useState<{ sourceConversationId: number; watermark: number; summary: string }>();
  const [reply, setReply] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [error, setError] = useState("");
  const activeIdRef = useRef(activeId);
  activeIdRef.current = activeId;
  const active = conversations.find((conversation) => conversation.id === activeId);
  const byId = new Map(conversations.map((conversation) => [conversation.id, conversation]));
  const breadcrumb = active ? (() => {
    const parts = [active.title];
    let current = active;
    while (current.parentConversationId) {
      current = byId.get(current.parentConversationId)!;
      if (!current) break;
      parts.unshift(current.title);
    }
    return parts.join(" / ");
  })() : "";
  const loadTree = async () => {
    const data = await api(`/jobs/${jobId}/conversations`);
    setConversations(data.conversations);
    setActiveId((current) => initialConversationSelection(current || initialConversationId, data.conversations));
  };
  const loadEvents = async (conversationId = activeId) => {
    if (!conversationId) return;
    const loaded = await api(`/conversations/${conversationId}/events`);
    if (conversationEventsBelongTo(conversationId, activeIdRef.current)) setEvents(loaded);
  };
  useEffect(() => {
    api(`/jobs/${jobId}`).then((detail) => setJob(detail.job)).catch((failure) => setError(String(failure)));
    loadTree().catch((failure) => setError(String(failure)));
  }, [jobId]);
  useEffect(() => { loadEvents().catch((failure) => setError(String(failure))); }, [activeId]);
  useEffect(() => {
    if (!activeId) return;
    const stream = new EventSource(`${base}api/conversations/${activeId}/stream`);
    stream.onmessage = () => { void loadEvents(activeId); void loadTree(); };
    return () => stream.close();
  }, [activeId]);
  const selectConversation = (id: number) => {
    setActiveId(id);
    setMobileTree(false);
    history.replaceState({}, "", conversationLocation(jobId, id));
  };
  const newestEvent = events.at(-1)?.id;
  const mainCanReply = active?.parentConversationId || ["in_progress", "blocked", "done"].includes(job?.state);
  const readOnly = !!job?.archived;
  return (
    <main className="conversation-page">
      <header className="conversation-page-header">
        <a href={base} className="conversation-wordmark">Paragentix</a>
        <div><h1>{job?.task ?? "Conversation"}</h1><p>{breadcrumb}</p></div>
        {job && <StatusBadge state={job.state} />}
        {active?.parentConversationId && !readOnly && <Button type="button" onClick={async () => {
          try {
            setMergePreview(normalizeMergePreview(await api(`/conversations/${activeId}/merge-preview`, { method: "POST", body: "{}" })));
          } catch (failure) { setError(String(failure)); }
        }}>Merge back to parent</Button>}
      </header>
      <div className={`conversation-workspace ${treeCollapsed ? "tree-collapsed" : ""}`}>
        <aside className="conversation-tree-pane">
          <Button type="button" variant="ghost" size="icon" aria-label="Collapse conversations pane" onClick={() => setTreeCollapsed(true)}><ChevronLeft /></Button>
          <ConversationBranchTree conversations={conversations} activeId={activeId} onSelect={selectConversation} />
        </aside>
        {treeCollapsed && <Button type="button" className="conversation-tree-expand" variant="outline" size="icon" aria-label="Expand conversations pane" onClick={() => setTreeCollapsed(false)}><ChevronRight /></Button>}
        <Button type="button" className="conversation-mobile-tree-trigger" variant="outline" onClick={() => setMobileTree(true)}><Menu /> Conversations</Button>
        <MobileConversationDrawer
          open={mobileTree}
          conversations={conversations}
          activeId={activeId}
          onClose={() => setMobileTree(false)}
          onSelect={selectConversation}
        />
        <section className="conversation-focus">
          <div className="conversation-thread">
            {error && <p role="alert">{error}</p>}
            {events.map((event) => <ConversationBubble key={event.id} event={event} onFork={(eventId) => setForkPoint({ conversationId: activeId, eventId })} readOnly={readOnly} />)}
            {!events.length && <p>No conversation yet</p>}
          </div>
          <div className="conversation-footer">
            {files.length > 0 && <small>{files.length} file{files.length === 1 ? "" : "s"} attached</small>}
            {!readOnly && mainCanReply && <form className="conversation-composer" onKeyDown={submitFormShortcut} onSubmit={async (event) => {
              event.preventDefault();
              if (!reply.trim() && !files.length) return;
              try {
                await api(`/conversations/${activeId}/comment`, conversationReplyRequest(reply, files));
                setReply(""); setFiles([]); await loadEvents();
              } catch (failure) { setError(String(failure)); }
            }}>
              <label className="sr-only" htmlFor="conversation-reply">Reply</label>
              <input id="conversation-files" type="file" multiple hidden onChange={(event) => {
                const selected = Array.from(event.target.files ?? []);
                try {
                  conversationReplyRequest("", selected);
                  setFiles(selected);
                  setError("");
                } catch (failure) {
                  setFiles([]);
                  setError(String(failure));
                }
              }} />
              <Button type="button" variant="outline" size="icon" aria-label="Add files" onClick={() => document.getElementById("conversation-files")?.click()}><Paperclip /></Button>
              <textarea id="conversation-reply" maxLength={4000} placeholder="Reply to conversation" value={reply} onChange={(event) => setReply(event.target.value)} />
              <Button type="submit" size="icon" aria-label="Send reply" disabled={!reply.trim() && !files.length}><Send /></Button>
            </form>}
            {!readOnly && <button type="button" className="conversation-fork-link" disabled={!newestEvent} onClick={() => setForkPoint({ conversationId: activeId, eventId: newestEvent })}>Fork conversation</button>}
          </div>
        </section>
      </div>
      <CreateBranchesDialog open={forkPoint !== undefined} onOpenChange={(open) => { if (!open) setForkPoint(undefined); }} onCreate={async (replies) => {
        const popup = window.open("", "_blank");
        if (!popup) throw Error("Allow pop-ups to open the fork conversation");
        popup.opener = null;
        try {
          const result = await api(`/conversations/${forkPoint!.conversationId}/forks`, { method: "POST", body: JSON.stringify({ forkEventId: forkPoint!.eventId, replies }) });
          const conversationId = result.conversations[0].id;
          popup.location.href = conversationLocation(jobId, conversationId);
          await loadTree();
        } catch (failure) {
          popup.close();
          throw failure;
        }
      }} />
      <MergeReviewDialog open={!!mergePreview} onOpenChange={(open) => { if (!open) setMergePreview(undefined); }} summary={mergePreview?.summary ?? ""} onConfirm={async (summary) => {
        await api(`/conversations/${mergePreview!.sourceConversationId}/merge`, {
          method: "POST",
          body: JSON.stringify({ summary, previewWatermark: mergePreview!.watermark, idempotencyKey: `${mergePreview!.sourceConversationId}-${mergePreview!.watermark}-${Date.now()}` }),
        });
        await loadTree();
      }} />
    </main>
  );
}
