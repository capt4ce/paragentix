export type JobState = "todo" | "in_progress" | "blocked" | "done";
export interface Job { id:number; task:string; state:JobState; creatorName:string; done_definition?:string; attempt_count?:number; session_id?:string; warning?:string; events?:JobEvent[] }
export interface JobEvent { id:number; kind:string; content:string }
export interface Column { id:number; name:string; projectName:string; projectId:number; worktreeEnabled:boolean; worktreeName?:string; jobs?:Job[] }
export interface Workspace { id:number; name:string; role:string; projectCount:number; memberCount:number; projectDirectory?:string }
export interface Board { id:number; name:string; workspaceId:number; workspaceName:string }
export interface Notification { id:number; title:string; created_at:string; read:boolean; job_id?:number }
