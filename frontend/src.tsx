import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";
const base = import.meta.env.BASE_URL;
export async function api(p: string, o?: RequestInit) {
  const r = await fetch(base + "api" + p, {
    ...o,
    headers: { "Content-Type": "application/json", ...o?.headers },
  });
  if (!r.ok) throw Error((await r.json()).error);
  return r.status === 204 ? null : r.json();
}
function Auth({ invitation }: { invitation?: string }) {
  const [email, setEmail] = useState(""),
    [password, setPassword] = useState(""),
    [signup, setSignup] = useState(false),
    [error, setError] = useState("");
  return (
    <main className="auth">
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          try {
            await api("/auth/" + (signup ? "signup" : "login"), {
              method: "POST",
              body: JSON.stringify({ email, password }),
            });
            if (invitation) await api(`/invitations/${encodeURIComponent(invitation)}`, { method: "POST" });
            location.reload();
          } catch (e) {
            setError(String(e));
          }
        }}
      >
        <h1>Paragentix</h1>
        <label>
          Email
          <input
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </label>
        <label>
          Password
          <input
            type="password"
            minLength={8}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && <p role="alert">{error}</p>}
        <button>{signup ? "Create account" : "Sign in"}</button>
        <button
          type="button"
          className="link"
          onClick={() => setSignup(!signup)}
        >
          {signup ? "Sign in" : "Create account"}
        </button>
      </form>
    </main>
  );
}
function Dialog({
  title,
  close,
  children,
}: {
  title: string;
  close: () => void;
  children: any;
}) {
  return (
    <div className="overlay">
      <section className="modal" role="dialog" aria-modal="true">
        <button className="x" onClick={close} aria-label="Close">
          ×
        </button>
        <h2>{title}</h2>
        {children}
      </section>
    </div>
  );
}
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
export const eventSide = (kind: string) => kind === "comment" || kind === "input" ? "sent" : "received";
export const mergeNotifications = (current: any[], incoming: any[]) => [...current, ...incoming.filter((x) => !current.some((y) => y.id === x.id))];
export const canComment = (state: string) =>
  state === "in_progress" || state === "blocked";
export const jobDetail = (x: any) => ({ ...x.job, events: x.events });
export const columnPatch = (form: any) => ({ projectId: Number(form.projectId) });
export const boardLocation = (id: number) => `?board=${id}`;
export function parseLocation(search: string) {
  const q = new URLSearchParams(search), token = q.get("invite"), id = Number(q.get("workspace")), boardId = Number(q.get("board"));
  if (token) return { view: "invitation", token };
  if (id) { const requested = q.get("tab") || "Info", tab = ["Info", "Projects", "Boards", "Users"].includes(requested) ? requested : "Info"; return { view: "workspace", workspaceId: id, tab }; }
  if (q.has("workspaces")) return { view: "workspaces" };
  return { view: "board", ...(boardId ? { boardId } : {}) };
}
function JobDetail({
  job,
  close,
  refresh,
}: {
  job: any;
  close: () => void;
  refresh: () => void;
}) {
  const [d, setD] = useState<any>(),
    [done, setDone] = useState(job.done_definition),
    [input, setInput] = useState(""),
    [comment, setComment] = useState(""),
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
    await api(`/jobs/${job.id}/${a}`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    refresh();
    close();
  };
  return (
    <Dialog title="Job detail" close={close}>
      <p>
        <b>{j.state}</b> · {j.cli_tool} · attempt {j.attempt_count}
      </p>
      <h3>Task</h3>
      <p>{j.task}</p>
      <label>
        Done definition
        <textarea
          disabled={j.state === "done"}
          value={done}
          onChange={(e) => setDone(e.target.value)}
        />
      </label>
      {j.state !== "done" && (
        <button
          onClick={async () => {
            await api("/jobs/" + job.id, {
              method: "PATCH",
              body: JSON.stringify({ done_definition: done }),
            });
            refresh();
          }}
        >
          Save
        </button>
      )}
      {j.warning && <p role="alert">{j.warning}</p>}
      <button onClick={() => action("retry")}>Retry job</button>
      <button
        onClick={async () => {
          await api("/jobs/" + job.id, { method: "DELETE" });
          refresh();
          close();
        }}
      >
        Archive job
      </button>
      <h3>Timeline</h3>
      <div className="conversation">
        {j.events?.length ? j.events.map((e: any) => <div key={e.id} className={`bubble ${eventSide(e.kind)} ${e.kind}`}><small>{eventSide(e.kind) === "sent" ? "You" : e.kind === "error" ? "Error" : "Agent"}</small><span>{e.content}</span></div>) : <p>No output yet</p>}
      </div>
      {canComment(j.state) && (
        <div className="commentbox">
          <label>
            Reply to session
            <textarea
              maxLength={4000}
              placeholder="Type a comment or instruction…"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
            />
          </label>
          {commentError && <p role="alert">{commentError}</p>}
          <button
            disabled={sending || !comment.trim()}
            onClick={async () => {
              setSending(true);
              setCommentError("");
              try {
                await api(`/jobs/${job.id}/comment`, {
                  method: "POST",
                  body: JSON.stringify({ comment }),
                });
                setComment("");
                setD(jobDetail(await api("/jobs/" + job.id)));
              } catch (e) {
                setCommentError(String(e));
              } finally {
                setSending(false);
              }
            }}
          >
            {sending ? "Sending…" : "Send comment"}
          </button>
        </div>
      )}
      {j.state === "blocked" && (
        <div className="actions">
          <input
            aria-label="Manual input"
            value={input}
            onChange={(e) => setInput(e.target.value)}
          />
          <button onClick={() => action("input", { input })}>Send input</button>
          <button onClick={() => action("approve")}>Approve</button>
          <button onClick={() => action("cancel")}>Cancel to todo</button>
        </div>
      )}
    </Dialog>
  );
}
function LegacyApp() {
  const [me, setMe] = useState<any>(),
    [ws, setWs] = useState<any[]>([]),
    [boards, setBoards] = useState<any[]>([]),
    [board, setBoard] = useState<any>(),
    [cols, setCols] = useState<any[]>([]),
    [dialog, setDialog] = useState(""),
    [error, setError] = useState(""),
    [form, setForm] = useState<any>({}),
    [settings, setSettings] = useState<any>(),
    [job, setJob] = useState<any>(),
    [notifications, setNotifications] = useState<any[]>([]),
    [notificationMore, setNotificationMore] = useState(false),
    [unread, setUnread] = useState(0),
    [toast, setToast] = useState<any>(),
    [pending, setPending] = useState(false),
    pendingRef = useRef(false);
  const load = async () => {
    setWs(await api("/workspaces"));
    const b = await api("/boards");
    setBoards(b);
    const active = b.find((x: any) => x.id === board?.id) || b[0];
    setBoard(active);
    setCols(active ? await api(`/boards/${active.id}/columns`) : []);
  };
  useEffect(() => {
    api("/auth/me")
      .then((x) => {
        setMe(x);
        load();
      })
      .catch(() => setMe(false));
  }, []);
  useEffect(() => {
    if (!me) return;
    let previous = new Set<number>();
    const poll = async () => {
      const page = await api("/notifications?limit=10");
      const fresh = page.notifications.find((n: any) => !previous.has(n.id));
      if (previous.size && fresh) setToast(fresh);
      previous = new Set(page.notifications.map((n: any) => n.id));
      setNotifications(page.notifications); setNotificationMore(page.has_more); setUnread(page.unread);
      await load();
    };
    poll(); const timer = setInterval(poll, 2000); return () => clearInterval(timer);
  }, [me]);
  if (me === undefined) return null;
  if (!me) return <Auth />;
  const openNotification = async (n: any) => {
    await api(`/notifications/${n.id}`, {method:"PATCH", body:JSON.stringify({read:true})});
    setNotifications(notifications.map(x => x.id === n.id ? {...x, read:true} : x)); setUnread(Math.max(0, unread-Number(!n.read))); setToast(undefined);
    if (n.job_id) setJob(jobDetail(await api(`/jobs/${n.job_id}`)));
  };
  const openJob = async (c?: any) => {
    if (pendingRef.current) return;
    pendingRef.current = true;
    setError("");
    setPending(true);
    try {
      const target =
        c ||
        (await jobColumn(cols, () =>
          api(`/boards/${board.id}/columns`, {
            method: "POST",
            body: JSON.stringify({
              name: "",
              worktreeEnabled: false,
              worktreeName: "",
            }),
          }),
        ));
      setForm({ columnId: target.id, task: "", doneDefinition: "" });
      setDialog("job");
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  };
  const submit = async () => {
    try {
      if (dialog === "workspace")
        await api("/workspaces", {
          method: "POST",
          body: JSON.stringify({
            name: form.name,
            projectDirectory: form.projectDirectory,
          }),
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
            worktreeEnabled: !!form.worktreeEnabled,
            worktreeName: form.worktreeName || "",
          }),
        });
      if (dialog === "job")
        await api(`/columns/${form.columnId}/jobs`, {
          method: "POST",
          body: JSON.stringify({
            task: form.task,
            doneDefinition: form.doneDefinition,
          }),
        });
      setDialog("");
      setForm({});
      await load();
    } catch (e) {
      setError(String(e));
    }
  };
  return (
    <>
      <header>
        <h1>Paragentix</h1>
        <nav>
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
          <button onClick={() => setDialog("workspace")}>New workspace</button>
          <button onClick={() => setDialog("board")}>New board</button>
        </nav>
        <details className="notifications">
          <summary aria-label="Notifications">🔔{unread > 0 && <b>{unread}</b>}</summary>
          <div className="notificationmenu" onScroll={async e=>{const el=e.currentTarget;if(notificationMore&&el.scrollTop+el.clientHeight>=el.scrollHeight-20){setNotificationMore(false);const page=await api(`/notifications?limit=10&before=${notifications.at(-1)?.id}`);setNotifications(mergeNotifications(notifications,page.notifications));setNotificationMore(page.has_more)}}}>
            <button onClick={async()=>{await api("/notifications/mark-unread",{method:"POST",body:"{}"});setNotifications(notifications.map(n=>({...n,read:false})));setUnread(notifications.length)}}>Mark unread</button>
            {notifications.map(n=><button key={n.id} className={n.read ? "read" : ""} onClick={()=>openNotification(n)}><strong>{n.title}</strong><small>{n.created_at}</small></button>)}
            {notificationMore && <small className="loading">Scroll for more</small>}
          </div>
        </details>
        <details className="account">
          <summary aria-label="Account menu">
            <span aria-hidden="true">{me.email.slice(0, 1).toUpperCase()}</span>
          </summary>
          <div className="accountmenu">
            <strong>{me.email.split("@")[0]}</strong>
            <small>{me.email}</small>
            <button onClick={() => setDialog("profile")}>Profile</button>
            <button
              onClick={async () => {
                setSettings(await api("/settings"));
                setDialog("settings");
              }}
            >
              Settings
            </button>
            <button
              onClick={async () => {
                await api("/auth/logout", { method: "POST" });
                location.reload();
              }}
            >
              Sign out
            </button>
          </div>
        </details>
      </header>
      {board && (
        <>
          <p className="workspace">
            <b>{board.workspaceName}</b> — Project directory:{" "}
            <code>{board.projectDirectory}</code>
          </p>
          <div className="boardactions">
            <button disabled={pending} onClick={() => openJob()}>
              + Add job
            </button>
            {error && <p role="alert">{error}</p>}
          </div>
        </>
      )}
      <main className="board">
        {cols.map((c) => (
          <section className="lane" key={c.id}>
            <div className="lanehead">
              <b>{c.name}</b>
              <button
                onClick={async () => {
                  await archiveColumn(c.id);
                  await load();
                }}
              >
                Archive
              </button>
            </div>
            <small>
              {c.worktreeEnabled
                ? `Worktree: ${c.worktreeName}`
                : `Project directory: ${board.projectDirectory}`}
            </small>
            {c.jobs?.map((j: any) => (
              <article
                onClick={() => setJob(j)}
                className={"job " + j.state}
                key={j.id}
              >
                <b>{j.task}</b>
                <small>
                  {j.state} · {j.cli_tool}
                </small>
              </article>
            ))}
            <button className="add" onClick={() => openJob(c)}>
              + Add job
            </button>
          </section>
        ))}
        {board && (
          <button
            className="newlane"
            onClick={() => {
              setForm({ name: "", worktreeEnabled: false });
              setDialog("column");
            }}
          >
            + Add column
          </button>
        )}
      </main>
      {toast && <button className={`toast ${toast.kind}`} onClick={()=>openNotification(toast)}>{toast.title}</button>}
      {job && (
        <JobDetail job={job} close={() => setJob(undefined)} refresh={load} />
      )}{" "}
      {dialog && (
        <Dialog
          title={
            dialog === "settings"
              ? "Settings"
              : dialog === "profile"
                ? "Profile"
                : `Create ${dialog}`
          }
          close={() => {
            setDialog("");
            setError("");
          }}
        >
          {error && <p role="alert">{error}</p>}
          {dialog === "profile" && (
            <>
              <p>
                <b>{me.email.split("@")[0]}</b>
              </p>
              <p>{me.email}</p>
            </>
          )}
          {dialog === "job" && (
            <>
              <label>
                Task
                <textarea
                  required
                  value={form.task || ""}
                  onChange={(e) => setForm({ ...form, task: e.target.value })}
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
              <button onClick={submit}>Create job</button>
            </>
          )}
          {dialog === "workspace" && (
            <>
              <label>
                Name
                <input
                  value={form.name || ""}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </label>
              <label>
                Project directory
                <input
                  value={form.projectDirectory || ""}
                  onChange={(e) =>
                    setForm({ ...form, projectDirectory: e.target.value })
                  }
                />
              </label>
              <p>
                Server-local existing directory inside the configured workspace
                root.
              </p>
              <button onClick={submit}>Create workspace</button>
            </>
          )}
          {dialog === "board" && (
            <>
              <label>
                Name
                <input
                  value={form.name || ""}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </label>
              <label>
                Workspace
                <select
                  value={form.workspaceId || ""}
                  onChange={(e) =>
                    setForm({ ...form, workspaceId: e.target.value })
                  }
                >
                  <option value="">Select…</option>
                  {ws.map((w) => (
                    <option key={w.id} value={w.id}>
                      {w.name} — {w.projectDirectory}
                    </option>
                  ))}
                </select>
              </label>
              <button onClick={submit}>Create board</button>
            </>
          )}
          {dialog === "column" && (
            <>
              <label>
                Column name (blank generates a lowercase word combination)
                <input
                  value={form.name || ""}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={!!form.worktreeEnabled}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      worktreeEnabled: e.target.checked,
                      worktreeName: e.target.checked ? form.worktreeName : "",
                    })
                  }
                />{" "}
                Create Git worktree
              </label>
              <label>
                Worktree name
                <input
                  disabled={!form.worktreeEnabled}
                  value={form.worktreeName || ""}
                  placeholder="Defaults to column-name slug"
                  onChange={(e) =>
                    setForm({ ...form, worktreeName: e.target.value })
                  }
                />
              </label>
              <small>Must match ^[a-z0-9]+(?:-[a-z0-9]+)*$</small>
              <button onClick={submit}>Create column</button>
            </>
          )}
          {dialog === "settings" && settings && (
            <>
              <label>
                Default delegate
                <select
                  value={settings.default_cli}
                  onChange={(e) =>
                    setSettings({ ...settings, default_cli: e.target.value })
                  }
                >
                  <option value="codex">Codex</option>
                  <option value="claude">Claude Code</option>
                  <option value="hermes">Hermes API</option>
                </select>
              </label>
              {settings.default_cli === "hermes" && (
                <>
                  <label>
                    Hermes API URL
                    <input
                      type="url"
                      required
                      value={settings.hermes_url || ""}
                      placeholder="http://127.0.0.1:8642"
                      onChange={(e) =>
                        setSettings({ ...settings, hermes_url: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    Hermes API key
                    <input
                      type="password"
                      required={!settings.hermes_api_key_set}
                      value={settings.hermes_api_key || ""}
                      placeholder={
                        settings.hermes_api_key_set
                          ? "Saved — leave blank to keep"
                          : "Required"
                      }
                      onChange={(e) =>
                        setSettings({
                          ...settings,
                          hermes_api_key: e.target.value,
                        })
                      }
                    />
                  </label>
                  <label>
                    Hermes model
                    <input
                      required
                      value={settings.hermes_model || "hermes-agent"}
                      onChange={(e) =>
                        setSettings({
                          ...settings,
                          hermes_model: e.target.value,
                        })
                      }
                    />
                  </label>
                </>
              )}
              {["codex", "claude"].map((t) => (
                <label key={t}>
                  Custom {t} command
                  <input
                    value={settings.commands[t]}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        commands: { ...settings.commands, [t]: e.target.value },
                      })
                    }
                  />
                </label>
              ))}
              <p>
                Commands are parsed to argv without a shell and run with
                service-user privileges. Example: codex -m gpt-5.6 --yolo
              </p>
              <button
                onClick={async () => {
                  try {
                    await api("/settings", {
                      method: "PATCH",
                      body: JSON.stringify(settings),
                    });
                    setDialog("");
                  } catch (e) {
                    setError(String(e));
                  }
                }}
              >
                Save
              </button>
            </>
          )}
        </Dialog>
      )}
    </>
  );
}
export function closeDetails(ref: { current: HTMLDetailsElement | null }) { if (ref.current) ref.current.open = false; }
export function NotificationCenter({notifications,unread,more,onOpen,onMarkUnread,onLoadMore}:{notifications:any[];unread:number;more:boolean;onOpen:(n:any)=>void;onMarkUnread:()=>void;onLoadMore:()=>void}) {
  const menu = useRef<HTMLDetailsElement>(null);
  useEffect(()=>{const outside=(e:PointerEvent)=>{if(menu.current&&!menu.current.contains(e.target as Node))closeDetails(menu)};document.addEventListener('pointerdown',outside);return()=>document.removeEventListener('pointerdown',outside)},[]);
  return <details ref={menu} className="notifications"><summary className="notification-bell" aria-label="Notifications">🔔{unread>0&&<b>{unread}</b>}</summary><div className="notificationmenu" onScroll={e=>{const el=e.currentTarget;if(more&&el.scrollTop+el.clientHeight>=el.scrollHeight-20)onLoadMore()}}><button onClick={onMarkUnread}>Mark unread</button>{notifications.map(n=><button key={n.id} className={n.read?"read":""} onClick={()=>onOpen(n)}><strong>{n.title}</strong><small>{n.created_at}</small></button>)}{more&&<small>Scroll for more</small>}</div></details>;
}
function App() {
  const [me,setMe]=useState<any>(),[ws,setWs]=useState<any[]>([]),[boards,setBoards]=useState<any[]>([]),[board,setBoard]=useState<any>(),[cols,setCols]=useState<any[]>([]),[view,setView]=useState("board"),[detail,setDetail]=useState<any>(),[tab,setTab]=useState("Info"),[items,setItems]=useState<any[]>([]),[dialog,setDialog]=useState(""),[form,setForm]=useState<any>({}),[error,setError]=useState(""),[notifications,setNotifications]=useState<any[]>([]),[notificationMore,setNotificationMore]=useState(false),[unread,setUnread]=useState(0),[job,setJob]=useState<any>(); const menu=useRef<HTMLDetailsElement>(null);
  const load=async()=>{const w=await api('/workspaces'),b=await api('/boards');setWs(w);setBoards(b);const route=parseLocation(location.search);const active=b.find((x:any)=>x.id===((route as any).boardId||board?.id))||b[0];setBoard(active);setCols(active?await api(`/boards/${active.id}/columns`):[])};
  const restore=async()=>{const route=parseLocation(location.search);setView(route.view);if(route.view==='workspace'){const d=await api('/workspaces/'+route.workspaceId);setDetail(d);setTab(route.tab!);setItems(route.tab==='Info'?[]:await api(`/workspaces/${route.workspaceId}/${route.tab!.toLowerCase()}`))}};
  useEffect(()=>{api('/auth/me').then(async x=>{setMe(x);try{await load();const route=parseLocation(location.search);if(route.view==='invitation'){await api(`/invitations/${encodeURIComponent(route.token!)}`,{method:'POST'});history.replaceState({},'', '?workspaces=1');setView('workspaces')}else await restore()}catch(e){setError(String(e))}}).catch(()=>setMe(false))},[]);
  useEffect(()=>{const outside=(e:PointerEvent)=>{if(menu.current&&!menu.current.contains(e.target as Node))closeDetails(menu)};document.addEventListener('pointerdown',outside);return()=>document.removeEventListener('pointerdown',outside)},[]);
  useEffect(()=>{const pop=()=>restore().catch(e=>setError(String(e)));addEventListener('popstate',pop);return()=>removeEventListener('popstate',pop)},[]);
  useEffect(()=>{if(!me)return;const poll=()=>api('/notifications?limit=10').then(p=>{setNotifications(p.notifications);setNotificationMore(p.has_more);setUnread(p.unread)}).catch(()=>{});poll();const timer=setInterval(poll,2000);return()=>clearInterval(timer)},[me]);
  const openWorkspace=async(w:any)=>{setDetail(await api('/workspaces/'+w.id));setView('workspace');setTab('Info');setItems([]);history.pushState({},'',`?workspace=${w.id}&tab=Info`)};
  const chooseTab=async(t:string)=>{setTab(t);history.pushState({},'',`?workspace=${detail.id}&tab=${t}`);setItems(t==='Info'?[]:await api(`/workspaces/${detail.id}/${t.toLowerCase()}`))};
  const submit=async()=>{try{if(dialog==='workspace')await api('/workspaces',{method:'POST',body:JSON.stringify({name:form.name})});if(dialog==='edit workspace')await api('/workspaces/'+detail.id,{method:'PATCH',body:JSON.stringify({name:form.name})});if(dialog==='project')await api(`/workspaces/${detail.id}/projects`,{method:'POST',body:JSON.stringify(form)});if(dialog==='edit project')await api('/projects/'+form.id,{method:'PATCH',body:JSON.stringify(form)});if(dialog==='board')await api('/boards',{method:'POST',body:JSON.stringify({name:form.name,workspaceId:Number(form.workspaceId)})});if(dialog==='column')await api(`/boards/${board.id}/columns`,{method:'POST',body:JSON.stringify({name:form.name,projectId:Number(form.projectId),worktreeEnabled:!!form.worktreeEnabled,worktreeName:form.worktreeName||''})});if(dialog==='edit column')await api(`/columns/${form.id}`,{method:'PATCH',body:JSON.stringify(columnPatch(form))});if(dialog==='invite')await api(`/workspaces/${detail.id}/invitations`,{method:'POST',body:JSON.stringify({email:form.email})});if(dialog==='remove')await api(`/workspaces/${detail.id}/members/${form.id}`,{method:'DELETE'});setDialog('');setForm({});await load();if(view==='workspace')await chooseTab(tab)}catch(e){setError(String(e))}};
  const route=parseLocation(location.search);if(me===undefined)return null; if(!me)return <Auth invitation={route.view==='invitation'?route.token:undefined}/>;
  const openNotification=async(n:any)=>{await api(`/notifications/${n.id}`,{method:'PATCH',body:JSON.stringify({read:true})});setNotifications(notifications.map(x=>x.id===n.id?{...x,read:true}:x));setUnread(Math.max(0,unread-Number(!n.read)));if(n.job_id)setJob(jobDetail(await api(`/jobs/${n.job_id}`)))};
  return <><header><h1>Paragentix</h1><nav>{view==='board'&&<select aria-label="Workspace board" value={board?.id||''} onChange={async e=>{const b=boards.find(x=>x.id===Number(e.target.value));setBoard(b);setCols(await api(`/boards/${b.id}/columns`))}}>{boards.map(b=><option key={b.id} value={b.id}>{b.workspaceName} / {b.name}</option>)}</select>}<button onClick={()=>{setForm({workspaceId:ws[0]?.id});setDialog('board')}}>Create Board</button></nav><div className="header-actions"><NotificationCenter notifications={notifications} unread={unread} more={notificationMore} onOpen={openNotification} onMarkUnread={async()=>{await api('/notifications/mark-unread',{method:'POST',body:'{}'});setNotifications(notifications.map(n=>({...n,read:false})));setUnread(notifications.length)}} onLoadMore={async()=>{setNotificationMore(false);const p=await api(`/notifications?limit=10&before=${notifications.at(-1)?.id}`);setNotifications(mergeNotifications(notifications,p.notifications));setNotificationMore(p.has_more)}}/><details ref={menu} className="account"><summary aria-label="Account menu"><span>{me.email[0].toUpperCase()}</span></summary><div className="accountmenu"><strong>{me.email}</strong><button onClick={()=>{closeDetails(menu);setView('workspaces');history.pushState({},'','?workspaces=1')}}>Workspaces</button><button onClick={()=>{closeDetails(menu);setDialog('profile')}}>Profile</button><button onClick={async()=>{closeDetails(menu);await api('/auth/logout',{method:'POST'});location.reload()}}>Sign out</button></div></details></div></header>
  {view==='workspaces'&&<main className="page"><div className="pagehead"><h2>Workspaces</h2><button onClick={()=>{setForm({});setDialog('workspace')}}>New workspace</button></div>{ws.map(w=><section className="panel" key={w.id}><h3>{w.name}</h3><p>{w.role} · {w.projectCount} projects · {w.memberCount} users</p><button onClick={()=>openWorkspace(w)}>Open workspace</button></section>)}</main>}
  {view==='workspace'&&detail&&<main className="page"><button onClick={()=>{setView('workspaces');history.pushState({},'','?workspaces=1')}}>← Workspaces</button><h2>{detail.name}</h2><div className="tabs" role="tablist">{['Info','Projects','Boards','Users'].map(t=><button role="tab" aria-selected={tab===t} onClick={()=>chooseTab(t)}>{t}</button>)}</div>{tab==='Info'&&<section className="panel"><p>Role: {detail.role}</p><p>{detail.projectCount} projects · {detail.memberCount} users</p>{detail.role==='owner'&&<button onClick={()=>{setForm({name:detail.name});setDialog('edit workspace')}}>Edit workspace</button>}</section>}{tab==='Projects'&&<><div className="pagehead"><h3>Projects</h3>{detail.role==='owner'&&<button onClick={()=>{setForm({});setDialog('project')}}>New project</button>}</div>{items.map(p=><section className="panel"><b>{p.name}</b><code>{p.directory}</code>{detail.role==='owner'&&<button onClick={()=>{setForm(p);setDialog('edit project')}}>Edit</button>}</section>)}</>}{tab==='Boards'&&items.map(b=><section className="panel"><b>{b.name}</b><span>{b.columnCount} columns</span><button onClick={()=>{setBoard(boards.find(x=>x.id===b.id));setView('board');history.pushState({},'',boardLocation(b.id));api(`/boards/${b.id}/columns`).then(setCols)}}>Open board</button></section>)}{tab==='Users'&&<><div className="pagehead"><h3>Users</h3>{detail.role==='owner'&&<button onClick={()=>{setForm({});setDialog('invite')}}>Invite user</button>}</div>{items.map(m=><section className="panel"><b>{m.email}</b><span>{m.role} · joined {m.joinedAt}</span>{detail.role==='owner'&&m.id!==me.id&&<button onClick={()=>{setForm(m);setDialog('remove')}}>Remove</button>}</section>)}</>}</main>}
  {view==='board'&&<main className="board">{cols.map(c=><section className="lane"><div className="lanehead"><b>{c.name}</b><button onClick={async()=>{setForm({...c,projects:await api(`/workspaces/${board.workspaceId}/projects`)});setDialog('edit column')}}>Edit</button></div><small>Project: {c.projectName}{c.worktreeEnabled?` · Worktree: ${c.worktreeName}`:''}</small>{c.jobs?.map((j:any)=><article className={'job '+j.state}><b>{j.task}</b></article>)}</section>)}{board&&<button className="newlane" onClick={async()=>{const projects=await api(`/workspaces/${board.workspaceId}/projects`);setForm({projects,projectId:projects[0]?.id,worktreeEnabled:false});setDialog('column')}}>+ Add column</button>}</main>}
  {dialog&&<Dialog title={dialog} close={()=>setDialog('')}>{error&&<p role="alert">{error}</p>}{dialog==='profile'?<p>{me.email}</p>:dialog==='remove'?<><p>Remove {form.email} from this workspace?</p><button onClick={submit}>Confirm removal</button></>:<>{dialog==='invite'?<label>Email<input type="email" required value={form.email||''} onChange={e=>setForm({...form,email:e.target.value})}/></label>:<label>Name<input required value={form.name||''} onChange={e=>setForm({...form,name:e.target.value})}/></label>}{['project','edit project'].includes(dialog)&&<label>Directory<input required value={form.directory||''} onChange={e=>setForm({...form,directory:e.target.value})}/></label>}{dialog==='board'&&<label>Workspace<select value={form.workspaceId||''} onChange={e=>setForm({...form,workspaceId:e.target.value})}>{ws.map(w=><option value={w.id}>{w.name}</option>)}</select></label>}{['column','edit column'].includes(dialog)&&<><label>Project<select required value={form.projectId||''} onChange={e=>setForm({...form,projectId:e.target.value})}><option value="">Select…</option>{form.projects?.map((p:any)=><option value={p.id}>{p.name}</option>)}</select></label><label><input type="checkbox" checked={!!form.worktreeEnabled} onChange={e=>setForm({...form,worktreeEnabled:e.target.checked})}/> Git worktree</label><label>Worktree name<input disabled={!form.worktreeEnabled} value={form.worktreeName||''} onChange={e=>setForm({...form,worktreeName:e.target.value})}/></label></>}<button disabled={dialog==='column'&&!form.projectId} onClick={submit}>Save</button></>}</Dialog>}
  {job&&<JobDetail job={job} close={()=>setJob(undefined)} refresh={async()=>setJob(jobDetail(await api(`/jobs/${job.id}`)))} />}
  </>;
}
const root = document.getElementById("root");
if (root) createRoot(root).render(<App />);
