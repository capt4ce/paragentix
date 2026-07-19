import { useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
export function Auth({ invitation }: { invitation?: string }) {
 const [email,setEmail]=useState(""),[password,setPassword]=useState(""),[signup,setSignup]=useState(false),[error,setError]=useState(""),[loading,setLoading]=useState(false);
 return <main className="auth"><Card><CardHeader><CardTitle>Paragentix</CardTitle></CardHeader><CardContent><form onSubmit={async e=>{e.preventDefault();if(loading)return;setLoading(true);try{await api("/auth/"+(signup?"signup":"login"),{method:"POST",body:JSON.stringify({email,password})});if(invitation)await api(`/invitations/${encodeURIComponent(invitation)}`,{method:"POST"});location.reload()}catch(e){setError(String(e))}finally{setLoading(false)}}}><Label htmlFor="email">Email</Label><Input id="email" type="email" required value={email} onChange={e=>setEmail(e.target.value)}/><Label htmlFor="password">Password</Label><Input id="password" type="password" minLength={8} required value={password} onChange={e=>setPassword(e.target.value)}/>{error&&<p role="alert">{error}</p>}<Button disabled={loading} aria-busy={loading||undefined}>{loading?"Loading…":signup?"Create account":"Sign in"}</Button><Button type="button" variant="link" onClick={()=>setSignup(!signup)}>{signup?"Sign in":"Create account"}</Button></form></CardContent></Card></main>;
}
