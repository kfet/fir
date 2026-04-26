#!/usr/bin/env python3
"""Tests for the aside builtin extension.

Exercises:
  - _result_text: extracting text from tool results
  - _build_synthesis_prompt: prompt construction
  - _run_aside: full orchestration with mocked ctx
  - aside tool handler: parameter validation
  - cmd_aside command handler: argument handling (side questions + tool orchestration)
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


def _load_aside():
    """(Re-)import aside.py, resetting registries and capturing handlers."""
    if "aside" in sys.modules:
        del sys.modules["aside"]
    fir_ext._tools.clear()
    fir_ext._tool_handlers.clear()
    fir_ext._event_handlers.clear()
    fir_ext._hook_handlers.clear()
    fir_ext._commands.clear()
    fir_ext._command_handlers.clear()
    with mock.patch.object(fir_ext, "run"):
        import aside
    return aside


# ---------------------------------------------------------------------------
# _result_text
# ---------------------------------------------------------------------------


class TestResultText(unittest.TestCase):
    def setUp(self):
        self.mod = _load_aside()

    def test_extracts_text_from_content_list(self):
        result = {"content": [{"text": "hello"}, {"text": "world"}]}
        self.assertEqual(self.mod._result_text(result), "hello\nworld")

    def test_extracts_text_key(self):
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
        self.mod = _load_aside()

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
# _run_aside
# ---------------------------------------------------------------------------


class TestRunAside(unittest.TestCase):
    def setUp(self):
        self.mod = _load_aside()

    def _make_ctx(self, tool_results=None, side_query_result="synthesis", available_tools=None):
        """Create a mock context with call_tool, side_query, and list_tools."""
        ctx = mock.MagicMock(spec=fir_ext.Context)
        if tool_results is None:
            tool_results = {}

        def _call_tool(name, params=None, **kw):
            if name in tool_results:
                return tool_results[name]
            return {"content": [{"text": f"result of {name}"}], "is_error": False}

        ctx.call_tool = mock.MagicMock(side_effect=_call_tool)
        ctx.side_query = mock.MagicMock(return_value=side_query_result)

        if available_tools is None:
            # Default: expose Read and Bash with a required "path"/"command" param.
            available_tools = [
                {"name": "Read", "description": "Read a file", "parameters": {
                    "type": "object",
                    "properties": {"path": {"type": "string"}},
                    "required": ["path"],
                }},
                {"name": "Bash", "description": "Run a command", "parameters": {
                    "type": "object",
                    "properties": {"command": {"type": "string"}},
                    "required": ["command"],
                }},
            ]
        ctx.list_tools = mock.MagicMock(return_value=available_tools)
        return ctx

    def test_basic_flow(self):
        ctx = self._make_ctx(side_query_result="summary of files")
        tools = [
            {"name": "Read", "params": {"path": "a.go"}},
            {"name": "Read", "params": {"path": "b.go"}},
        ]
        result = self.mod._run_aside(tools, "summarise files", ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual(result["content"][0]["text"], "summary of files")
        self.assertEqual(ctx.call_tool.call_count, 2)
        ctx.side_query.assert_called_once()
        # details should contain raw tool outputs for TUI display
        self.assertIn("details", result)
        tool_outputs = result["details"]["tool_outputs"]
        self.assertEqual(len(tool_outputs), 2)
        self.assertEqual(tool_outputs[0]["name"], "Read")
        self.assertFalse(tool_outputs[0]["is_error"])

    def test_no_tools_runs_pure_side_query(self):
        ctx = self._make_ctx(side_query_result="the answer is 42")
        result = self.mod._run_aside([], "what is the meaning of life?", ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual(result["content"][0]["text"], "the answer is 42")
        ctx.call_tool.assert_not_called()
        ctx.side_query.assert_called_once_with("what is the meaning of life?")

    def test_empty_instructions_returns_error(self):
        ctx = self._make_ctx()
        result = self.mod._run_aside([{"name": "Read"}], "", ctx)
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
        result = self.mod._run_aside(tools, "summarise", ctx)
        self.assertFalse(result["is_error"])
        # side_query was called with the error output included
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("[ERROR]", prompt)
        self.assertIn("file not found", prompt)
        # details should mark the tool output as an error
        self.assertTrue(result["details"]["tool_outputs"][0]["is_error"])

    def test_call_tool_exception_handled(self):
        ctx = self._make_ctx(side_query_result="partial results")
        ctx.call_tool = mock.MagicMock(
            side_effect=RuntimeError("connection lost")
        )
        tools = [{"name": "Read", "params": {"path": "a.go"}}]
        result = self.mod._run_aside(tools, "summarise", ctx)
        # Should still succeed — error is included in synthesis
        self.assertFalse(result["is_error"])
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("connection lost", prompt)

    def test_side_query_failure_returns_error(self):
        ctx = self._make_ctx()
        ctx.side_query = mock.MagicMock(side_effect=RuntimeError("LLM down"))
        tools = [{"name": "Read", "params": {"path": "a.go"}}]
        result = self.mod._run_aside(tools, "summarise", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("aside LLM call failed", result["content"][0]["text"])

    def test_no_tools_side_query_failure_returns_error(self):
        ctx = self._make_ctx()
        ctx.side_query = mock.MagicMock(side_effect=RuntimeError("LLM down"))
        result = self.mod._run_aside([], "some question", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("aside LLM call failed", result["content"][0]["text"])

    def test_side_query_overflow_includes_hint(self):
        ctx = self._make_ctx()
        ctx.side_query = mock.MagicMock(
            side_effect=RuntimeError("side-query: Input exceeds context window limit")
        )
        result = self.mod._run_aside([], "some question", ctx)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("aside LLM call failed", text)
        self.assertIn("context window full", text)

    def test_unnamed_tool_produces_validation_error(self):
        ctx = self._make_ctx(side_query_result="handled")
        tools = [{"params": {"path": "a.go"}}]  # no name
        result = self.mod._run_aside(tools, "summarise", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("name is required", result["content"][0]["text"])
        ctx.call_tool.assert_not_called()
        ctx.side_query.assert_not_called()

    def test_synthesis_prompt_has_instructions(self):
        ctx = self._make_ctx(side_query_result="done")
        tools = [{"name": "Bash", "params": {"command": "ls"}}]
        self.mod._run_aside(tools, "list all files", ctx)
        prompt = ctx.side_query.call_args[0][0]
        self.assertIn("--- Instructions ---", prompt)
        self.assertIn("list all files", prompt)

    def test_unknown_tool_returns_validation_error(self):
        ctx = self._make_ctx()
        tools = [{"name": "NoSuchTool", "params": {}}]
        result = self.mod._run_aside(tools, "summarise", ctx)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("NoSuchTool", text)
        self.assertIn("Available:", text)
        ctx.call_tool.assert_not_called()

    def test_missing_required_params_returns_validation_error(self):
        ctx = self._make_ctx()
        tools = [{"name": "Read", "params": {}}]  # missing required "path"
        result = self.mod._run_aside(tools, "summarise", ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("missing required params", result["content"][0]["text"])
        self.assertIn("path", result["content"][0]["text"])
        ctx.call_tool.assert_not_called()


# ---------------------------------------------------------------------------
# aside tool handler (registered via @fir_ext.tool)
# ---------------------------------------------------------------------------


class TestAsideTool(unittest.TestCase):
    def setUp(self):
        self.mod = _load_aside()

    def test_registered(self):
        names = {t["name"] for t in fir_ext._tools}
        self.assertIn("aside", names)

    def test_handler_delegates_to_run_aside(self):
        handler = fir_ext._tool_handlers["aside"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.call_tool = mock.MagicMock(
            return_value={"content": [{"text": "ok"}], "is_error": False}
        )
        ctx.side_query = mock.MagicMock(return_value="synthesised")
        ctx.list_tools = mock.MagicMock(return_value=[
            {"name": "Bash", "description": "Run a command", "parameters": {
                "type": "object",
                "properties": {"command": {"type": "string"}},
                "required": ["command"],
            }},
        ])
        params = {
            "tools": [{"name": "Bash", "params": {"command": "echo hi"}}],
            "instructions": "summarise",
            "description": "test",
        }
        result = handler(params, ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual(result["content"][0]["text"], "synthesised")


# ---------------------------------------------------------------------------
# /aside command handler
# ---------------------------------------------------------------------------


class TestAsideCommand(unittest.TestCase):
    def setUp(self):
        self.mod = _load_aside()

    def test_registered(self):
        names = {c["name"] for c in fir_ext._commands}
        self.assertIn("aside", names)

    def test_no_args_returns_usage(self):
        handler = fir_ext._command_handlers["aside"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        result = handler([], ctx)
        self.assertIn("Usage", result["message"])
        ctx.send_user_message.assert_not_called()
        ctx.side_query.assert_not_called()

    def test_short_question_runs_side_query(self):
        """Short text without tool keywords → pure side question."""
        handler = fir_ext._command_handlers["aside"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.side_query = mock.MagicMock(return_value="it means X")
        result = handler(["what", "does", "that", "mean?"], ctx)
        self.assertIn("aside:", result["message"])
        self.assertIn("it means X", result["message"])
        ctx.send_user_message.assert_not_called()

    def test_tool_request_sends_user_message(self):
        """Longer text with tool keywords → delegate to agent."""
        handler = fir_ext._command_handlers["aside"]
        ctx = mock.MagicMock(spec=fir_ext.Context)
        args = [
            "read", "the", "5", "largest", ".go", "files",
            "and", "summarise", "their", "purpose",
        ]
        result = handler(args, ctx)
        self.assertEqual(result, {})
        ctx.send_user_message.assert_called_once()
        msg = ctx.send_user_message.call_args[0][0]
        self.assertIn("aside", msg)
        self.assertIn("read the 5 largest .go files", msg)


# ---------------------------------------------------------------------------
# Advisor configuration & escalation
# ---------------------------------------------------------------------------


class TestParseAdvisorSpec(unittest.TestCase):
    """Round-trip parse/format of the 'provider/model[:effort]' spec form."""

    def setUp(self):
        self.mod = _load_aside()

    def test_parses_provider_and_model(self):
        got = self.mod._parse_advisor_spec("anthropic/claude-opus-4-x")
        self.assertEqual(got, {"provider": "anthropic", "model": "claude-opus-4-x"})

    def test_parses_with_effort(self):
        got = self.mod._parse_advisor_spec("anthropic/claude-opus-4-x:high")
        self.assertEqual(
            got,
            {"provider": "anthropic", "model": "claude-opus-4-x", "effort": "high"},
        )

    def test_strips_whitespace(self):
        got = self.mod._parse_advisor_spec("  anthropic / claude-opus-4-x : high  ")
        self.assertEqual(got["provider"], "anthropic")
        self.assertEqual(got["model"], "claude-opus-4-x")
        self.assertEqual(got["effort"], "high")

    def test_rejects_missing_slash(self):
        self.assertIsNone(self.mod._parse_advisor_spec("claude-opus-4-x"))

    def test_rejects_empty_provider(self):
        self.assertIsNone(self.mod._parse_advisor_spec("/claude-opus-4-x"))

    def test_rejects_empty_model(self):
        self.assertIsNone(self.mod._parse_advisor_spec("anthropic/"))

    def test_format_round_trips(self):
        for spec in ("anthropic/claude-opus-4-x", "anthropic/claude-opus-4-x:high"):
            cfg = self.mod._parse_advisor_spec(spec)
            self.assertEqual(self.mod._format_advisor_spec(cfg), spec)


class TestAdvisorEscalation(unittest.TestCase):
    """Tool-level behaviour when an advisor is configured and 'escalate' is set."""

    def _ctx(self, side_query_result="advisor reply"):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.side_query = mock.MagicMock(return_value=side_query_result)
        return ctx

    def _load_with_advisor(self, advisor_cfg):
        """Reload aside.py with a patched _ADVISOR module-level constant."""
        mod = _load_aside()
        mod._ADVISOR = advisor_cfg
        return mod

    def test_escalate_routes_to_advisor_model(self):
        mod = self._load_with_advisor(
            {"provider": "anthropic", "model": "claude-opus-4-x", "effort": "high"}
        )
        ctx = self._ctx(side_query_result="advisor says so")
        result = mod._run_aside([], "design tradeoff?", ctx, escalate=True)

        self.assertFalse(result["is_error"])
        ctx.side_query.assert_called_once()
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-opus-4-x")
        self.assertEqual(kwargs["provider"], "anthropic")
        self.assertEqual(kwargs["effort"], "high")
        # Output prefixed with the advisor trace line.
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor: anthropic/claude-opus-4-x:high]"))
        self.assertIn("advisor says so", text)

    def test_escalate_without_effort_omits_effort_kwarg(self):
        mod = self._load_with_advisor(
            {"provider": "anthropic", "model": "claude-opus-4-x"}
        )
        ctx = self._ctx()
        mod._run_aside([], "q", ctx, escalate=True)
        kwargs = ctx.side_query.call_args.kwargs
        self.assertNotIn("effort", kwargs)
        self.assertEqual(kwargs["model"], "claude-opus-4-x")

    def test_no_escalate_uses_no_overrides(self):
        mod = self._load_with_advisor(
            {"provider": "anthropic", "model": "claude-opus-4-x"}
        )
        ctx = self._ctx(side_query_result="plain reply")
        result = mod._run_aside([], "q", ctx, escalate=False)
        # No model/provider/effort kwargs were passed.
        self.assertEqual(ctx.side_query.call_args.kwargs, {})
        # No advisor prefix on the output.
        self.assertEqual(result["content"][0]["text"], "plain reply")

    def test_escalate_ignored_when_no_advisor_configured(self):
        mod = self._load_with_advisor(None)
        ctx = self._ctx(side_query_result="plain reply")
        result = mod._run_aside([], "q", ctx, escalate=True)
        # Even with escalate=True, no override kwargs are set.
        self.assertEqual(ctx.side_query.call_args.kwargs, {})
        self.assertEqual(result["content"][0]["text"], "plain reply")

    def test_tool_schema_has_no_escalate_when_unconfigured(self):
        # Reload without an advisor — escalate must NOT appear in the schema.
        mod = _load_aside()
        mod._ADVISOR = None
        params = mod._aside_tool_parameters()
        self.assertNotIn("escalate", params["properties"])
        self.assertNotIn("Escalation", mod._aside_tool_description())

    def test_tool_schema_has_escalate_when_configured(self):
        mod = _load_aside()
        mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-x"}
        params = mod._aside_tool_parameters()
        self.assertIn("escalate", params["properties"])
        self.assertEqual(params["properties"]["escalate"]["type"], "boolean")
        self.assertIn("Escalation", mod._aside_tool_description())


class TestLoadAdvisorConfig(unittest.TestCase):
    """Cover the default-vs-config matrix for _load_advisor_config."""

    def setUp(self):
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        # Seed SDK config_dirs so the extension reads from our tmp dir.
        self._prev_dirs = list(fir_ext.config_dirs)
        fir_ext.config_dirs = [self._tmp.name]
        self.addCleanup(lambda: setattr(fir_ext, "config_dirs", self._prev_dirs))
        self.mod = _load_aside()
        self._cfg_path = self.mod.Path(self._tmp.name) / "aside.json"

    def test_missing_file_returns_default(self):
        cfg = self.mod._load_advisor_config()
        self.assertIsNotNone(cfg)
        # Default tracks _DEFAULT_ADVISOR_SPEC. If someone bumps it to a
        # newer Opus, the test still passes — we only assert the family.
        self.assertEqual(cfg["provider"], "anthropic")
        self.assertTrue(cfg["model"].startswith("claude-opus-"))

    def test_default_is_at_least_opus_4_7(self):
        # Floor for "no config" UX. Bump alongside _DEFAULT_ADVISOR_SPEC.
        spec = self.mod._DEFAULT_ADVISOR_SPEC
        self.assertTrue(spec.startswith("anthropic/claude-opus-"))
        # Compare numeric tail so 4-7, 4-8, 5-0 etc. all pass.
        tail = spec.split("claude-opus-")[1]
        major, minor = (int(p) for p in tail.split("-")[:2])
        self.assertGreaterEqual((major, minor), (4, 7))

    def test_explicit_off_returns_none(self):
        self._cfg_path.write_text('{"advisor": "off"}')
        self.assertIsNone(self.mod._load_advisor_config())

    def test_explicit_null_returns_none(self):
        self._cfg_path.write_text('{"advisor": null}')
        self.assertIsNone(self.mod._load_advisor_config())

    def test_pinned_spec_returns_pinned(self):
        self._cfg_path.write_text('{"advisor": "anthropic/claude-opus-4-x:high"}')
        cfg = self.mod._load_advisor_config()
        self.assertEqual(
            cfg,
            {"provider": "anthropic", "model": "claude-opus-4-x", "effort": "high"},
        )

    def test_malformed_spec_falls_back_to_default(self):
        # A bad spec should not silently disable escalation — fall back to
        # the bundled default so the feature keeps working.
        self._cfg_path.write_text('{"advisor": "not-a-spec"}')
        cfg = self.mod._load_advisor_config()
        self.assertIsNotNone(cfg)
        self.assertEqual(cfg["provider"], "anthropic")

    def test_corrupt_json_falls_back_to_default(self):
        self._cfg_path.write_text('not json')
        cfg = self.mod._load_advisor_config()
        self.assertIsNotNone(cfg)


class TestAsideAdvisorCommand(unittest.TestCase):
    """The /aside-advisor slash command — show, set, off."""

    def setUp(self):
        # Re-import aside.py with a freshly patched config dir/file so the
        # tests don't touch the user's real ~/.config/fir.
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self._prev_dirs = list(fir_ext.config_dirs)
        fir_ext.config_dirs = [self._tmp.name]
        self.addCleanup(lambda: setattr(fir_ext, "config_dirs", self._prev_dirs))
        self.mod = _load_aside()
        self._cfg_path = self.mod.Path(self._tmp.name) / "aside.json"

    def _handler(self):
        return fir_ext._command_handlers["aside-advisor"]

    def test_show_when_disabled(self):
        self.mod._ADVISOR = None
        result = self._handler()([], mock.MagicMock())
        self.assertIn("disabled", result["message"])

    def test_show_when_default(self):
        # File missing + advisor is the default → "(default — no aside.json)"
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-7"}
        result = self._handler()([], mock.MagicMock())
        self.assertIn("anthropic/claude-opus-4-7", result["message"])
        self.assertIn("default", result["message"])

    def test_show_when_pinned(self):
        # File present → show its path.
        self._cfg_path.write_text('{"advisor": "anthropic/claude-opus-4-x"}')
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-x"}
        result = self._handler()([], mock.MagicMock())
        self.assertIn("anthropic/claude-opus-4-x", result["message"])
        self.assertIn(str(self._cfg_path), result["message"])

    def test_set_writes_config_file(self):
        result = self._handler()(["anthropic/claude-opus-4-x:high"], mock.MagicMock())
        self.assertIn("set to anthropic/claude-opus-4-x:high", result["message"])
        # File persisted with the spec.
        self.assertTrue(self._cfg_path.is_file())
        import json as _json
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data, {"advisor": "anthropic/claude-opus-4-x:high"})

    def test_set_rejects_malformed_spec(self):
        result = self._handler()(["claude-opus-4-x"], mock.MagicMock())
        self.assertIn("malformed spec", result["message"])
        self.assertFalse(self._cfg_path.is_file())

    def test_off_writes_explicit_off_marker(self):
        # /aside-advisor off must be a hard opt-out — survives across sessions
        # without a missing file silently re-enabling the default advisor.
        result = self._handler()(["off"], mock.MagicMock())
        self.assertIn("disabled", result["message"])
        self.assertTrue(self._cfg_path.is_file())
        import json as _json
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data, {"advisor": "off"})

    def test_off_overwrites_existing_pin(self):
        self._cfg_path.write_text('{"advisor": "anthropic/claude-opus-4-x"}')
        self._handler()(["off"], mock.MagicMock())
        import json as _json
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data["advisor"], "off")

    def test_off_preserves_other_keys(self):
        # If aside.json gains other keys later, /aside-advisor off must
        # only flip the advisor key, not nuke the file.
        import json as _json
        self._cfg_path.write_text(_json.dumps(
            {"advisor": "anthropic/claude-opus-4-x", "future_key": "x"}
        ))
        self._handler()(["off"], mock.MagicMock())
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data, {"advisor": "off", "future_key": "x"})


class DefaultAdvisorTracksHighestAnthropicOpus(unittest.TestCase):
    """Guards aside.py's _DEFAULT_ADVISOR_SPEC.

    When no aside.json exists the extension falls back to a hard-coded model
    spec — that spec must always point at the strongest Anthropic Opus baked
    into fir's bundled model registry. We parse both sides as text and
    compare.

    Date-suffixed aliases (claude-opus-4-1-20250805, claude-opus-4-20250514)
    are intentionally ignored — those are short-lived; the bare X-Y form is
    the long-lived alias users pin to.
    """

    _ASIDE_PY = os.path.join(_ext_dir, "aside.py")
    _MODELS_GO = os.path.join(
        os.path.dirname(os.path.abspath(__file__)),
        "..", "..", "ai", "models_generated.go",
    )

    def test_default_advisor_matches_highest_opus(self):
        import re

        with open(self._ASIDE_PY, encoding="utf-8") as f:
            aside_src = f.read()
        m = re.search(r'_DEFAULT_ADVISOR_SPEC\s*=\s*"([^"]+)"', aside_src)
        self.assertIsNotNone(m, "aside.py: _DEFAULT_ADVISOR_SPEC literal not found")
        assert m is not None  # for type checker
        got = m.group(1)

        with open(self._MODELS_GO, encoding="utf-8") as f:
            models_src = f.read()

        # Each RegisterModel block has ID: "..." and Provider: "anthropic".
        # Bare X-Y form only — minor capped at 2 digits to reject date stamps.
        block_re = re.compile(
            r'ID:\s*"(claude-opus-(\d+)-(\d{1,2}))"'
            r'(?:[^}]*?)Provider:\s*"anthropic"',
            re.DOTALL,
        )
        best = (-1, -1, "")
        for match in block_re.finditer(models_src):
            full_id, major, minor = match.group(1), int(match.group(2)), int(match.group(3))
            if (major, minor) > (best[0], best[1]):
                best = (major, minor, full_id)

        self.assertNotEqual(
            best[2], "",
            "no claude-opus-<major>-<minor> models registered under anthropic provider",
        )
        want_spec = "anthropic/" + best[2]
        self.assertEqual(
            got, want_spec,
            "aside.py _DEFAULT_ADVISOR_SPEC out of sync with model registry:\n"
            f"  got:  {got}\n"
            f"  want: {want_spec}\n"
            f"\nFix: edit pkg/resources/builtin_extensions/aside.py and update "
            f"_DEFAULT_ADVISOR_SPEC to {want_spec!r}.",
        )


if __name__ == "__main__":
    unittest.main()
