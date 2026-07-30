import type { ReactNode } from "react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

type DialogShellProps = {
  title: string;
  close: () => void;
  children?: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  footer?: ReactNode;
  open?: boolean;
  inspector?: boolean;
  preventOutsideClose?: boolean;
  className?: string;
};

export function DialogShell({
  title,
  close,
  children,
  description,
  error,
  footer,
  open = true,
  inspector = false,
  preventOutsideClose = false,
  className,
}: DialogShellProps) {
  return <Dialog open={open} onOpenChange={nextOpen => { if (!nextOpen) close(); }}><DialogContent
    className={cn(inspector ? "inspector" : "modal", className)}
    onPointerDownOutside={event => { if (preventOutsideClose) event.preventDefault(); }}
    onInteractOutside={event => { if (preventOutsideClose) event.preventDefault(); }}
    onEscapeKeyDown={event => { if (preventOutsideClose) event.preventDefault(); }}
  >
    <DialogHeader className="dialog-shell-header">
      <DialogTitle>{title}</DialogTitle>
      {description && <DialogDescription>{description}</DialogDescription>}
    </DialogHeader>
    <div className="dialog-shell-body">{children}</div>
    {error && <p className="dialog-shell-error" role="alert">{error}</p>}
    {footer && <DialogFooter className="dialog-shell-footer">{footer}</DialogFooter>}
  </DialogContent></Dialog>;
}
