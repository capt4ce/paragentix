export const boardLocation = (id: number) => `?board=${id}`;
export const projectLocation = (id: number) => `?project=${id}`;
export const conversationLocation = (jobId: number, conversationId?: number) =>
  `?job=${jobId}${conversationId ? `&conversation=${conversationId}` : ""}`;
export const jobLocation = (jobId: number) => `/jobs/${jobId}`;
export type AppRoute =
  | { view: "invitation"; token: string }
  | { view: "job"; jobId: number }
  | { view: "conversation"; jobId: number; conversationId?: number }
  | { view: "workspace"; workspaceId: number; tab: string }
  | { view: "project"; projectId: number }
  | { view: "projects" }
  | { view: "workspaces" }
  | { view: "board"; boardId?: number };
export function parseLocation(search: string, pathname = ""): AppRoute {
  const directJob = pathname.match(/^\/jobs\/(\d+)\/?$/);
  if (directJob) return { view: "job", jobId: Number(directJob[1]) };
  const q = new URLSearchParams(search), token = q.get("invite"), id = Number(q.get("workspace")), projectId = Number(q.get("project")), boardId = Number(q.get("board")), jobId = Number(q.get("job")), conversationId = Number(q.get("conversation"));
  if (token) return { view: "invitation", token };
  if (jobId) return { view: "conversation", jobId, ...(conversationId ? { conversationId } : {}) };
  if (id) { const requested = q.get("tab") || "Info", tab = ["Info", "Projects", "Boards", "Users", "Settings"].includes(requested) ? requested : "Info"; return { view: "workspace", workspaceId: id, tab }; }
  if (projectId) return { view: "project", projectId };
  if (q.has("projects")) return { view: "projects" };
  if (q.has("workspaces")) return { view: "workspaces" };
  return { view: "board", ...(boardId ? { boardId } : {}) };
}
