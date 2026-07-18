export const base = import.meta.env.BASE_URL;
export async function api(p: string, o?: RequestInit) {
  const r = await fetch(base + "api" + p, { ...o, headers: { "Content-Type": "application/json", ...o?.headers } });
  if (!r.ok) throw Error((await r.json()).error);
  return r.status === 204 ? null : r.json();
}
