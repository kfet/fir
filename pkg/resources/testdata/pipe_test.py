#!/usr/bin/env python3
"""Tests for the pipe builtin extension."""

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


def _load_pipe():
    if "pipe" in sys.modules:
        del sys.modules["pipe"]
    fir_ext._tools.clear()
    fir_ext._tool_handlers.clear()
    fir_ext._event_handlers.clear()
    fir_ext._hook_handlers.clear()
    fir_ext._commands.clear()
    fir_ext._command_handlers.clear()
    with mock.patch.object(fir_ext, "run"):
        import pipe
    return pipe


def _text_result(text, is_error=False):
    return {"content": [{"type": "text", "text": text}], "is_error": is_error}


def _make_ctx(call_results, available=None):
    """Build a mock ctx whose call_tool pops results from a list in order."""
    ctx = mock.MagicMock()
    ctx.list_tools.return_value = available or [
        {"name": "Read", "parameters": {}},
        {"name": "Bash", "parameters": {}},
        {"name": "JSONTool", "parameters": {}},
    ]
    iterator = iter(call_results)

    def call_tool(name, params, **_):
        spec = next(iterator)
        if isinstance(spec, Exception):
            raise spec
        if callable(spec):
            return spec(name, params)
        return spec

    ctx.call_tool.side_effect = call_tool
    return ctx


# ---------------------------------------------------------------------------
# Registration
# ---------------------------------------------------------------------------


class TestRegistration(unittest.TestCase):
    def test_registers_pipe_tool(self):
        _load_pipe()
        names = [t["name"] for t in fir_ext._tools]
        self.assertIn("pipe", names)


# ---------------------------------------------------------------------------
# Substitution
# ---------------------------------------------------------------------------


class TestSubstitution(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_prev_token(self):
        out = self.mod._substitute("hi {{prev}}!", ["world"])
        self.assertEqual(out, "hi world!")

    def test_step_index(self):
        out = self.mod._substitute("a={{step:0}} b={{step:1}}", ["x", "y"])
        self.assertEqual(out, "a=x b=y")

    def test_field_access(self):
        prior = ['{"name": "Alice", "age": 30}']
        out = self.mod._substitute("hello {{prev.name}}", prior)
        self.assertEqual(out, "hello Alice")

    def test_field_access_falls_back_on_non_json(self):
        out = self.mod._substitute("{{prev.field}}", ["plain text"])
        self.assertEqual(out, "plain text")

    def test_recurses_into_dict_and_list(self):
        params = {"a": "{{prev}}", "b": ["x", "{{step:0}}"]}
        out = self.mod._substitute(params, ["VAL"])
        self.assertEqual(out, {"a": "VAL", "b": ["x", "VAL"]})

    def test_empty_prior_for_first_step(self):
        out = self.mod._substitute("[{{prev}}]", [])
        self.assertEqual(out, "[]")


# ---------------------------------------------------------------------------
# Single-step passthrough
# ---------------------------------------------------------------------------


class TestSingleStep(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_single_step_returns_raw_text(self):
        ctx = _make_ctx([_text_result("hello")])
        out = self.mod._run_pipe([{"tool": "Read", "params": {}}], "", ctx)
        self.assertFalse(out["is_error"])
        self.assertEqual(out["content"][0]["text"], "hello")

    def test_single_step_propagates_error_flag(self):
        ctx = _make_ctx([_text_result("boom", is_error=True)])
        out = self.mod._run_pipe([{"tool": "Read"}], "", ctx)
        # Single-step is transparent: error flag preserved.
        self.assertTrue(out["is_error"])


# ---------------------------------------------------------------------------
# Multi-step
# ---------------------------------------------------------------------------


class TestMultiStep(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_substitutes_prev_into_next_step(self):
        captured = []

        def first(name, params):
            return _text_result("FROM_FIRST")

        def second(name, params):
            captured.append(params)
            return _text_result("done")

        ctx = _make_ctx([first, second])
        steps = [
            {"tool": "Read", "params": {}},
            {"tool": "Bash", "params": {"cmd": "echo {{prev}}"}},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertFalse(out["is_error"])
        self.assertEqual(captured[0], {"cmd": "echo FROM_FIRST"})
        text = out["content"][0]["text"]
        self.assertIn("## Step 1: Read", text)
        self.assertIn("FROM_FIRST", text)
        self.assertIn("## Step 2: Bash", text)
        self.assertIn("done", text)

    def test_step_index_substitution(self):
        captured = []

        def grab(name, params):
            captured.append(params)
            return _text_result("z")

        ctx = _make_ctx(
            [_text_result("A"), _text_result("B"), grab]
        )
        steps = [
            {"tool": "Read"},
            {"tool": "Read"},
            {"tool": "Bash", "params": {"cmd": "{{step:0}}-{{step:1}}"}},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertFalse(out["is_error"])
        self.assertEqual(captured[0], {"cmd": "A-B"})


# ---------------------------------------------------------------------------
# Error handling
# ---------------------------------------------------------------------------


class TestErrorHandling(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_aborts_on_error_by_default(self):
        ctx = _make_ctx(
            [_text_result("ok"), _text_result("bad", is_error=True), _text_result("never")]
        )
        steps = [{"tool": "Read"}, {"tool": "Bash"}, {"tool": "Read"}]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertEqual(ctx.call_tool.call_count, 2)
        self.assertIn("aborted at step 2", out["content"][0]["text"])

    def test_continue_on_error_runs_remaining_steps(self):
        ctx = _make_ctx(
            [_text_result("ok"), _text_result("bad", is_error=True), _text_result("after")]
        )
        steps = [
            {"tool": "Read"},
            {"tool": "Bash", "continue_on_error": True},
            {"tool": "Read"},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])  # any-error propagation
        self.assertEqual(ctx.call_tool.call_count, 3)
        text = out["content"][0]["text"]
        self.assertIn("[ERROR]", text)
        self.assertIn("after", text)

    def test_exception_aborts(self):
        ctx = _make_ctx([_text_result("ok"), RuntimeError("boom"), _text_result("nope")])
        steps = [{"tool": "Read"}, {"tool": "Bash"}, {"tool": "Read"}]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertEqual(ctx.call_tool.call_count, 2)
        self.assertIn("boom", out["content"][0]["text"])

    def test_exception_continue_on_error(self):
        ctx = _make_ctx([RuntimeError("x"), _text_result("after")])
        steps = [
            {"tool": "Bash", "continue_on_error": True},
            {"tool": "Read"},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        # Even with continue_on_error, an erroring step propagates is_error
        # in the final envelope.
        self.assertTrue(out["is_error"])
        self.assertEqual(ctx.call_tool.call_count, 2)

    def test_empty_steps(self):
        ctx = _make_ctx([])
        out = self.mod._run_pipe([], "", ctx)
        self.assertTrue(out["is_error"])

    def test_missing_tool_field(self):
        ctx = _make_ctx([])
        out = self.mod._run_pipe([{"params": {}}], "", ctx)
        self.assertTrue(out["is_error"])
        self.assertIn("missing 'tool'", out["content"][0]["text"])


# ---------------------------------------------------------------------------
# Upfront validation
# ---------------------------------------------------------------------------


class TestUpfrontValidation(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_unknown_tool_aborts_before_any_call(self):
        ctx = _make_ctx([_text_result("never")])
        steps = [
            {"tool": "Read"},
            {"tool": "Nonsense"},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertEqual(ctx.call_tool.call_count, 0)
        text = out["content"][0]["text"]
        self.assertIn("steps[1]", text)
        self.assertIn("Nonsense", text)
        self.assertIn("Available", text)
        self.assertIn("Read", text)

    def test_case_insensitive_tool_name_normalised(self):
        ctx = _make_ctx([_text_result("ok")])
        out = self.mod._run_pipe([{"tool": "read"}], "", ctx)
        self.assertFalse(out["is_error"])
        self.assertEqual(ctx.call_tool.call_args[0][0], "Read")

    def test_missing_required_param_aborts_before_any_call(self):
        available = [
            {"name": "Read", "parameters": {"required": ["path"]}},
            {"name": "Bash", "parameters": {"required": ["cmd"]}},
        ]
        ctx = _make_ctx([_text_result("never")], available=available)
        steps = [
            {"tool": "Read", "params": {"path": "/x"}},
            {"tool": "Bash", "params": {}},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertEqual(ctx.call_tool.call_count, 0)
        text = out["content"][0]["text"]
        self.assertIn("steps[1]", text)
        self.assertIn("cmd", text)

    def test_collects_multiple_validation_errors(self):
        available = [
            {"name": "Read", "parameters": {"required": ["path"]}},
        ]
        ctx = _make_ctx([], available=available)
        steps = [
            {"tool": "Read", "params": {}},
            {"tool": "Nope"},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("path", text)
        self.assertIn("Nope", text)


# ---------------------------------------------------------------------------
# Truncation
# ---------------------------------------------------------------------------


class TestTruncation(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_step_output_truncated_in_markdown(self):
        big = "A" * (self.mod._MAX_OUTPUT_LEN + 5000)
        ctx = _make_ctx([_text_result(big), _text_result("done")])
        steps = [{"tool": "Read"}, {"tool": "Bash"}]
        out = self.mod._run_pipe(steps, "", ctx)
        text = out["content"][0]["text"]
        self.assertIn("... (truncated)", text)
        # Far smaller than the raw 50KB+5000 input.
        self.assertLess(len(text), self.mod._MAX_OUTPUT_LEN + 1000 + 200)

    def test_truncation_applies_to_substitution_feedforward(self):
        big = "B" * (self.mod._MAX_OUTPUT_LEN + 1000)
        captured = []

        def first(name, params):
            return _text_result(big)

        def second(name, params):
            captured.append(params)
            return _text_result("ok")

        ctx = _make_ctx([first, second])
        steps = [
            {"tool": "Read"},
            {"tool": "Bash", "params": {"cmd": "{{prev}}"}},
        ]
        self.mod._run_pipe(steps, "", ctx)
        # Substituted value is the truncated text, not the original.
        cmd = captured[0]["cmd"]
        self.assertLessEqual(len(cmd), self.mod._MAX_OUTPUT_LEN + 100)
        self.assertTrue(cmd.endswith("... (truncated)"))


# ---------------------------------------------------------------------------
# Description carries [SYS_EXT] guidance
# ---------------------------------------------------------------------------


class TestDescription(unittest.TestCase):
    def test_description_mentions_sys_ext_guidance(self):
        mod = _load_pipe()
        self.assertIn("[SYS_EXT]", mod._PIPE_DESCRIPTION)
        # Doc nits requested in review.
        self.assertIn("dict keys", mod._PIPE_DESCRIPTION)
        self.assertIn("empty string", mod._PIPE_DESCRIPTION)


if __name__ == "__main__":
    unittest.main()
