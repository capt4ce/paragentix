import { useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { submitFormShortcut } from "@/lib/forms";
export function Auth({ invitation: _invitation }: { invitation?: string }) {
 const [email,setEmail]=useState(""),[password,setPassword]=useState(""),[signup,setSignup]=useState(false),[error,setError]=useState(""),[loading,setLoading]=useState(false);
 return <main className="auth command-center command-auth-surface"><section className="auth-intro"><b>PARA<span>GENTIX</span></b><h1>Command your autonomous workforce.</h1><p>Plan, dispatch, and review work from one operational console.</p></section><Card className="auth-card border-0"><CardHeader><small>SECURE ACCESS</small><CardTitle>{signup?"Create your operator account":"Welcome back"}</CardTitle></CardHeader><CardContent><form onKeyDown={submitFormShortcut} onSubmit={async e=>{e.preventDefault();if(loading)return;setLoading(true);try{await api("/auth/"+(signup?"signup":"login"),{method:"POST",body:JSON.stringify({email,password})});location.reload()}catch(e){setError(String(e))}finally{setLoading(false)}}}><Label htmlFor="email">Email</Label><Input id="email" type="email" required value={email} onChange={e=>setEmail(e.target.value)}/><Label htmlFor="password">Password</Label><Input id="password" type="password" minLength={8} required value={password} onChange={e=>setPassword(e.target.value)}/>{error&&<p role="alert">{error}</p>}<Button disabled={loading} aria-busy={loading||undefined}>{loading?"Loading…":signup?"Create account":"Sign in"}</Button><Button type="button" variant="link" onClick={()=>setSignup(!signup)}>{signup?"Sign in":"Create account"}</Button></form></CardContent></Card></main>;
}
