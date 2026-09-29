import json,os,glob,datetime,collections
root=os.path.expanduser("~/.config/fir/sessions")
cut=datetime.datetime(2026,9,7,tzinfo=datetime.timezone.utc).timestamp()
W5,W1,R=1.25,2.0,0.10
S=collections.Counter()
for f in glob.glob(root+"/**/*.jsonl",recursive=True):
    if "zulip-acp" not in f or os.path.getmtime(f)<cut: continue
    ev=[]
    for line in open(f,errors="replace"):
        if '"usage"' not in line: continue
        try:o=json.loads(line)
        except: continue
        m=o.get("message") or {}; u=m.get("usage")
        if o.get("type")!="message" or not u or not u.get("totalTokens"): continue
        try:t=datetime.datetime.fromisoformat(o["timestamp"].replace("Z","+00:00")).timestamp()
        except: continue
        if t<cut: continue
        ev.append((t,u.get("cacheWrite") or 0,u.get("cacheRead") or 0,u.get("input") or 0,m.get("stopReason")))
    ev.sort(); anchor=None; anchor_t=None
    for i,(t,cw,cr,inp,sr) in enumerate(ev):
        S["base"]+=inp+cw*W5+cr*R
        start = i==0 or ev[i-1][4]!="toolUse"
        if not start: continue
        S["turns"]+=1
        if anchor is not None:
            g=t-anchor_t
            if 300<t-ev[i-1][0] and g<=3600:   # 5m cache gone, 1h anchor alive
                S["hit"]+=1; S["save"]+=min(cw,anchor)*(W5-R)
        S["prem"]+=cw*(W1-W5)               # new turn-start tokens written at 1h
        anchor=cw+cr+inp; anchor_t=t
b=S["base"]; net=S["save"]-S["prem"]
print(dict(S)); print(f'net {net/1e6:+.1f}M of {b/1e6:.1f}M = {100*net/b:+.1f}%')
