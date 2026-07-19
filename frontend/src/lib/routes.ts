export const boardLocation = (id: number) => `?board=${id}`;
export const projectLocation = (id: number) => `?project=${id}`;
export function parseLocation(search: string) {
  const q = new URLSearchParams(search), token = q.get("invite"), id = Number(q.get("workspace")), projectId = Number(q.get("project")), boardId = Number(q.get("board"));
  if (token) return { view: "invitation", token };
  if (id) { const requested = q.get("tab") || "Info", tab = ["Info", "Projects", "Boards", "Users", "Settings"].includes(requested) ? requested : "Info"; return { view: "workspace", workspaceId: id, tab }; }
  if (projectId) return { view: "project", projectId };
  if (q.has("projects")) return { view: "projects" };
  if (q.has("workspaces")) return { view: "workspaces" };
  return { view: "board", ...(boardId ? { boardId } : {}) };
}
