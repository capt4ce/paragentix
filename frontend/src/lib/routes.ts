export const boardLocation = (id: number) => `?board=${id}`;
export const projectLocation = (id: number) => `?project=${id}`;
export const jobLocation = (id: number) => `/jobs/${id}`;
export const conversationLocation = (jobId: number, conversationId?: number) =>
  `?job=${jobId}${conversationId ? `&conversation=${conversationId}` : ""}`;
export type AppRoute =
  | { view: "invitation"; token: string }
  | { view: "conversation"; jobId: number; conversationId?: number }
  | { view: "job"; jobId: number }
  | { view: "workspace"; workspaceId: number; tab: string }
  | { view: "project"; projectId: number }
  | { view: "projects" }
  | { view: "workspaces" }
  | { view: "board"; boardId?: number };
export function parseLocation(search: string, pathname = location.pathname): AppRoute {
  const q = new URLSearchParams(search), token = q.get("invite"), id = Number(q.get("workspace")), projectId = Number(q.get("project")), boardId = Number(q.get("board")), jobId = Number(q.get("job")), conversationId = Number(q.get("conversation"));
  const pathJobId = pathname.match(/^\/jobs\/(\d+)\/?$/)?.[1];
  if (token) return { view: "invitation", token };
  if (jobId) return { view: "conversation", jobId, ...(conversationId ? { conversationId } : {}) };
  if (pathJobId) return { view: "job", jobId: Number(pathJobId) };
  if (id) { const requested = q.get("tab") || "Info", tab = ["Info", "Projects", "Boards", "Users", "Settings"].includes(requested) ? requested : "Info"; return { view: "workspace", workspaceId: id, tab }; }
  if (projectId) return { view: "project", projectId };
  if (q.has("projects")) return { view: "projects" };
  if (q.has("workspaces")) return { view: "workspaces" };
  return { view: "board", ...(boardId ? { boardId } : {}) };
}
