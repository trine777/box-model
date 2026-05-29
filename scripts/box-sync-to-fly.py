#!/usr/bin/env python3
# R8: Mac → Fly one-way disaster-recovery sync.
# Mac (tailnet, no token) is source of truth; Fly (Bearer) is a read-only
# replica. v1: add-only (Fly never loses data, may lag on in-place edits).
import json, urllib.request, time, sys, os

MAC = "http://100.83.33.126:7777"          # tailnet, no token
FLY = "https://box-mcp-trine.fly.dev"
FTOK = open(os.path.expanduser("~/.box-fly-token")).read().strip()

def mcp(url, tok, m, pa, sid=None, notify=False):
    body={"jsonrpc":"2.0","method":m,"params":pa}
    if not notify: body["id"]=1
    h={"Content-Type":"application/json","Accept":"application/json, text/event-stream"}
    if tok: h["Authorization"]=f"Bearer {tok}"
    if sid: h["Mcp-Session-Id"]=sid
    req=urllib.request.Request(url+"/mcp",data=json.dumps(body).encode(),headers=h,method="POST")
    with urllib.request.urlopen(req,timeout=40) as r:
        ns=r.headers.get("Mcp-Session-Id"); raw=r.read().decode()
    if notify: return None,ns
    for ln in raw.splitlines():
        if ln.startswith("data: "): return json.loads(ln[6:]),ns
    return json.loads(raw),ns
def sess(url,tok):
    _,sid=mcp(url,tok,"initialize",{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"dr-sync","version":"0.1"}})
    mcp(url,tok,"notifications/initialized",{},sid,notify=True); return sid
def tool(url,tok,sid,n,a,retries=3):
    for i in range(retries):
        try:
            r,_=mcp(url,tok,"tools/call",{"name":n,"arguments":a},sid); res=r.get("result",{})
            return {"_err":res["content"][0]["text"]} if res.get("isError") else json.loads(res["content"][0]["text"])
        except Exception as e:
            if i==retries-1: return {"_err":str(e)}
            time.sleep(2)

def log(m): print(f"[{time.strftime('%H:%M:%S')}] {m}", flush=True)

def main():
    t0=time.time()
    msid=sess(MAC,None)
    fsid=sess(FLY,FTOK)
    # Mac box list via box_globes (caller-scoped to trine)
    g=tool(MAC,None,msid,"box_globes",{})
    mac_boxes=[]
    for sphere in g.get("globes",[]): mac_boxes += [(b["key"],b["id"]) for b in sphere.get("boxes",[])]
    if g.get("unassigned"): mac_boxes += [(b["key"],b["id"]) for b in g["unassigned"].get("boxes",[])]
    log(f"Mac boxes: {len(mac_boxes)}")
    tot_new=0
    for key,mac_bid in mac_boxes:
        mb=tool(MAC,None,msid,"box_get_box_by_key",{"key":key})
        # ensure Fly box exists (mirror policy + labels)
        fb=tool(FLY,FTOK,fsid,"box_get_box_by_key",{"key":key})
        if "_err" in fb:
            pol=mb.get("storage_policy",{})
            fb=tool(FLY,FTOK,fsid,"box_create_box",{"key":key,"owner_type":mb.get("owner_type","system"),
                "owner_id":mb.get("owner_id","trine"),
                "storage_policy":{"allowed_formats":pol.get("allowed_formats",["yaml","json","markdown","text","binary"]),
                    "max_items":max(pol.get("max_items",1000),2000),"max_content_bytes":max(pol.get("max_content_bytes",0),524288)}})
            if "_err" in fb: log(f"  {key}: create FAIL {fb['_err'][:50]}"); continue
        fly_bid=fb["id"]
        # mirror box labels (sphere etc.)
        if mb.get("labels"): tool(FLY,FTOK,fsid,"box_set_box_labels",{"box_id":fly_bid,"labels":mb["labels"],"mode":"replace"})
        # diff items by idem_key
        mac_items=tool(MAC,None,msid,"box_browse",{"box_id":mac_bid,"limit":5000}).get("items",[])
        fly_items=tool(FLY,FTOK,fsid,"box_browse",{"box_id":fly_bid,"limit":5000}).get("items",[])
        have=set(it.get("idem_key") for it in fly_items)
        missing=[it for it in mac_items if (it.get("idem_key") or ("x_"+it["id"])) not in have]
        new=0
        for it in missing:
            args={"box_id":fly_bid,"kind":it.get("kind","M"),"source_type":it.get("source_type","sync"),
                "storage_uri":it.get("storage_uri","row://sync/"+it["id"]),
                "idem_key":it.get("idem_key") or ("x_"+it["id"]),
                "format":it.get("format","json"),"content":it.get("content")}
            if it.get("labels"): args["labels"]=it["labels"]
            if it.get("symbols"): args["symbols"]=it["symbols"]
            r=tool(FLY,FTOK,fsid,"box_store",args)
            new += ("_err" not in r)
        tot_new+=new
        log(f"  {key}: mac={len(mac_items)} fly_had={len(have)} +{new}")
    log(f"DONE: +{tot_new} items in {time.time()-t0:.0f}s")

if __name__=="__main__": main()
