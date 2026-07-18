import { Bell } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
export function NotificationCenter({notifications,unread,more,onOpen,onMarkUnread,onLoadMore}:{notifications:any[];unread:number;more:boolean;onOpen:(n:any)=>void;onMarkUnread:()=>void;onLoadMore:()=>void}) {
 return <DropdownMenu><DropdownMenuTrigger asChild><Button variant="ghost" size="icon" className="notification-bell" aria-label="Notifications"><Bell/>{unread>0&&<b>{unread}</b>}</Button></DropdownMenuTrigger><DropdownMenuContent className="notificationmenu" onScroll={e=>{const el=e.currentTarget;if(more&&el.scrollTop+el.clientHeight>=el.scrollHeight-20)onLoadMore()}}><DropdownMenuItem onSelect={onMarkUnread}>Mark unread</DropdownMenuItem>{notifications.map(n=><DropdownMenuItem key={n.id} className={n.read?"read":""} onSelect={()=>onOpen(n)}><strong>{n.title}</strong><small>{n.created_at}</small></DropdownMenuItem>)}{more&&<small>Scroll for more</small>}</DropdownMenuContent></DropdownMenu>;
}
