import { Badge } from "@/components/ui/badge";
const labels: Record<string,string> = { todo:"Todo", in_progress:"In progress", blocked:"Blocked", done:"Done" };
export function StatusBadge({state}:{state:string}) { return <Badge className="w-fit" data-status={state}><span className="status-dot" aria-hidden="true" />{labels[state] ?? state}</Badge>; }
