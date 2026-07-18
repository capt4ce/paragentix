export const boardLocation = (id: number) => `?board=${id}`;
export function parseLocation(search: string) {
  const q = new URLSearchParams(search), token = q.get("invite"), id = Number(q.get("workspace")), boardId = Number(q.get("board"));
  if (token) return { view: "invitation", token };
  if (id) { const requested = q.get("tab") || "Info", tab = ["Info", "Projects", "Boards", "Users"].includes(requested) ? requested : "Info"; return { view: "workspace", workspaceId: id, tab }; }
  if (q.has("workspaces")) return { view: "workspaces" };
  return { view: "board", ...(boardId ? { boardId } : {}) };
}
