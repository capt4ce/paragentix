import type { ReactNode } from "react";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
export function DialogShell({ title, close, children, inspector = false }: { title: string; close: () => void; children?: ReactNode; inspector?: boolean }) {
  return <Dialog open onOpenChange={open => { if (!open) close(); }}><DialogContent className={inspector ? "inspector" : "modal"}><DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>{children}</DialogContent></Dialog>;
}
