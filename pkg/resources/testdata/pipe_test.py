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

    def test_registers_wait_tool(self):
        _load_pipe()
        names = [t["name"] for t in fir_ext._tools]
        self.assertIn("wait", names)


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

    def test_step_timeout_forwarded_to_call_tool(self):
        ctx = _make_ctx([_text_result("ok")])
        out = self.mod._run_pipe(
            [{"tool": "Read", "params": {}, "timeout_s": 120}], "", ctx
        )
        self.assertFalse(out["is_error"])
        # timeout_s is passed through to ctx.call_tool as timeout=.
        self.assertEqual(ctx.call_tool.call_args.kwargs.get("timeout"), 120.0)

    def test_step_without_timeout_uses_call_tool_default(self):
        ctx = _make_ctx([_text_result("ok")])
        self.mod._run_pipe([{"tool": "Read", "params": {}}], "", ctx)
        # No timeout kwarg passed → ctx.call_tool applies its own default.
        self.assertNotIn("timeout", ctx.call_tool.call_args.kwargs)


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
        # Step 1 is non-leaf (referenced by step 2 via {{prev}}) — omitted.
        self.assertNotIn("FROM_FIRST", text)
        self.assertIn("intermediate", text)
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
        # Make step 1 a leaf by having step 2 not reference it.
        ctx = _make_ctx([_text_result("first"), _text_result(big)])
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
        self.assertIn("dict keys", mod._PIPE_DESCRIPTION)
        self.assertIn("empty string", mod._PIPE_DESCRIPTION)
        # Leaf-only output is documented.
        self.assertIn("leaf", mod._PIPE_DESCRIPTION.lower())


# ---------------------------------------------------------------------------
# Leaf detection / output filtering
# ---------------------------------------------------------------------------


class TestLeafDetection(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_last_step_always_leaf(self):
        steps = [{"tool": "A"}, {"tool": "B"}, {"tool": "C"}]
        leaves = self.mod._leaf_indices(steps)
        self.assertIn(2, leaves)

    def test_prev_marks_predecessor_non_leaf(self):
        steps = [
            {"tool": "A"},
            {"tool": "B", "params": {"x": "{{prev}}"}},
        ]
        leaves = self.mod._leaf_indices(steps)
        self.assertEqual(leaves, {1})

    def test_step_index_marks_target_non_leaf(self):
        steps = [
            {"tool": "A"},
            {"tool": "B"},
            {"tool": "C", "params": {"x": "{{step:0}}"}},
        ]
        leaves = self.mod._leaf_indices(steps)
        # Step 0 referenced; step 1 unreferenced; step 2 last → leaf.
        self.assertEqual(leaves, {1, 2})

    def test_field_token_also_counts_as_reference(self):
        steps = [
            {"tool": "A"},
            {"tool": "B", "params": {"x": "{{step:0.field}}"}},
        ]
        leaves = self.mod._leaf_indices(steps)
        self.assertEqual(leaves, {1})

    def test_refs_inside_nested_structures(self):
        steps = [
            {"tool": "A"},
            {"tool": "B", "params": {"nested": {"k": ["{{step:0}}"]}}},
        ]
        leaves = self.mod._leaf_indices(steps)
        self.assertEqual(leaves, {1})

    def test_intermediate_output_omitted_but_substituted(self):
        big = "X" * 10_000
        captured = []

        def first(name, params):
            return _text_result(big)

        def second(name, params):
            captured.append(params)
            return _text_result("FINAL")

        ctx = _make_ctx([first, second])
        steps = [
            {"tool": "Read"},
            {"tool": "Bash", "params": {"cmd": "{{prev}}"}},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertFalse(out["is_error"])
        # Substitution still happened with the full intermediate text.
        self.assertEqual(captured[0]["cmd"], big)
        text = out["content"][0]["text"]
        # Big intermediate blob is NOT in the LLM-visible output.
        self.assertNotIn(big, text)
        self.assertIn("intermediate", text)
        self.assertIn(str(len(big)), text)
        # Leaf output (final step) is included.
        self.assertIn("FINAL", text)

    def test_all_leaves_when_no_refs(self):
        ctx = _make_ctx([_text_result("one"), _text_result("two")])
        steps = [{"tool": "Read"}, {"tool": "Bash"}]
        out = self.mod._run_pipe(steps, "", ctx)
        text = out["content"][0]["text"]
        self.assertIn("one", text)
        self.assertIn("two", text)

    def test_self_reference_does_not_mark_step_non_leaf(self):
        # {{step:1}} inside step 1 is a self-ref that substitutes to "" at
        # runtime — it doesn't actually consume step 1's output, so step 1
        # must remain a leaf.
        steps = [
            {"tool": "A"},
            {"tool": "B", "params": {"x": "{{step:1}}"}},
        ]
        leaves = self.mod._leaf_indices(steps)
        self.assertEqual(leaves, {0, 1})

    def test_forward_reference_does_not_mark_step_non_leaf(self):
        steps = [
            {"tool": "A", "params": {"x": "{{step:5}}"}},
            {"tool": "B"},
        ]
        leaves = self.mod._leaf_indices(steps)
        self.assertEqual(leaves, {0, 1})

    def test_errored_non_leaf_visible_under_continue_on_error(self):
        # Step 0 is non-leaf (referenced by step 1), errors, but continues.
        # The LLM must still see the error output.
        ctx = _make_ctx([
            _text_result("FAIL_OUTPUT", is_error=True),
            _text_result("after"),
        ])
        steps = [
            {"tool": "Read", "continue_on_error": True},
            {"tool": "Bash", "params": {"cmd": "{{prev}}"}},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("FAIL_OUTPUT", text)
        self.assertIn("[ERROR]", text)
        self.assertIn("after", text)

    def test_error_step_visible_even_if_non_leaf(self):
        # Step 0 is non-leaf (referenced by step 1), but if it errors and
        # aborts, its output must still surface so the LLM sees the failure.
        ctx = _make_ctx([_text_result("BOOM", is_error=True), _text_result("never")])
        steps = [
            {"tool": "Read"},
            {"tool": "Bash", "params": {"cmd": "{{prev}}"}},
        ]
        out = self.mod._run_pipe(steps, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertIn("BOOM", out["content"][0]["text"])


# ---------------------------------------------------------------------------
# wait — server-side poll loop
# ---------------------------------------------------------------------------


def _wait_ctx(self, mod, call_results, available=None):
    """Build a ctx for wait tests and stub out the clock/sleep so polls are
    instantaneous and the elapsed time advances 1s per poll deterministically."""
    ctx = _make_ctx(call_results, available=available)
    # Fake monotonic clock: advances by 1.0 on every read.
    clock = {"t": 0.0}

    def now():
        v = clock["t"]
        clock["t"] += 1.0
        return v

    p_now = mock.patch.object(mod, "_now", now)
    p_sleep = mock.patch.object(mod, "_sleep_sliced", lambda *a, **k: None)
    p_now.start()
    p_sleep.start()
    self.addCleanup(p_now.stop)
    self.addCleanup(p_sleep.stop)
    return ctx


class TestWaitVerdict(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def _run(self, call_results, *, steps=None, interval=5, timeout=300,
             max_polls=60, available=None):
        ctx = _wait_ctx(self, self.mod, call_results, available=available)
        steps = steps or [{"tool": "Bash", "params": {"command": "probe"}}]
        out = self.mod._run_wait(steps, interval, timeout, max_polls, "", ctx)
        return out, ctx

    def test_done_first_poll(self):
        out, ctx = self._run([_text_result("WAIT:done")])
        self.assertFalse(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait: success", text)
        self.assertIn("polls: 1", text)
        self.assertEqual(ctx.call_tool.call_count, 1)
        # Bare sentinel stripped from visible output.
        self.assertNotIn("WAIT:done", text)

    def test_continue_then_done(self):
        out, ctx = self._run([
            _text_result("checking...\nWAIT:continue"),
            _text_result("ready\nWAIT:done"),
        ])
        self.assertFalse(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait: success", text)
        self.assertIn("polls: 2", text)
        self.assertEqual(ctx.call_tool.call_count, 2)
        # Pre-sentinel debug line is kept; sentinel stripped.
        self.assertIn("ready", text)
        self.assertNotIn("WAIT:done", text)

    def test_fail_returns_error_with_message(self):
        out, _ = self._run([_text_result("boom\nWAIT:fail disk full")])
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait: error", text)
        self.assertIn("disk full", text)
        # Debug line retained, sentinel stripped.
        self.assertIn("boom", text)
        self.assertNotIn("WAIT:fail", text)

    def test_fail_without_message_uses_fallback(self):
        out, _ = self._run([_text_result("WAIT:fail")])
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait: error", text)
        self.assertIn("verdict reported fail", text)

    def test_missing_sentinel_hard_error(self):
        out, ctx = self._run([_text_result("no verdict here")])
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("no WAIT: sentinel", text)
        # Hard error after one poll — does not default to continue.
        self.assertEqual(ctx.call_tool.call_count, 1)
        # Diagnostic output preserved.
        self.assertIn("no verdict here", text)

    def test_max_polls_timeout(self):
        results = [_text_result("WAIT:continue") for _ in range(5)]
        out, ctx = self._run(results, max_polls=3)
        # A cap hit is a partial result, not a failure.
        self.assertFalse(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait: timeout", text)
        self.assertIn("max_polls cap reached", text)
        self.assertIn("polls: 3", text)
        self.assertEqual(ctx.call_tool.call_count, 3)

    def test_timeout_wall_clock(self):
        # Clock advances 1s per read; with timeout=2 the cap is hit quickly.
        results = [_text_result("WAIT:continue") for _ in range(20)]
        out, _ = self._run(results, timeout=2, max_polls=60)
        self.assertFalse(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait: timeout", text)
        self.assertIn("timeout cap reached", text)

    def test_wait_default_timeout_is_900(self):
        # Regression guard: 300s under-budgeted every real CI/release wait.
        with mock.patch.object(self.mod, "_run_wait") as run:
            self.mod.wait(
                {"steps": [{"tool": "Bash", "params": {"command": "true"}}]},
                mock.MagicMock(),
            )
        # _run_wait(steps, interval, timeout, max_polls, label, ctx)
        self.assertEqual(run.call_args.args[2], 900.0)

    def test_missing_sentinel_reports_offending_line(self):
        out, _ = self._run([_text_result("no verdict here")])
        self.assertTrue(out["is_error"])
        self.assertIn("'no verdict here'", out["content"][0]["text"])

    def test_verdict_error_strike_escalation(self):
        # Three consecutive verdict-step errors -> outcome=error.
        out, ctx = self._run([
            _text_result("err1", is_error=True),
            _text_result("err2", is_error=True),
            _text_result("err3", is_error=True),
        ])
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("failed 3 polls in a row", text)
        self.assertEqual(ctx.call_tool.call_count, 3)

    def test_verdict_error_strikes_reset_on_success(self):
        # Two errors, then a successful continue resets strikes, then done.
        out, ctx = self._run([
            _text_result("e1", is_error=True),
            _text_result("e2", is_error=True),
            _text_result("WAIT:continue"),
            _text_result("e3", is_error=True),
            _text_result("WAIT:done"),
        ])
        self.assertFalse(out["is_error"])
        self.assertIn("wait: success", out["content"][0]["text"])
        self.assertEqual(ctx.call_tool.call_count, 5)


class TestWaitSubstitutionAndEnv(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_substitution_chaining(self):
        captured = []

        def probe(name, params):
            captured.append(params)
            return _text_result("VALUE")

        def verdict(name, params):
            captured.append(params)
            return _text_result("WAIT:done")

        ctx = _wait_ctx(self, self.mod, [probe, verdict])
        steps = [
            {"tool": "Read", "params": {}},
            {"tool": "Bash", "params": {"command": "check {{prev}}"}},
        ]
        out = self.mod._run_wait(steps, 5, 300, 60, "", ctx)
        self.assertFalse(out["is_error"])
        # {{prev}} substituted into the verdict step's command (after the
        # injected env exports).
        self.assertIn("check VALUE", captured[1]["command"])

    def test_wait_poll_and_state_exposed(self):
        captured = []

        def probe(name, params):
            captured.append(params["command"])
            return _text_result("WAIT:done")

        ctx = _wait_ctx(self, self.mod, [probe])
        steps = [{"tool": "Bash", "params": {"command": "do_probe"}}]
        out = self.mod._run_wait(steps, 5, 300, 60, "", ctx)
        self.assertFalse(out["is_error"])
        cmd = captured[0]
        self.assertIn("export WAIT_POLL=1", cmd)
        self.assertIn("export WAIT_STATE=", cmd)
        self.assertIn("do_probe", cmd)

    def test_wait_poll_increments(self):
        captured = []

        def probe(name, params):
            captured.append(params["command"])
            verdict = "WAIT:done" if len(captured) >= 2 else "WAIT:continue"
            return _text_result(verdict)

        ctx = _wait_ctx(self, self.mod, [probe, probe])
        steps = [{"tool": "Bash", "params": {"command": "p"}}]
        self.mod._run_wait(steps, 5, 300, 60, "", ctx)
        self.assertIn("export WAIT_POLL=1", captured[0])
        self.assertIn("export WAIT_POLL=2", captured[1])

    def test_state_file_cleaned_up(self):
        seen = {}

        def probe(name, params):
            cmd = params["command"]
            # Extract the WAIT_STATE path from the export line.
            for line in cmd.splitlines():
                if line.startswith("export WAIT_STATE="):
                    seen["path"] = line.split("=", 1)[1].strip("'")
            return _text_result("WAIT:done")

        ctx = _wait_ctx(self, self.mod, [probe])
        steps = [{"tool": "Bash", "params": {"command": "p"}}]
        self.mod._run_wait(steps, 5, 300, 60, "", ctx)
        self.assertIn("path", seen)
        self.assertFalse(os.path.exists(seen["path"]))


class TestWaitSleepSlicing(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_sleep_sliced_covers_full_duration(self):
        slept = []
        with mock.patch.object(self.mod.time, "sleep", lambda s: slept.append(s)):
            self.mod._sleep_sliced(1.0)
        # Sliced into <=_WAIT_SLEEP_SLICE chunks that sum to the full duration.
        self.assertTrue(all(s <= self.mod._WAIT_SLEEP_SLICE for s in slept))
        self.assertAlmostEqual(sum(slept), 1.0, places=6)


class TestWaitValidationAndDescription(unittest.TestCase):
    def setUp(self):
        self.mod = _load_pipe()

    def test_empty_steps_rejected(self):
        ctx = _make_ctx([])
        out = self.mod._run_wait([], 5, 300, 60, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertIn("wait: steps must be a non-empty array", out["content"][0]["text"])

    def test_unknown_tool_rejected(self):
        ctx = _make_ctx([])
        out = self.mod._run_wait([{"tool": "Nope"}], 5, 300, 60, "", ctx)
        self.assertTrue(out["is_error"])
        text = out["content"][0]["text"]
        self.assertIn("wait validation failed", text)
        self.assertIn("Nope", text)

    def test_description_mentions_sys_ext_and_sentinels(self):
        self.assertIn("[SYS_EXT]", self.mod._WAIT_DESCRIPTION)
        self.assertIn("WAIT:done", self.mod._WAIT_DESCRIPTION)
        self.assertIn("WAIT_POLL", self.mod._WAIT_DESCRIPTION)
        self.assertIn("WAIT_STATE", self.mod._WAIT_DESCRIPTION)

    def test_earlier_step_abort_counts_as_strike(self):
        # An earlier (non-verdict) step errors and aborts the chain before the
        # verdict runs — treated as continue + strike, escalating after 3.
        ctx = _wait_ctx(self, self.mod, [
            _text_result("e1", is_error=True),
            _text_result("e2", is_error=True),
            _text_result("e3", is_error=True),
        ])
        steps = [
            {"tool": "Bash", "params": {"command": "guard"}},
            {"tool": "Bash", "params": {"command": "verdict"}},
        ]
        out = self.mod._run_wait(steps, 5, 300, 60, "", ctx)
        self.assertTrue(out["is_error"])
        self.assertIn("failed 3 polls in a row", out["content"][0]["text"])



# ---------------------------------------------------------------------------
# [hash: ...] metadata block filtering (regression: wait sentinel displaced)
# ---------------------------------------------------------------------------


def _hashed_result(text, h="0123456789abcdef", is_error=False):
    """Mimic core Bash/Read results: output block + trailing [hash: ...] block."""
    return {
        "content": [
            {"type": "text", "text": text},
            {"type": "text", "text": f"[hash: {h}]"},
        ],
        "is_error": is_error,
    }


class TestHashBlockFiltering(unittest.TestCase):
    def setUp(self):
        self.pipe = _load_pipe()

    def test_result_text_drops_hash_block(self):
        self.assertEqual(
            self.pipe._result_text(_hashed_result("hello")), "hello"
        )

    def test_result_text_keeps_hash_like_line_inside_output(self):
        # A [hash: ...] line embedded in a normal output block is real
        # output (e.g. catting a file) and must survive.
        text = "before\n[hash: 0123456789abcdef]\nafter"
        self.assertEqual(
            self.pipe._result_text(_text_result(text)), text
        )

    def test_wait_verdict_parses_despite_trailing_hash_block(self):
        ctx = _make_ctx([_hashed_result("checking...\nWAIT:done")])
        result = self.pipe._run_wait(
            [{"tool": "Bash", "params": {"command": "true"}}],
            0.01, 30, 5, "", ctx,
        )
        text = result["content"][0]["text"]
        self.assertFalse(result.get("is_error", False), text)
        self.assertIn("wait: success", text)

    def test_pipe_substitution_excludes_hash_block(self):
        seen = {}

        def second(name, params):
            seen["cmd"] = params["command"]
            return _text_result("done")

        ctx = _make_ctx([_hashed_result("alpha"), second])
        self.pipe._run_pipe(
            [
                {"tool": "Bash", "params": {"command": "first"}},
                {"tool": "Bash", "params": {"command": "use {{prev}}"}},
            ],
            "", ctx,
        )
        self.assertEqual(seen["cmd"], "use alpha")




# ---------------------------------------------------------------------------
# meta channel (post-llm-meta ROOT fix): the content hash now lives in
# result["meta"], never in a [hash: ...] content block. So it structurally
# cannot reach _result_text, the WAIT: verdict line, or {{prev}} subs. These
# assert the real post-fix tool-result shape (the TestHashBlockFiltering class
# above covers the legacy content-block path kept as mixed-fleet defense).
# ---------------------------------------------------------------------------


def _meta_result(text, h="0123456789abcdef", is_error=False):
    """Mimic post-fix core Bash/Read results: one output content block plus a
    sibling ``meta`` dict carrying the hash (no [hash: ...] content block)."""
    return {
        "content": [{"type": "text", "text": text}],
        "meta": {"hash": h},
        "is_error": is_error,
    }


class TestMetaChannel(unittest.TestCase):
    def setUp(self):
        self.pipe = _load_pipe()

    def test_result_text_ignores_meta_field(self):
        # The sibling meta dict must never bleed into the extracted text.
        self.assertEqual(self.pipe._result_text(_meta_result("hello")), "hello")

    def test_result_text_no_hash_leak(self):
        out = self.pipe._result_text(_meta_result("hello", h="deadbeefcafef00d"))
        self.assertNotIn("deadbeefcafef00d", out)
        self.assertNotIn("hash", out)

    def test_wait_verdict_parses_with_meta_hash(self):
        ctx = _make_ctx([_meta_result("checking...\nWAIT:done")])
        result = self.pipe._run_wait(
            [{"tool": "Bash", "params": {"command": "true"}}],
            0.01, 30, 5, "", ctx,
        )
        text = result["content"][0]["text"]
        self.assertFalse(result.get("is_error", False), text)
        self.assertIn("wait: success", text)

    def test_wait_verdict_meta_hash_not_in_output(self):
        ctx = _make_ctx([_meta_result("ready\nWAIT:done", h="deadbeefcafef00d")])
        result = self.pipe._run_wait(
            [{"tool": "Bash", "params": {"command": "true"}}],
            0.01, 30, 5, "", ctx,
        )
        text = result["content"][0]["text"]
        self.assertNotIn("deadbeefcafef00d", text)

    def test_pipe_substitution_excludes_meta_hash(self):
        seen = {}

        def second(name, params):
            seen["cmd"] = params["command"]
            return _text_result("done")

        ctx = _make_ctx([_meta_result("alpha", h="feedface00000000"), second])
        self.pipe._run_pipe(
            [
                {"tool": "Bash", "params": {"command": "first"}},
                {"tool": "Bash", "params": {"command": "use {{prev}}"}},
            ],
            "", ctx,
        )
        self.assertEqual(seen["cmd"], "use alpha")


if __name__ == "__main__":
    unittest.main()
