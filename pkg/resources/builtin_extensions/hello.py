#!/usr/bin/env python3
# ---
# name: hello
# explicit: true
# ---
"""Simple test extension: logs a message on agent_end."""

import sys

import fir_ext


@fir_ext.on("session_start")
def on_session_start(params, ctx):
    print("hello.py: session_start fired", file=sys.stderr, flush=True)


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    print("hello.py: agent_end fired", file=sys.stderr, flush=True)


fir_ext.run(name="hello")
