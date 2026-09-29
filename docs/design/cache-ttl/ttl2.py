import json,os,glob,datetime,collections
root=os.path.expanduser("~/.config/fir/sessions")
cut=datetime.datetime(2026,9,7,tzinfo=datetime.timezone.utc).timestamp()
def cls(d):
    if "zulip-acp" in d: return "zulip relay"
    if "poe-acp" in d: return "poe relay"
    return "other (cli/agents)"
W5,W1,R=1.25,2.0,0.10
S=collections.defaultdict(lambda: collections.Counter())
for f in glob.glob(root+"/**/*.jsonl",recursive=True):
    if os.path.getmtime(f)<cut: continue
    c=cls(os.path.dirname(f)); ev=[]
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
    if not ev: continue
    ev.sort(); s=S[c]; s["sessions"]+=1
    for i,(t,cw,cr,inp,sr) in enumerate(ev):
        s["req"]+=1; s["cw"]+=cw; s["cr"]+=cr
        s["base"]+=inp+cw*W5+cr*R
        end = sr!="toolUse"
        if end: s["cw_end"]+=cw
        if i==0: continue
        g=t-ev[i-1][0]; p=ev[i-1]
        conv=min(cw,p[1]+p[2]) if 300<g<=3600 else 0
        s["conv"]+=conv
        if 300<g<=3600: s["gap_mid"]+=1
        # policy B (end-of-turn only): conversion only if previous req ended a turn
        if p[4]!="toolUse": s["conv_end"]+=conv
for c,s in sorted(S.items()):
    A=s["conv"]*(W5-R)-s["cw"]*(W1-W5)          # global 1h
    B=s["conv_end"]*(W5-R)-s["cw_end"]*(W1-W5)  # 1h only on turn-ending requests
    b=s["base"]
    print(f'{c:20} sess {s["sessions"]:4} req {s["req"]:6} gap5m-1h {100*s["gap_mid"]/s["req"]:5.1f}%  base {b/1e6:7.1f}M  global1h {A/1e6:+6.1f}M ({100*A/b:+5.1f}%)  endTurn1h {B/1e6:+6.1f}M ({100*B/b:+5.1f}%)')
