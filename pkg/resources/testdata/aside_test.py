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


def _blocking_ctx():
    """Return a MagicMock(spec=fir_ext.Context) with the streaming
    side_query_stream attribute deleted.

    aside.py prefers ctx.side_query_stream when present, so a MagicMock
    spec — which auto-attaches every Context method — would force the
    streaming code path on every test. Most tests want to assert on the
    blocking ctx.side_query call instead; deleting the streaming
    attribute makes aside.py's ``hasattr`` fall through to the blocking
    flavor and keeps the legacy assertions valid.

    The dedicated streaming tests build their own ctx with
    side_query_stream wired up explicitly.
    """
    from unittest import mock as _mock

    ctx = _mock.MagicMock(spec=fir_ext.Context)
    del ctx.side_query_stream
    return ctx


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
        ctx = _blocking_ctx()
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
                {
                    "name": "Read",
                    "description": "Read a file",
                    "parameters": {
                        "type": "object",
                        "properties": {"path": {"type": "string"}},
                        "required": ["path"],
                    },
                },
                {
                    "name": "Bash",
                    "description": "Run a command",
                    "parameters": {
                        "type": "object",
                        "properties": {"command": {"type": "string"}},
                        "required": ["command"],
                    },
                },
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
        ctx.side_query.assert_called_once_with(
            "what is the meaning of life?", model=None, provider=None, effort=None
        )

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
        ctx.call_tool = mock.MagicMock(side_effect=RuntimeError("connection lost"))
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
        ctx = _blocking_ctx()
        ctx.call_tool = mock.MagicMock(
            return_value={"content": [{"text": "ok"}], "is_error": False}
        )
        ctx.side_query = mock.MagicMock(return_value="synthesised")
        ctx.list_tools = mock.MagicMock(
            return_value=[
                {
                    "name": "Bash",
                    "description": "Run a command",
                    "parameters": {
                        "type": "object",
                        "properties": {"command": {"type": "string"}},
                        "required": ["command"],
                    },
                },
            ]
        )
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
        ctx = _blocking_ctx()
        result = handler([], ctx)
        self.assertIn("Usage", result["message"])
        ctx.send_user_message.assert_not_called()
        ctx.side_query.assert_not_called()

    def test_short_question_runs_side_query(self):
        """Short text without tool keywords → pure side question."""
        handler = fir_ext._command_handlers["aside"]
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(return_value="it means X")
        result = handler(["what", "does", "that", "mean?"], ctx)
        self.assertIn("aside:", result["message"])
        self.assertIn("it means X", result["message"])
        ctx.send_user_message.assert_not_called()

    def test_tool_request_sends_user_message(self):
        """Longer text with tool keywords → delegate to agent."""
        handler = fir_ext._command_handlers["aside"]
        ctx = _blocking_ctx()
        args = [
            "read",
            "the",
            "5",
            "largest",
            ".go",
            "files",
            "and",
            "summarise",
            "their",
            "purpose",
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
        ctx = _blocking_ctx()
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
        mod = self._load_with_advisor({"provider": "anthropic", "model": "claude-opus-4-x"})
        ctx = self._ctx()
        mod._run_aside([], "q", ctx, escalate=True)
        kwargs = ctx.side_query.call_args.kwargs
        # effort is always passed by name (None when no override is configured).
        self.assertIsNone(kwargs["effort"])
        self.assertEqual(kwargs["model"], "claude-opus-4-x")

    def test_no_escalate_uses_no_overrides(self):
        mod = self._load_with_advisor({"provider": "anthropic", "model": "claude-opus-4-x"})
        ctx = self._ctx(side_query_result="plain reply")
        result = mod._run_aside([], "q", ctx, escalate=False)
        # All overrides default to None when not escalating.
        self.assertEqual(
            ctx.side_query.call_args.kwargs,
            {"model": None, "provider": None, "effort": None},
        )
        # No advisor prefix on the output.
        self.assertEqual(result["content"][0]["text"], "plain reply")

    def test_escalate_ignored_when_no_advisor_configured(self):
        mod = self._load_with_advisor(None)
        ctx = self._ctx(side_query_result="plain reply")
        result = mod._run_aside([], "q", ctx, escalate=True)
        # Even with escalate=True, all overrides remain None.
        self.assertEqual(
            ctx.side_query.call_args.kwargs,
            {"model": None, "provider": None, "effort": None},
        )
        self.assertEqual(result["content"][0]["text"], "plain reply")

    def test_tool_schema_has_no_escalate_when_unconfigured(self):
        # Reload without an advisor — escalate must NOT appear in the schema.
        mod = _load_aside()
        mod._ADVISOR = None
        params = mod._aside_tool_parameters()
        self.assertNotIn("escalate", params["properties"])
        self.assertNotIn("Escalation", mod._aside_tool_description())

    def test_executor_advisor_pattern_gather_then_escalate(self):
        """Demonstrate the full executor/advisor pattern.

        1. Executor (cheap model) gathers data via aside without escalate.
        2. Executor then escalates to advisor with escalate=True for a decision.
        """
        mod = self._load_with_advisor({"provider": "anthropic", "model": "claude-opus-4-x"})

        # Step 1: Cheap model gathers data (no escalate).
        # Build a proper mock context with call_tool, list_tools, and side_query.
        ctx_gather = _blocking_ctx()
        ctx_gather.call_tool = mock.MagicMock(
            return_value={"content": [{"text": "Found 3 .go files"}], "is_error": False}
        )
        ctx_gather.list_tools = mock.MagicMock(
            return_value=[
                {
                    "name": "Bash",
                    "description": "Run bash",
                    "parameters": {
                        "type": "object",
                        "properties": {"command": {"type": "string"}},
                        "required": ["command"],
                    },
                }
            ]
        )
        ctx_gather.side_query = mock.MagicMock(return_value="Found 3 .go files, 5KB total")
        ctx_gather.report_progress = mock.MagicMock()

        gather_tools = [
            {
                "name": "Bash",
                "title": "Find large Go files",
                "params": {"command": "find . -name '*.go' -size +1k"},
            }
        ]
        gather_result = mod._run_aside(
            gather_tools,
            "How many large .go files are there?",
            ctx_gather,
            escalate=False,
        )

        # Assertion: no model override in the side_query call (escalate=False).
        self.assertEqual(
            ctx_gather.side_query.call_args.kwargs,
            {"model": None, "provider": None, "effort": None},
        )
        self.assertEqual(gather_result["content"][0]["text"], "Found 3 .go files, 5KB total")
        # No advisor trace prefix.
        self.assertFalse(gather_result["content"][0]["text"].startswith("[advisor:"))

        # Step 2: Escalate to advisor for planning (escalate=True).
        ctx_advise = self._ctx(
            side_query_result="Recommend: refactor all 3 into a module to improve testability."
        )
        advise_result = mod._run_aside(
            [],  # no tools, just ask the advisor
            "Given those 3 large files, what's the best refactoring strategy?",
            ctx_advise,
            escalate=True,
        )

        # Assertion: model/provider were passed to side_query.
        kwargs = ctx_advise.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-opus-4-x")
        self.assertEqual(kwargs["provider"], "anthropic")

        # Output has the advisor trace prefix so the AI knows this came from the advisor.
        text = advise_result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor: anthropic/claude-opus-4-x]"))
        self.assertIn(
            "Recommend: refactor all 3 into a module", advise_result["content"][0]["text"]
        )


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
        # Default is now an ordered fallback CHAIN (list of specs). The head
        # tracks _DEFAULT_ADVISOR_SPEC; if someone bumps it to a newer
        # flagship, the test still passes — we only assert the family.
        self.assertIsInstance(cfg, list)
        self.assertGreaterEqual(len(cfg), 1)
        head = cfg[0]
        self.assertEqual(head["provider"], "anthropic")
        self.assertTrue(head["model"].startswith("claude-"))

    def test_default_is_anthropic_flagship(self):
        # Floor for "no config" UX. Bump alongside _DEFAULT_ADVISOR_SPEC.
        spec = self.mod._DEFAULT_ADVISOR_SPEC
        self.assertTrue(spec.startswith("anthropic/claude-"))
        # Fable (the Mythos-class flagship) or a 4.7+ Opus are acceptable.
        model = spec.split("/", 1)[1]
        if model.startswith("claude-fable-"):
            major = int(model.split("claude-fable-")[1].split("-")[0])
            self.assertGreaterEqual(major, 5)
        else:
            self.assertTrue(model.startswith("claude-opus-"))
            tail = model.split("claude-opus-")[1]
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
        # the bundled default chain so the feature keeps working.
        self._cfg_path.write_text('{"advisor": "not-a-spec"}')
        cfg = self.mod._load_advisor_config()
        self.assertIsNotNone(cfg)
        self.assertIsInstance(cfg, list)
        self.assertEqual(cfg[0]["provider"], "anthropic")

    def test_corrupt_json_falls_back_to_default(self):
        self._cfg_path.write_text("not json")
        cfg = self.mod._load_advisor_config()
        self.assertIsNotNone(cfg)
        self.assertIsInstance(cfg, list)

    def test_string_config_returns_single_dict_backcompat(self):
        # A single string stays a single dict (back-compat) — NOT a chain.
        self._cfg_path.write_text('{"advisor": "anthropic/claude-opus-4-8:high"}')
        cfg = self.mod._load_advisor_config()
        self.assertEqual(
            cfg,
            {"provider": "anthropic", "model": "claude-opus-4-8", "effort": "high"},
        )

    def test_array_config_returns_chain(self):
        self._cfg_path.write_text(
            '{"advisor": ["anthropic/claude-fable-5:high", "anthropic/claude-opus-4-8"]}'
        )
        cfg = self.mod._load_advisor_config()
        self.assertEqual(
            cfg,
            [
                {"provider": "anthropic", "model": "claude-fable-5", "effort": "high"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ],
        )

    def test_array_skips_malformed_elements(self):
        self._cfg_path.write_text('{"advisor": ["bad-no-slash", "anthropic/claude-opus-4-8", 42]}')
        cfg = self.mod._load_advisor_config()
        self.assertEqual(cfg, [{"provider": "anthropic", "model": "claude-opus-4-8"}])

    def test_array_all_malformed_falls_back_to_default(self):
        self._cfg_path.write_text('{"advisor": ["bad", "also-bad"]}')
        cfg = self.mod._load_advisor_config()
        self.assertIsInstance(cfg, list)
        # Fell through to the bundled default chain (head = flagship).
        self.assertEqual(cfg[0]["provider"], "anthropic")


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

    def test_show_when_chain(self):
        # An array config renders as an arrow-joined chain.
        self._cfg_path.write_text(
            '{"advisor": ["anthropic/claude-fable-5", "anthropic/claude-opus-4-8"]}'
        )
        self.mod._ADVISOR = [
            {"provider": "anthropic", "model": "claude-fable-5"},
            {"provider": "anthropic", "model": "claude-opus-4-8"},
        ]
        result = self._handler()([], mock.MagicMock())
        self.assertIn("anthropic/claude-fable-5 -> anthropic/claude-opus-4-8", result["message"])

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

        self._cfg_path.write_text(
            _json.dumps({"advisor": "anthropic/claude-opus-4-x", "future_key": "x"})
        )
        self._handler()(["off"], mock.MagicMock())
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data, {"advisor": "off", "future_key": "x"})


# ---------------------------------------------------------------------------
# Delegate configuration & de-escalation
# ---------------------------------------------------------------------------


class TestDelegateRouting(unittest.TestCase):
    """Tool-level behaviour when a delegate is configured and 'delegate' is set."""

    def _ctx(self, side_query_result="delegate reply"):
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(return_value=side_query_result)
        return ctx

    def _load_with_delegate(self, delegate_cfg, advisor_cfg=None):
        """Reload aside.py with patched _DELEGATE / _ADVISOR module globals."""
        mod = _load_aside()
        mod._DELEGATE = delegate_cfg
        mod._ADVISOR = advisor_cfg
        return mod

    def test_delegate_routes_to_delegate_model(self):
        mod = self._load_with_delegate(
            {"provider": "anthropic", "model": "claude-haiku-4-5", "effort": "low"}
        )
        ctx = self._ctx(side_query_result="cheap summary")
        result = mod._run_aside([], "summarise these logs", ctx, delegate=True)

        self.assertFalse(result["is_error"])
        ctx.side_query.assert_called_once()
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")
        self.assertEqual(kwargs["provider"], "anthropic")
        self.assertEqual(kwargs["effort"], "low")
        # Output prefixed with the delegate trace line.
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[delegate: anthropic/claude-haiku-4-5:low]"))
        self.assertIn("cheap summary", text)

    def test_delegate_without_effort_omits_effort_kwarg(self):
        mod = self._load_with_delegate({"provider": "anthropic", "model": "claude-haiku-4-5"})
        ctx = self._ctx()
        result = mod._run_aside([], "q", ctx, delegate=True)
        kwargs = ctx.side_query.call_args.kwargs
        self.assertIsNone(kwargs["effort"])
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[delegate: anthropic/claude-haiku-4-5]"))

    def test_no_delegate_uses_no_overrides(self):
        mod = self._load_with_delegate({"provider": "anthropic", "model": "claude-haiku-4-5"})
        ctx = self._ctx(side_query_result="plain reply")
        result = mod._run_aside([], "q", ctx, delegate=False)
        self.assertEqual(
            ctx.side_query.call_args.kwargs,
            {"model": None, "provider": None, "effort": None},
        )
        # No delegate prefix on the output.
        self.assertEqual(result["content"][0]["text"], "plain reply")

    def test_delegate_ignored_when_no_delegate_configured(self):
        mod = self._load_with_delegate(None)
        ctx = self._ctx(side_query_result="plain reply")
        result = mod._run_aside([], "q", ctx, delegate=True)
        self.assertEqual(
            ctx.side_query.call_args.kwargs,
            {"model": None, "provider": None, "effort": None},
        )
        self.assertEqual(result["content"][0]["text"], "plain reply")

    def test_escalate_and_delegate_both_true_is_error(self):
        mod = self._load_with_delegate(
            {"provider": "anthropic", "model": "claude-haiku-4-5"},
            advisor_cfg={"provider": "anthropic", "model": "claude-fable-5"},
        )
        ctx = self._ctx()
        result = mod._run_aside([], "q", ctx, escalate=True, delegate=True)
        self.assertTrue(result["is_error"])
        self.assertIn("mutually exclusive", result["content"][0]["text"])
        ctx.side_query.assert_not_called()

    def test_delegate_with_tools_routes_synthesis_to_delegate(self):
        mod = self._load_with_delegate({"provider": "anthropic", "model": "claude-haiku-4-5"})
        ctx = self._ctx(side_query_result="bulk synthesis")
        ctx.call_tool = mock.MagicMock(
            return_value={"content": [{"text": "log lines"}], "is_error": False}
        )
        ctx.list_tools = mock.MagicMock(
            return_value=[
                {
                    "name": "Bash",
                    "description": "Run a command",
                    "parameters": {
                        "type": "object",
                        "properties": {"command": {"type": "string"}},
                        "required": ["command"],
                    },
                },
            ]
        )
        result = mod._run_aside(
            [{"name": "Bash", "params": {"command": "cat big.log"}}],
            "summarise the log",
            ctx,
            delegate=True,
        )
        self.assertFalse(result["is_error"])
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")
        self.assertEqual(kwargs["provider"], "anthropic")
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[delegate: anthropic/claude-haiku-4-5]"))

    def test_tool_handler_passes_delegate_flag(self):
        self._load_with_delegate({"provider": "anthropic", "model": "claude-haiku-4-5"})
        handler = fir_ext._tool_handlers["aside"]
        ctx = self._ctx(side_query_result="via handler")
        result = handler({"instructions": "q", "delegate": True}, ctx)
        self.assertFalse(result["is_error"])
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")

    def test_tool_schema_has_no_delegate_when_unconfigured(self):
        mod = _load_aside()
        mod._DELEGATE = None
        params = mod._aside_tool_parameters()
        self.assertNotIn("delegate", params["properties"])
        self.assertNotIn("Delegation", mod._aside_tool_description())

    def test_tool_schema_has_delegate_when_configured(self):
        mod = self._load_with_delegate({"provider": "anthropic", "model": "claude-haiku-4-5"})
        params = mod._aside_tool_parameters()
        self.assertIn("delegate", params["properties"])
        self.assertIn("Delegation", mod._aside_tool_description())


class TestLoadDelegateConfig(unittest.TestCase):
    """Cover the default-vs-config matrix for _load_delegate_config."""

    def setUp(self):
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self._prev_dirs = list(fir_ext.config_dirs)
        fir_ext.config_dirs = [self._tmp.name]
        self.addCleanup(lambda: setattr(fir_ext, "config_dirs", self._prev_dirs))
        self.mod = _load_aside()
        self._cfg_path = self.mod.Path(self._tmp.name) / "aside.json"

    def test_missing_file_returns_default(self):
        cfg = self.mod._load_delegate_config()
        self.assertIsNotNone(cfg)
        self.assertEqual(cfg["provider"], "anthropic")
        self.assertTrue(cfg["model"].startswith("claude-haiku-"))

    def test_explicit_off_returns_none(self):
        self._cfg_path.write_text('{"delegate": "off"}')
        self.assertIsNone(self.mod._load_delegate_config())

    def test_explicit_null_returns_none(self):
        self._cfg_path.write_text('{"delegate": null}')
        self.assertIsNone(self.mod._load_delegate_config())

    def test_pinned_spec_returns_pinned(self):
        self._cfg_path.write_text('{"delegate": "openai/gpt-mini:low"}')
        cfg = self.mod._load_delegate_config()
        self.assertEqual(
            cfg,
            {"provider": "openai", "model": "gpt-mini", "effort": "low"},
        )

    def test_malformed_spec_falls_back_to_default(self):
        self._cfg_path.write_text('{"delegate": "not-a-spec"}')
        cfg = self.mod._load_delegate_config()
        self.assertIsNotNone(cfg)
        self.assertEqual(cfg["provider"], "anthropic")

    def test_corrupt_json_falls_back_to_default(self):
        self._cfg_path.write_text("not json")
        cfg = self.mod._load_delegate_config()
        self.assertIsNotNone(cfg)

    def test_delegate_key_independent_of_advisor_key(self):
        # Disabling the advisor must not disable the delegate, and vice versa.
        self._cfg_path.write_text('{"advisor": "off"}')
        self.assertIsNone(self.mod._load_advisor_config())
        self.assertIsNotNone(self.mod._load_delegate_config())
        self._cfg_path.write_text('{"delegate": "off"}')
        self.assertIsNotNone(self.mod._load_advisor_config())
        self.assertIsNone(self.mod._load_delegate_config())


class TestAsideDelegateCommand(unittest.TestCase):
    """The /aside-delegate slash command — show, set, off."""

    def setUp(self):
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self._prev_dirs = list(fir_ext.config_dirs)
        fir_ext.config_dirs = [self._tmp.name]
        self.addCleanup(lambda: setattr(fir_ext, "config_dirs", self._prev_dirs))
        self.mod = _load_aside()
        self._cfg_path = self.mod.Path(self._tmp.name) / "aside.json"

    def _handler(self):
        return fir_ext._command_handlers["aside-delegate"]

    def test_registered(self):
        names = {c["name"] for c in fir_ext._commands}
        self.assertIn("aside-delegate", names)

    def test_show_when_disabled(self):
        self.mod._DELEGATE = None
        result = self._handler()([], mock.MagicMock())
        self.assertIn("disabled", result["message"])

    def test_show_when_default(self):
        self.mod._DELEGATE = {"provider": "anthropic", "model": "claude-haiku-4-5"}
        result = self._handler()([], mock.MagicMock())
        self.assertIn("anthropic/claude-haiku-4-5", result["message"])
        self.assertIn("default", result["message"])

    def test_show_when_pinned(self):
        self._cfg_path.write_text('{"delegate": "anthropic/claude-haiku-4-5"}')
        self.mod._DELEGATE = {"provider": "anthropic", "model": "claude-haiku-4-5"}
        result = self._handler()([], mock.MagicMock())
        self.assertIn("anthropic/claude-haiku-4-5", result["message"])
        self.assertIn(str(self._cfg_path), result["message"])

    def test_set_writes_config_file(self):
        result = self._handler()(["anthropic/claude-haiku-4-5:low"], mock.MagicMock())
        self.assertIn("set to anthropic/claude-haiku-4-5:low", result["message"])
        self.assertTrue(self._cfg_path.is_file())
        import json as _json

        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data, {"delegate": "anthropic/claude-haiku-4-5:low"})

    def test_set_rejects_malformed_spec(self):
        result = self._handler()(["claude-haiku-4-5"], mock.MagicMock())
        self.assertIn("malformed spec", result["message"])
        self.assertFalse(self._cfg_path.is_file())

    def test_off_writes_explicit_off_marker(self):
        result = self._handler()(["off"], mock.MagicMock())
        self.assertIn("disabled", result["message"])
        self.assertTrue(self._cfg_path.is_file())
        import json as _json

        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(data, {"delegate": "off"})

    def test_off_preserves_other_keys(self):
        # /aside-delegate off must only flip the delegate key — the advisor
        # pin (and any future keys) must survive.
        import json as _json

        self._cfg_path.write_text(
            _json.dumps(
                {"advisor": "anthropic/claude-fable-5", "delegate": "anthropic/claude-haiku-4-5"}
            )
        )
        self._handler()(["off"], mock.MagicMock())
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(
            data,
            {"advisor": "anthropic/claude-fable-5", "delegate": "off"},
        )

    def test_set_preserves_advisor_key(self):
        import json as _json

        self._cfg_path.write_text('{"advisor": "off"}')
        self._handler()(["anthropic/claude-haiku-4-5"], mock.MagicMock())
        data = _json.loads(self._cfg_path.read_text())
        self.assertEqual(
            data,
            {"advisor": "off", "delegate": "anthropic/claude-haiku-4-5"},
        )


class DefaultAdvisorTracksHighestAnthropicFlagship(unittest.TestCase):
    """Guards aside.py's _DEFAULT_ADVISOR_SPEC.

    When no aside.json exists the extension falls back to a hard-coded model
    spec — that spec must always point at the strongest Anthropic flagship
    baked into fir's bundled model registry: the highest
    claude-fable-<major>[-<minor>], falling back to the highest
    claude-opus-X-Y only when no fable exists. We parse both sides as text
    and compare.

    Date-suffixed aliases (claude-opus-4-1-20250805, claude-opus-4-20250514)
    are intentionally ignored — those are short-lived; the bare form is the
    long-lived alias users pin to.
    """

    _ASIDE_PY = os.path.join(_ext_dir, "aside.py")
    _MODELS_GO = os.path.join(
        os.path.dirname(os.path.abspath(__file__)),
        "..",
        "..",
        "ai",
        "models_generated.go",
    )

    def test_default_advisor_matches_highest_flagship(self):
        import re

        mod = _load_aside()

        with open(self._ASIDE_PY, encoding="utf-8") as f:
            aside_src = f.read()
        m = re.search(r'_DEFAULT_ADVISOR_SPEC\s*=\s*"([^"]+)"', aside_src)
        self.assertIsNotNone(m, "aside.py: _DEFAULT_ADVISOR_SPEC literal not found")
        assert m is not None  # for type checker
        got = m.group(1)

        with open(self._MODELS_GO, encoding="utf-8") as f:
            models_src = f.read()

        # Gather every model id registered under the anthropic provider, then
        # pick the strongest flagship via the SAME ranking helper the runtime
        # degrade path uses (_best_anthropic_flagship). This guarantees test
        # and runtime agree on "which model is the flagship".
        id_re = re.compile(r'ID:\s*"([^"]+)"(?:[^}]*?)Provider:\s*"anthropic"', re.DOTALL)
        anthropic_ids = id_re.findall(models_src)
        best = mod._best_anthropic_flagship(anthropic_ids)

        self.assertIsNotNone(
            best,
            "no claude-fable or claude-opus models registered under anthropic provider",
        )
        want_spec = "anthropic/" + best
        self.assertEqual(
            got,
            want_spec,
            "aside.py _DEFAULT_ADVISOR_SPEC out of sync with model registry:\n"
            f"  got:  {got}\n"
            f"  want: {want_spec}\n"
            f"\nFix: edit pkg/resources/builtin_extensions/aside.py and update "
            f"_DEFAULT_ADVISOR_SPEC to {want_spec!r}.",
        )


class DefaultDelegateTracksHighestAnthropicHaiku(unittest.TestCase):
    """Guards aside.py's _DEFAULT_DELEGATE_SPEC.

    The default delegate must always point at the cheapest current Anthropic
    tier baked into fir's bundled model registry — the highest bare
    claude-haiku-X-Y registered under the anthropic provider. Same
    parse-both-sides-as-text approach as the advisor drift test;
    date-suffixed aliases are ignored.
    """

    _ASIDE_PY = os.path.join(_ext_dir, "aside.py")
    _MODELS_GO = os.path.join(
        os.path.dirname(os.path.abspath(__file__)),
        "..",
        "..",
        "ai",
        "models_generated.go",
    )

    def test_default_delegate_matches_highest_haiku(self):
        import re

        mod = _load_aside()

        with open(self._ASIDE_PY, encoding="utf-8") as f:
            aside_src = f.read()
        m = re.search(r'_DEFAULT_DELEGATE_SPEC\s*=\s*"([^"]+)"', aside_src)
        self.assertIsNotNone(m, "aside.py: _DEFAULT_DELEGATE_SPEC literal not found")
        assert m is not None  # for type checker
        got = m.group(1)

        with open(self._MODELS_GO, encoding="utf-8") as f:
            models_src = f.read()

        # Gather all anthropic ids and pick the highest Haiku via the same
        # runtime ranking helper (_best_anthropic_haiku).
        id_re = re.compile(r'ID:\s*"([^"]+)"(?:[^}]*?)Provider:\s*"anthropic"', re.DOTALL)
        anthropic_ids = id_re.findall(models_src)
        best = mod._best_anthropic_haiku(anthropic_ids)

        self.assertIsNotNone(
            best,
            "no claude-haiku-<major>-<minor> models registered under anthropic provider",
        )
        want_spec = "anthropic/" + best
        self.assertEqual(
            got,
            want_spec,
            "aside.py _DEFAULT_DELEGATE_SPEC out of sync with model registry:\n"
            f"  got:  {got}\n"
            f"  want: {want_spec}\n"
            f"\nFix: edit pkg/resources/builtin_extensions/aside.py and update "
            f"_DEFAULT_DELEGATE_SPEC to {want_spec!r}.",
        )


class TestRankingHelpers(unittest.TestCase):
    """Ranking helpers shared by runtime degrade + drift tests."""

    def setUp(self):
        self.mod = _load_aside()

    def test_flagship_fable_beats_opus(self):
        self.assertGreater(
            self.mod._rank_flagship("claude-fable-5"),
            self.mod._rank_flagship("claude-opus-4-9"),
        )

    def test_flagship_orders_by_version(self):
        self.assertGreater(
            self.mod._rank_flagship("claude-opus-4-8"),
            self.mod._rank_flagship("claude-opus-4-7"),
        )

    def test_flagship_rejects_date_stamp_and_nonflagship(self):
        self.assertIsNone(self.mod._rank_flagship("claude-opus-4-1-20250805"))
        self.assertIsNone(self.mod._rank_flagship("claude-haiku-4-5"))

    def test_best_flagship_picks_highest(self):
        ids = ["claude-haiku-4-5", "claude-opus-4-7", "claude-opus-4-8"]
        self.assertEqual(self.mod._best_anthropic_flagship(ids), "claude-opus-4-8")

    def test_best_flagship_prefers_fable(self):
        ids = ["claude-opus-4-9", "claude-fable-5", "claude-haiku-4-5"]
        self.assertEqual(self.mod._best_anthropic_flagship(ids), "claude-fable-5")

    def test_best_flagship_none_when_absent(self):
        self.assertIsNone(self.mod._best_anthropic_haiku(["claude-opus-4-8"]))
        self.assertIsNone(self.mod._best_anthropic_flagship(["claude-haiku-4-5"]))

    def test_best_haiku_picks_highest(self):
        ids = ["claude-haiku-4-5", "claude-haiku-3-5"]
        self.assertEqual(self.mod._best_anthropic_haiku(ids), "claude-haiku-4-5")


class TestModelUnavailableSignature(unittest.TestCase):
    """_is_model_unavailable_error matching (Layer B)."""

    def setUp(self):
        self.mod = _load_aside()

    def test_matches_not_found_and_404(self):
        for s in (
            "not_found_error: model X does not exist",
            "HTTP 400: invalid model",
            "the model is not available",
            "404 page not found",
            "unknown model claude-fable-5",
        ):
            self.assertTrue(self.mod._is_model_unavailable_error(s), s)

    def test_does_not_match_overflow(self):
        self.assertFalse(
            self.mod._is_model_unavailable_error("side-query: Input exceeds context window limit")
        )

    def test_does_not_match_generic_error(self):
        self.assertFalse(self.mod._is_model_unavailable_error("connection reset by peer"))


class TestAvailabilityDegrade(unittest.TestCase):
    """Layer A — degrade advisor/delegate to the highest available model."""

    def _ctx(self, available, side_query_result="reply"):
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(return_value=side_query_result)
        ctx.available_models = mock.MagicMock(return_value=available)
        return ctx

    def _mod(self, advisor=None, delegate=None):
        mod = _load_aside()
        mod._ADVISOR = advisor
        mod._DELEGATE = delegate
        return mod

    def test_fable_unavailable_degrades_to_highest_opus(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})
        available = [
            {"provider": "anthropic", "id": "claude-opus-4-7", "name": "Opus 4.7"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus 4.8"},
            {"provider": "anthropic", "id": "claude-haiku-4-5", "name": "Haiku"},
        ]
        ctx = self._ctx(available, side_query_result="opus reply")
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-opus-4-8")
        self.assertEqual(kwargs["provider"], "anthropic")
        text = result["content"][0]["text"]
        self.assertTrue(
            text.startswith(
                "[advisor: anthropic/claude-opus-4-8 (fallback: claude-fable-5 unavailable)]"
            ),
            text,
        )

    def test_configured_model_available_used_as_is(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        ctx = self._ctx(available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-fable-5")
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor: anthropic/claude-fable-5]"), text)
        self.assertNotIn("fallback", text)

    def test_empty_available_uses_configured_spec(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})
        ctx = self._ctx([])
        mod._run_aside([], "q", ctx, escalate=True)
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-fable-5")

    def test_no_anthropic_flagship_disables_escalation(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})
        # Only a Haiku available — no flagship to degrade to → advisor None.
        available = [{"provider": "anthropic", "id": "claude-haiku-4-5", "name": "Haiku"}]
        ctx = self._ctx(available, side_query_result="executor reply")
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertEqual(
            ctx.side_query.call_args.kwargs,
            {"model": None, "provider": None, "effort": None},
        )
        # No advisor prefix — escalation was disabled this session.
        self.assertEqual(result["content"][0]["text"], "executor reply")

    def test_delegate_degrades_to_highest_haiku(self):
        mod = self._mod(delegate={"provider": "anthropic", "model": "claude-haiku-9-9"})
        available = [
            {"provider": "anthropic", "id": "claude-haiku-4-5", "name": "Haiku 4.5"},
            {"provider": "anthropic", "id": "claude-haiku-3-5", "name": "Haiku 3.5"},
        ]
        ctx = self._ctx(available, side_query_result="cheap reply")
        result = mod._run_aside([], "q", ctx, delegate=True)
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")
        text = result["content"][0]["text"]
        self.assertTrue(
            text.startswith(
                "[delegate: anthropic/claude-haiku-4-5 (fallback: claude-haiku-9-9 unavailable)]"
            ),
            text,
        )

    def test_effort_preserved_through_degrade(self):
        mod = self._mod(
            advisor={"provider": "anthropic", "model": "claude-fable-5", "effort": "high"}
        )
        available = [{"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"}]
        ctx = self._ctx(available)
        mod._run_aside([], "q", ctx, escalate=True)
        self.assertEqual(ctx.side_query.call_args.kwargs["effort"], "high")


class TestReactiveFallback(unittest.TestCase):
    """Layer B — escalated/delegated call falls back to the executor model."""

    def _mod(self, advisor=None, delegate=None):
        mod = _load_aside()
        mod._ADVISOR = advisor
        mod._DELEGATE = delegate
        return mod

    def _ctx(self, sq):
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(side_effect=sq)
        # available_models returns [] (MagicMock) → configured spec used as-is.
        return ctx

    def test_unavailable_advisor_retries_on_executor(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            if model is not None:
                raise RuntimeError("not_found_error: model claude-fable-5 does not exist")
            return "executor answer"

        ctx = self._ctx(sq)
        result = mod._run_aside([], "design tradeoff?", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor unavailable — answered on executor model]"), text)
        self.assertIn("executor answer", text)
        # Two calls: routed (failed) then executor (succeeded).
        self.assertEqual(ctx.side_query.call_count, 2)

    def test_unavailable_delegate_retries_on_executor(self):
        mod = self._mod(delegate={"provider": "anthropic", "model": "claude-haiku-4-5"})

        def sq(question, model=None, provider=None, effort=None):
            if model is not None:
                raise RuntimeError("HTTP 400: invalid model")
            return "executor answer"

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, delegate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(
            text.startswith("[delegate unavailable — answered on executor model]"), text
        )

    def test_non_unavailability_error_still_surfaces(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            raise RuntimeError("connection reset by peer")

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        self.assertIn("aside LLM call failed", result["content"][0]["text"])
        # Only the routed call — no executor retry for a generic error.
        self.assertEqual(ctx.side_query.call_count, 1)

    def test_overflow_error_hits_hint_not_fallback(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            raise RuntimeError("side-query: Input exceeds context window limit")

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("context window full", text)
        self.assertEqual(ctx.side_query.call_count, 1)

    def test_executor_retry_also_failing_chains_both_errors(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            if model is not None:
                raise RuntimeError("not_found_error: model gone")
            raise RuntimeError("not_found_error: still gone")

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("aside LLM call failed", text)
        # BOTH errors surfaced — neither the advisor chain error nor the
        # executor fallback error is discarded.
        self.assertIn("chain exhausted", text)
        self.assertIn("model gone", text)
        self.assertIn("executor fallback also failed", text)
        self.assertIn("still gone", text)


class TestSessionUnavailableMemo(unittest.TestCase):
    """Per-session 404 memo — once a model 404s, escalation skips it for the
    rest of the session and Layer A degrades to the next live flagship instead
    of re-probing the dead model on every call."""

    def _mod(self, advisor=None, delegate=None):
        mod = _load_aside()
        mod._ADVISOR = advisor
        mod._DELEGATE = delegate
        return mod

    def test_fresh_load_starts_with_empty_memo(self):
        mod = self._mod()
        self.assertEqual(mod._SESSION_UNAVAILABLE, set())

    def test_mark_ignores_falsy(self):
        mod = self._mod()
        mod._mark_model_unavailable(None, "x")
        mod._mark_model_unavailable("anthropic", None)
        self.assertEqual(mod._SESSION_UNAVAILABLE, set())
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        self.assertEqual(mod._SESSION_UNAVAILABLE, {"anthropic/claude-fable-5"})

    def test_degrade_role_excludes_memoized_model(self):
        mod = self._mod()
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        cfg = {"provider": "anthropic", "model": "claude-fable-5"}
        # Not memoized -> fable (in the live catalogue) used as-is.
        self.assertEqual(mod._degrade_role(cfg, available, "advisor")["model"], "claude-fable-5")
        # Memoized -> degrade to opus, even though fable is still in the catalogue.
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        out = mod._degrade_role(cfg, available, "advisor")
        self.assertEqual(out["model"], "claude-opus-4-8")
        self.assertEqual(out["_fallback"], "claude-fable-5")

    def test_first_404_memoizes_then_second_call_degrades_to_opus(self):
        # Anthropic still LISTS fable in /v1/models but it 404s on use. Layer A
        # cannot pre-empt that on call 1 (fable looks available); Layer B catches
        # the 404 and memoizes. Call 2 then degrades to opus with no failed probe.
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model == "claude-fable-5":
                raise RuntimeError("not_found_error: Claude Fable 5 is not available")
            return "answer"

        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(side_effect=sq)
        ctx.available_models = mock.MagicMock(return_value=available)

        r1 = mod._run_aside([], "q1", ctx, escalate=True)
        self.assertFalse(r1["is_error"])
        self.assertTrue(
            r1["content"][0]["text"].startswith(
                "[advisor unavailable — answered on executor model]"
            ),
            r1["content"][0]["text"],
        )
        self.assertIn("anthropic/claude-fable-5", mod._SESSION_UNAVAILABLE)

        r2 = mod._run_aside([], "q2", ctx, escalate=True)
        self.assertFalse(r2["is_error"])
        self.assertTrue(
            r2["content"][0]["text"].startswith(
                "[advisor: anthropic/claude-opus-4-8 (fallback: claude-fable-5 unavailable)]"
            ),
            r2["content"][0]["text"],
        )
        # fable probed once (call 1), executor fallback once, then opus -- no
        # second fable probe.
        self.assertEqual(calls, ["claude-fable-5", None, "claude-opus-4-8"])


class TestChainWalk(unittest.TestCase):
    """Ordered fallback CHAIN semantics — walk candidates, advance past dead
    models, terminate on the executor model."""

    def _mod(self, advisor=None, delegate=None):
        mod = _load_aside()
        mod._ADVISOR = advisor
        mod._DELEGATE = delegate
        return mod

    def _ctx(self, sq, available=None):
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(side_effect=sq)
        if available is not None:
            ctx.available_models = mock.MagicMock(return_value=available)
        return ctx

    def test_dead_first_model_advances_to_live_second(self):
        # Explicit array chain: fable dead, opus-4-8 answers.
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model == "claude-fable-5":
                raise RuntimeError("not_found_error: Fable is not available")
            return "opus answered"

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        # Second candidate answered; trace notes the dead head as a fallback.
        self.assertTrue(
            text.startswith(
                "[advisor: anthropic/claude-opus-4-8 (fallback: claude-fable-5 unavailable)]"
            ),
            text,
        )
        self.assertIn("opus answered", text)
        # fable probed once, then opus — no executor fallback (2 calls only).
        self.assertEqual(calls, ["claude-fable-5", "claude-opus-4-8"])

    def test_whole_chain_dead_falls_to_executor_with_note(self):
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model is not None:
                raise RuntimeError("not_found_error: gone")
            return "executor answered"

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor unavailable — answered on executor model]"), text)
        self.assertIn("executor answered", text)
        # both candidates probed, then executor (model=None).
        self.assertEqual(calls, ["claude-fable-5", "claude-opus-4-8", None])
        # both memoized.
        self.assertIn("anthropic/claude-fable-5", mod._SESSION_UNAVAILABLE)
        self.assertIn("anthropic/claude-opus-4-8", mod._SESSION_UNAVAILABLE)

    def test_chain_and_executor_both_dead_chains_both_errors(self):
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]

        def sq(question, model=None, provider=None, effort=None):
            if model == "claude-fable-5":
                raise RuntimeError("not_found_error: fable gone")
            if model == "claude-opus-4-8":
                raise RuntimeError("not_found_error: opus gone")
            raise RuntimeError("not_found_error: executor gone too")

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("chain exhausted", text)
        self.assertIn("fable gone", text)
        self.assertIn("opus gone", text)
        self.assertIn("executor fallback also failed", text)
        self.assertIn("executor gone too", text)

    def test_overflow_not_swallowed_by_chain(self):
        # A context-overflow error on the first candidate must surface with
        # its hint — NOT be treated as unavailable and advance the chain.
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            raise RuntimeError("side-query: Input exceeds context window limit")

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("context window full", text)
        # Only the first candidate was tried — no advance, no executor retry.
        self.assertEqual(calls, ["claude-fable-5"])

    def test_memo_skips_dead_model_on_next_call(self):
        # First call kills fable; second call must not re-probe it — the chain
        # resolves to opus-4-8 directly.
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model == "claude-fable-5":
                raise RuntimeError("not_found_error: fable gone")
            return "opus answered"

        ctx = self._ctx(sq, available=available)
        mod._run_aside([], "q1", ctx, escalate=True)
        mod._run_aside([], "q2", ctx, escalate=True)
        # Call 1: fable, opus. Call 2: opus only (fable memoized → skipped).
        self.assertEqual(calls, ["claude-fable-5", "claude-opus-4-8", "claude-opus-4-8"])

    def test_memoized_model_not_reprobed_under_unknown_availability(self):
        # Availability unknown ([]): once a chain head 404s and is memoized,
        # a later call must NOT re-probe it — the resolved chain skips it and
        # advances to the next live candidate directly.
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model == "claude-fable-5":
                raise RuntimeError("not_found_error: fable gone")
            return "opus answered"

        # No available_models seeded → _query_available_models returns [].
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(side_effect=sq)

        mod._run_aside([], "q1", ctx, escalate=True)
        mod._run_aside([], "q2", ctx, escalate=True)
        # Call 1: fable (404) then opus. Call 2: opus only — fable skipped,
        # not re-probed.
        self.assertEqual(calls, ["claude-fable-5", "claude-opus-4-8", "claude-opus-4-8"])

    def test_default_chain_head_is_flagship_spec(self):
        # The bundled default advisor chain leads with _DEFAULT_ADVISOR_SPEC.
        mod = _load_aside()
        chain = mod._parse_advisor_chain(mod._DEFAULT_ADVISOR_CHAIN)
        self.assertGreaterEqual(len(chain), 2)
        head_spec = mod._format_advisor_spec(chain[0])
        self.assertEqual(head_spec, mod._DEFAULT_ADVISOR_SPEC)

    def test_delegate_chain_from_array_config(self):
        mod = self._mod(
            delegate=[
                {"provider": "anthropic", "model": "claude-haiku-9-9"},
                {"provider": "anthropic", "model": "claude-haiku-4-5"},
            ]
        )
        available = [
            {"provider": "anthropic", "id": "claude-haiku-4-5", "name": "Haiku"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            return "cheap reply"

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, delegate=True)
        self.assertFalse(result["is_error"])
        # haiku-9-9 not available → Layer A degrades to haiku-4-5; the second
        # entry also resolves to haiku-4-5 → deduped. Single live candidate.
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-haiku-4-5")


class TestAdviseCommand(unittest.TestCase):
    """/advise — slash command that always routes to the advisor model."""

    def setUp(self):
        self.mod = _load_aside()

    def _ctx(self, side_query_result="advisor reply"):
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(return_value=side_query_result)
        return ctx

    def test_empty_args_shows_usage(self):
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-x"}
        handler = fir_ext._command_handlers["advise"]
        ctx = self._ctx()
        result = handler([], ctx)
        self.assertIn("Usage", result["message"])
        ctx.side_query.assert_not_called()

    def test_no_advisor_configured_returns_hint(self):
        self.mod._ADVISOR = None
        handler = fir_ext._command_handlers["advise"]
        ctx = self._ctx()
        result = handler(["why", "is", "the", "sky", "blue"], ctx)
        self.assertIn("No advisor configured", result["message"])
        self.assertIn("/aside-advisor", result["message"])
        ctx.side_query.assert_not_called()

    def test_routes_to_advisor_with_overrides(self):
        self.mod._ADVISOR = {
            "provider": "anthropic",
            "model": "claude-opus-4-x",
            "effort": "high",
        }
        handler = fir_ext._command_handlers["advise"]
        ctx = self._ctx(side_query_result="deep thought")
        result = handler(["what", "should", "I", "do?"], ctx)
        ctx.side_query.assert_called_once()
        kwargs = ctx.side_query.call_args.kwargs
        self.assertEqual(kwargs["model"], "claude-opus-4-x")
        self.assertEqual(kwargs["provider"], "anthropic")
        self.assertEqual(kwargs["effort"], "high")
        msg = result["message"]
        self.assertIn("advise:", msg)
        self.assertIn("[advisor: anthropic/claude-opus-4-x:high]", msg)
        self.assertIn("deep thought", msg)
        self.assertTrue(
            result.get("print_response"),
            "print_response must be True so response renders in conversation area",
        )

    def test_advisor_without_effort_passes_none(self):
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-x"}
        handler = fir_ext._command_handlers["advise"]
        ctx = self._ctx()
        handler(["q"], ctx)
        kwargs = ctx.side_query.call_args.kwargs
        self.assertIsNone(kwargs["effort"])

    def test_side_query_failure_returns_error(self):
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-x"}
        handler = fir_ext._command_handlers["advise"]
        ctx = self._ctx()
        ctx.side_query = mock.MagicMock(side_effect=RuntimeError("LLM down"))
        result = handler(["q"], ctx)
        self.assertIn("LLM down", result["message"])

    def test_unavailable_advisor_answers_on_executor(self):
        # Layer B: /advise degrades to the executor model on unavailability.
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-fable-5"}
        handler = fir_ext._command_handlers["advise"]
        ctx = self._ctx()

        def sq(question, model=None, provider=None, effort=None):
            if model is not None:
                raise RuntimeError("not_found_error: model claude-fable-5 does not exist")
            return "executor answer"

        ctx.side_query = mock.MagicMock(side_effect=sq)
        result = handler(["what", "now"], ctx)
        self.assertIn("[advisor unavailable — answered on executor model]", result["message"])
        self.assertIn("executor answer", result["message"])
        self.assertTrue(result.get("print_response"))


if __name__ == "__main__":
    unittest.main()


# ---------------------------------------------------------------------------
# Streaming side_query + observable card publication
# ---------------------------------------------------------------------------


class _FakeStream:
    """Minimal in-process stand-in for fir_ext.SideQueryStream.

    Wraps a list of deltas (dicts with type/text/tokens_out keys), exposes
    the iterator protocol, and surfaces ``.result`` / ``.error`` after the
    iterator is exhausted. ``deltas`` may be empty for the "no usable
    content" case.
    """

    def __init__(self, deltas, result=None, error=None):
        self._deltas = list(deltas)
        self.result = result
        self.error = error
        self._i = 0

    def __iter__(self):
        return self

    def __next__(self):
        if self._i >= len(self._deltas):
            raise StopIteration
        d = self._deltas[self._i]
        self._i += 1
        return fir_ext.SideQueryDelta(
            type=d.get("type", ""),
            text=d.get("text", ""),
            tokens_out=d.get("tokens_out", 0),
            seq=self._i - 1,
            raw=dict(d),
        )


def _streaming_ctx(stream_factory):
    """Build a Context mock wired to a streaming side_query_stream.

    stream_factory(question, model, provider, effort) -> _FakeStream.
    """
    ctx = mock.MagicMock(spec=fir_ext.Context)
    ctx.put_observable = mock.MagicMock()
    ctx.clear_observable = mock.MagicMock()
    ctx.report_progress = mock.MagicMock()
    ctx.list_tools = mock.MagicMock(return_value=[])
    ctx.side_query_stream = mock.MagicMock(
        side_effect=lambda question, model=None, provider=None, effort=None: stream_factory(
            question, model, provider, effort
        )
    )
    # Leave ctx.side_query alone — aside.py prefers side_query_stream when
    # both are present.
    return ctx


class TestAsideStreamingCards(unittest.TestCase):
    def setUp(self):
        self.mod = _load_aside()

    def test_success_publishes_card_with_stop_slug(self):
        deltas = [
            {"type": "thinking", "text": "mulling…"},
            {"type": "text", "text": "the answer "},
            {"type": "text", "text": "is 42"},
            {"type": "usage", "tokens_out": 12},
        ]
        result_dict = {
            "text": "the answer is 42",
            "blocks": [{"type": "text", "len": 16}],
            "finish_reason": "stop",
        }

        def factory(*_a, **_kw):
            return _FakeStream(deltas, result=result_dict)

        ctx = _streaming_ctx(factory)
        out = self.mod._run_aside([], "what?", ctx)

        self.assertFalse(out["is_error"])
        self.assertEqual(out["content"][0]["text"], "the answer is 42")
        # At least one running card and one terminal "stop" card.
        calls = list(ctx.put_observable.call_args_list)
        self.assertGreaterEqual(len(calls), 2)
        first_kwargs = calls[0].kwargs
        self.assertTrue(
            first_kwargs.get("slug") == "running" or calls[0].args[1:2] == ("running",),
            f"first card slug should be 'running', got {calls[0]}",
        )
        # Terminal card carries finish_reason as slug + full text as detail.
        terminal = calls[-1]
        slug = terminal.kwargs.get("slug") or (terminal.args[1] if len(terminal.args) > 1 else "")
        detail = terminal.kwargs.get("detail") or (
            terminal.args[2] if len(terminal.args) > 2 else ""
        )
        self.assertEqual(slug, "stop")
        self.assertEqual(detail, "the answer is 42")
        # All cards share a "query/<unix-ms>" key.
        keys = {(c.kwargs.get("key") or c.args[0]) for c in calls}
        self.assertEqual(len(keys), 1, f"all cards should share one key, got {keys}")
        only_key = next(iter(keys))
        self.assertTrue(only_key.startswith("query/"), f"unexpected key: {only_key}")

    def test_empty_redacted_response_emits_empty_slug(self):
        # Mimics what the Go side sends back when the response only carried
        # a redacted thinking block (th=0, sig>0): stream.error is set with
        # the formatted block summary and stream.result has the blocks.
        err = "side-query: response had no usable content (blocks: [thinking(th=0,sig=940)])"

        def factory(*_a, **_kw):
            return _FakeStream([], result=None, error=err)

        ctx = _streaming_ctx(factory)
        out = self.mod._run_aside([], "q", ctx)
        self.assertTrue(out["is_error"])
        self.assertIn("aside LLM call failed", out["content"][0]["text"])
        # Card slug should be empty:redacted (th=0,sig>0 ⇒ redacted).
        terminal = ctx.put_observable.call_args_list[-1]
        slug = terminal.kwargs.get("slug") or terminal.args[1]
        self.assertEqual(slug, "empty:redacted")

    def test_stream_iterator_exception_emits_err_slug(self):
        class _Boom(_FakeStream):
            def __next__(self):
                raise RuntimeError("connection reset")

        def factory(*_a, **_kw):
            return _Boom([])

        ctx = _streaming_ctx(factory)
        out = self.mod._run_aside([], "q", ctx)
        self.assertTrue(out["is_error"])
        terminal = ctx.put_observable.call_args_list[-1]
        slug = terminal.kwargs.get("slug") or terminal.args[1]
        self.assertEqual(slug, "ERR")
        detail = terminal.kwargs.get("detail") or terminal.args[2]
        self.assertIn("connection reset", detail)

    def test_advisor_override_routed_to_stream(self):
        self.mod._ADVISOR = {"provider": "anthropic", "model": "claude-opus-4-x", "effort": "high"}

        def factory(question, model, provider, effort):
            # Capture call kwargs by reference in the closure so the test
            # can verify advisor overrides reach the streaming flavor.
            captured.update(question=question, model=model, provider=provider, effort=effort)
            return _FakeStream(
                [{"type": "text", "text": "advisor reply"}],
                result={"text": "advisor reply", "finish_reason": "stop"},
            )

        captured: dict = {}
        ctx = _streaming_ctx(factory)
        out = self.mod._run_aside([], "design tradeoff?", ctx, escalate=True)
        self.assertFalse(out["is_error"])
        self.assertEqual(captured["model"], "claude-opus-4-x")
        self.assertEqual(captured["provider"], "anthropic")
        self.assertEqual(captured["effort"], "high")
