import { useEffect, useState, type ReactNode } from "react";
import { BriefcaseBusiness, Columns3, Menu, PanelsTopLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTitle, DialogTrigger } from "@/components/ui/dialog";

type Destination = "board" | "projects" | "workspaces";
const destinations: Array<{ id: Destination; label: string; icon: typeof Columns3 }> = [
  { id: "board", label: "Board", icon: Columns3 },
  { id: "projects", label: "Projects", icon: BriefcaseBusiness },
  { id: "workspaces", label: "Workspaces", icon: PanelsTopLeft },
];

export function AppShell({ active, email, onNavigate, topbar, children }: {
  active: Destination;
  email: string;
  onNavigate: (destination: Destination) => void;
  topbar?: ReactNode;
  children?: ReactNode;
}) {
  const [drawer, setDrawer] = useState(false);
  useEffect(() => {
    if (!drawer) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = previous; };
  }, [drawer]);
  const navigation = (mobile = false) => (
    <nav className="command-nav" aria-label={mobile ? "Mobile primary" : "Primary"}>
      {destinations.map(({ id, label, icon: Icon }) => (
        mobile ? <DialogClose key={id} asChild><button type="button" className={active === id ? "active" : ""} aria-current={active === id ? "page" : undefined} onClick={() => onNavigate(id)}><Icon aria-hidden="true" /> <span>{label}</span></button></DialogClose>
          : <button key={id} type="button" className={active === id ? "active" : ""} aria-current={active === id ? "page" : undefined} onClick={() => onNavigate(id)}><Icon aria-hidden="true" /> <span>{label}</span></button>
      ))}
    </nav>
  );
  return <div className="command-shell">
    <aside className="command-sidebar">
      <a className="command-brand" href="/" aria-label="Paragentix home">PARA<span>GENTIX</span></a>
      {navigation()}
      <div className="command-operator"><small>OPERATOR</small><strong>{email}</strong></div>
    </aside>
    <div className="command-main">
      <div className="command-mobile-bar">
        <Dialog open={drawer} onOpenChange={setDrawer}><DialogTrigger asChild><Button type="button" variant="outline" size="icon" aria-label="Open navigation"><Menu /></Button></DialogTrigger>
          <DialogContent className="command-drawer" aria-label="Navigation">
            <DialogTitle className="sr-only">Navigation</DialogTitle>
            <div className="command-drawer-head"><span className="command-brand">PARA<span>GENTIX</span></span></div>
            {navigation(true)}
            <div className="command-operator"><small>OPERATOR</small><strong>{email}</strong></div>
          </DialogContent>
        </Dialog>
        <a className="command-brand" href="/">PARA<span>GENTIX</span></a>
      </div>
      {topbar && <header className="command-topbar">{topbar}</header>}
      {children}
    </div>
  </div>;
}
