// @vitest-environment jsdom
import{describe,it,expect,vi}from'vitest';import{api}from'./src';describe('api',()=>{it('surfaces backend errors',async()=>{vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({error:'locked'}),{status:409,headers:{'Content-Type':'application/json'}})));await expect(api('/jobs/1')).rejects.toThrow('locked')})})
