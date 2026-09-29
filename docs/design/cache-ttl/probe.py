import json,os,time,uuid,urllib.request
a=json.load(open(os.path.expanduser("~/.config/fir/auth.json")))["anthropic"]
print("token valid for", int(a["expires"]/1000-time.time()),"s")
nonce=uuid.uuid4().hex
big=("Probe "+nonce+". "+" ".join(f"word{i}" for i in range(6000)))
def call(ttl):
    cc={"type":"ephemeral"}; 
    if ttl: cc["ttl"]=ttl
    body={"model":"claude-haiku-4-5","max_tokens":5,
      "system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},
                {"type":"text","text":big,"cache_control":cc}],
      "messages":[{"role":"user","content":"say ok"}]}
    r=urllib.request.Request("https://api.anthropic.com/v1/messages",json.dumps(body).encode(),
      {"content-type":"application/json","authorization":"Bearer "+a["access"],
       "anthropic-version":"2023-06-01","anthropic-beta":"oauth-2025-04-20,extended-cache-ttl-2025-04-11"})
    u=json.load(urllib.request.urlopen(r))["usage"]; print(ttl or "5m", json.dumps(u)); 
call(None); time.sleep(2); call("1h"); time.sleep(400); call("1h")
