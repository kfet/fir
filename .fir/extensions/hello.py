#!/usr/bin/env python3
"""A simple test extension that registers a word_count tool."""
import sys
sys.path.insert(0, "pkg/extproc/sdk/python")
import fir_ext

@fir_ext.tool(
    name="word_count",
    description="Count words in the given text",
    parameters={
        "type": "object",
        "properties": {
            "text": {"type": "string", "description": "Text to count words in"}
        },
        "required": ["text"]
    }
)
def word_count(params, ctx):
    text = params.get("text", "")
    count = len(text.split())
    return f"Word count: {count}"

@fir_ext.on("session_start")
def on_start(event):
    pass

fir_ext.run()
