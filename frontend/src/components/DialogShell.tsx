import type { ReactNode } from "react";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
export function DialogShell({ title, close, children, inspector = false, preventOutsideClose = false }: { title: string; close: () => void; children?: ReactNode; inspector?: boolean; preventOutsideClose?: boolean }) {
  return <Dialog open onOpenChange={open => { if (!open) close(); }}><DialogContent
    className={inspector ? "inspector" : "modal"}
    onPointerDownOutside={event => { if (preventOutsideClose) event.preventDefault(); }}
    onInteractOutside={event => { if (preventOutsideClose) event.preventDefault(); }}
    onEscapeKeyDown={event => { if (preventOutsideClose) event.preventDefault(); }}
  ><DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>{children}</DialogContent></Dialog>;
}
