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
        ev.append((t,u.get("cacheWrite") or 0,u.get("cacheRead") or 0,u.get("input") or 0,u.get("output") or 0,m.get("stopReason")))
    ev.sort()
    for i,(t,cw,cr,inp,out,sr) in enumerate(ev):
        S["base"]+=inp+cw*W5+cr*R
        if sr=="toolUse": continue
        S["ends"]+=1
        P=cw+cr+inp+out                 # prefix the next human turn will reuse
        S["refresh_pess"]+=P*W1          # full 1h rewrite
        S["refresh_opt"]+=P*R+(out)*W1   # read 5m entry, write only the tail at 1h
        if i+1<len(ev):
            n=ev[i+1]; g=n[0]-t
            b="<=5m" if g<=300 else "5m-1h" if g<=3600 else ">1h"
            S["n_"+b]+=1
            if b=="5m-1h":
                S["save"]+=min(n[1],P)*(W5-R)
        else: S["n_last"]+=1
b=S["base"]
print({k:v for k,v in S.items() if k.startswith("n_") or k=="ends"})
print(f'base {b/1e6:.1f}M  save {S["save"]/1e6:.1f}M')
for k in ("refresh_pess","refresh_opt"):
    net=S["save"]-S[k]; print(f'{k}: cost {S[k]/1e6:.1f}M  net {net/1e6:+.1f}M ({100*net/b:+.1f}%)')
