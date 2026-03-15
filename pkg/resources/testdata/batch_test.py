#!/usr/bin/env python3
"""Tests for the batch builtin extension.

Exercises:
  - _result_text: extracting text from tool results
  - _build_synthesis_prompt: prompt construction
  - _run_batch: full orchestration with mocked ctx
  - batch_run tool handler: parameter validation
  - cmd_batch command handler: argument handling
"""

import os
import sys
import unittest
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..",
    "..",
    "extension",
    "sdk",
    "python",
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext


def _load_batch():
    """(Re-)import batch.py, resetting registries and capturing handlers."""
    if "batch" in sys.modules:
        del sys.modules["batch"]
    fir_ext._tools.clear()
    fir_ext._tool_handlers.clear()
    fir_ext._event_handlers.clear()
    fir_ext._hook_handlers.clear()
    fir_ext._commands.clear()
    fir_ext._command_handlers.clear()
    with mock.patch.object(fir_ext, "run"):
        import batch
    return batch


# ---------------------------------------------------------------------------
# _result_text
# ---------------------------------------------------------------------------


class TestResultText(unittest.TestCase):
    def setUp(self):
        self.mod = _load_batch()

    def test_extracts_text_from_content_list(self):
        result = {"content": [{"text": "hello"}, {"text": "world"}]}
        self.assertEqual(self.mod._result_text(result), "hello\nworld")

    def test_extracts_Text_key(self):
        """Go JSON marshals to uppercase Text."""
        result = {"content": [{"Text": "from Go"}]}
        self.assertEqual(self.mod._result_text(result), "from Go")

    def test_handles_string_content(self):
        result = {"content": "plain string"}
        self.assertEqual(self.mod._result_text(result), "plain string")

    def test_handles_empty_content(self):
        result = {"content": []}
        self.assertEqual(self.mod._result_text(result), "")

    def test_handles_missing_content(self):
        result = {"something": "else"}
        self.assertEqual(self.mod._result_text(result), "")

    def test_handles_string_blocks(self):
        result = {"content": ["one", "two"]}
        self.assertEqual(self.mod._result_text(result), "one\ntwo")


# ---------------------------------------------------------------------------
# _build_synthesis_prompt
# ---------------------------------------------------------------------------


class TestBuildSynthesisPrompt(unittest.TestCase):
    def setUp(self):
        self.mod = _load_batch()

    def test_includes_all_tool_outputs(self):
        results = [
            {"name": "Read", "output": "file contents", "is_error": False},
            {"name": "Bash", "output": "cmd output", "is_error": False},
        ]
        prompt = self.mod._build_synthesis_prompt(results, "summarise")
        self.assertIn("Tool 1: Read", prompt)
        self.assertIn("file contents", prompt)
        self.assertIn("Tool 2: Bash", prompt)
        self.assertIn("cmd output", prompt)
        self.assertIn("summarise", prompt)

    def test_marks_errors(self):
        results = [
            {"name": "Read", "output": "not found", "is_error": True},
        ]
        prompt = self.mod._build_synthesis_prompt(results, "handle errors")
        self.assertIn("[ERROR]", prompt)

    def test_instructions_at_end(self):
        results = [{"name": "A", "output": "x", "is_error": False}]
        prompt = self.mod._build_synthesis_prompt(results, "do this")
        # Instructions should come after tool outputs
        idx_tool = prompt.index("Tool 1")
        idx_instr = prompt.index("--- Instructions ---")
        self.assertGreater(idx_instr, idx_tool)


# ---------------------------------------------------------------------------
# _run_batch
# ---------------------------------------------------------------------------


class TestRunBatch(unittest.TestCase):
    def setUp(self):
        self.mod = _load_batch()

    def _make_ctx(self, tool_results=None, side_query_result="synthesis"):
        """Create a mock context with call_tool and side_query."""
        ctx = mock.MagicMock(spec=fir_ext.Context)
        if tool_results is None:
            tool_results = {}

        def _call_tool(name, params=None, **kw):
            if name in tool_results:
                return tool_results[name]
            return {"content": [{"text": f"result of {name}"}], "is_error": False}

        ctx.call_tool = mock.MagicMock(side_effect=_call_tool)
        ctx.side_query = mock.MagicMock(return_value=side_query_result)
        return ctx

    def test_basic_flow(self):
        ctx = self._make_ctx(side_query_result="summary of files")
        tools = [
            {"name": "Read", "params": {"path": "a.go"}},
            {"name": "Read", "params": {"path": "b.go"}},
        ]
        result = self.mod._run_batch(tools, "summarise files", ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual(result["content"][0]["text"], "summary of files")
        self.assertEqual(ctx.call_tool.call_count, 2)
        ctx.side_query.assert_called_once()

    def test_empty_tools_returns_error(self):
        ctx = self._make_ctx()
        result = self.mod._run_batch([], "summarise", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("empty", result["content"][0]["text"])

    def test_empty_instructions_returns_error(self):
        ctx = self._make_ctx()
        result = self.mod._run_batch([{"name": "Read"}], "", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("instructions", result["content"][0]["text"])

    def test_tool_error_included_in_synthesis(self):
        ctx = self._make_ctx(
            tool_results={
                "Read": {
                    "content": [{"text": "file not found"}],
                    "is_error": True,
                },
            },
            side_query_result="one file failed",
        )
        tools = [{"name": "Read", "params": {"path": "missing.go"}}]
        result = self.mod._run_batch(tools, "summarise", ctx)
        self.assertFalse(result["is_error"])
        # side_query was called with the error output included
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("[ERROR]", prompt)
        self.assertIn("file not found", prompt)

    def test_call_tool_exception_handled(self):
        ctx = self._make_ctx(side_query_result="partial results")
        ctx.call_tool = mock.MagicMock(
            side_effect=RuntimeError("connection lost")
        )
        tools = [{"name": "Read", "params": {"path": "a.go"}}]
        result = self.mod._run_batch(tools, "summarise", ctx)
        # Should still succeed — error is included in synthesis
        self.assertFalse(result["is_error"])
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("connection lost", prompt)

    def test_side_query_failure_returns_error(self):
        ctx = self._make_ctx()
        ctx.side_query = mock.MagicMock(side_effect=RuntimeError("LLM down"))
        tools = [{"name": "Read", "params": {"path": "a.go"}}]
        result = self.mod._run_batch(tools, "summarise", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("synthesis failed", result["content"][0]["text"])

    def test_unnamed_tool_produces_error_entry(self):
        ctx = self._make_ctx(side_query_result="handled")
        tools = [{"params": {"path": "a.go"}}]  # no name
        result = self.mod._run_batch(tools, "summarise", ctx)
        self.assertFalse(result["is_error"])
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("unnamed", prompt)
        self.assertIn("name is required", prompt)

    def test_synthesis_prompt_has_instructions(self):
        ctx = self._make_ctx(side_query_result="done")
        tools = [{"name": "Bash", "params": {"command": "ls"}}]
        self.mod._run_batch(tools, "list all files", ctx)
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("--- Instructions ---", prompt)
        self.assertIn("list all files", prompt)


# ---------------------------------------------------------------------------
# batch_run tool handler (registered via @fir_ext.tool)
# ---------------------------------------------------------------------------


class TestBatchRunTool(unittest.TestCase):
    def setUp(self):
        self.mod = _load_batch()

    def test_registered(self):
        names = {t["name"] for t in fir_ext._tools}
        self.assertIn("batch_run", names)

    def test_handler_delegates_to_run_batch(self):
        handler = fir_ext._tool_handlers["batch_run"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.call_tool = mock.MagicMock(
            return_value={"content": [{"text": "ok"}], "is_error": False}
        )
        ctx.side_query = mock.MagicMock(return_value="synthesised")
        params = {
            "tools": [{"name": "Bash", "params": {"command": "echo hi"}}],
            "instructions": "summarise",
            "description": "test",
        }
        result = handler(params, ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual(result["content"][0]["text"], "synthesised")


# ---------------------------------------------------------------------------
# /batch command handler
# ---------------------------------------------------------------------------


class TestBatchCommand(unittest.TestCase):
    def setUp(self):
        self.mod = _load_batch()

    def test_registered(self):
        names = {c["name"] for c in fir_ext._commands}
        self.assertIn("batch", names)

    def test_no_args_returns_usage(self):
        handler = fir_ext._command_handlers["batch"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        result = handler([], ctx)
        self.assertIn("Usage", result["message"])
        ctx.send_user_message.assert_not_called()

    def test_with_args_sends_user_message(self):
        handler = fir_ext._command_handlers["batch"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        result = handler(["read", "files", "and", "summarise"], ctx)
        self.assertEqual(result, {})
        ctx.send_user_message.assert_called_once()
        msg = ctx.send_user_message.call_args[0][0]
        self.assertIn("batch_run", msg)
        self.assertIn("read files and summarise", msg)


if __name__ == "__main__":
    unittest.main()
