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

    # Empty-content retries sleep before the second probe in production. Zero
    # it everywhere so the suite stays fast; the tests that care about the
    # backoff assert on the constant / patch it explicitly.
    aside._EMPTY_CONTENT_RETRY_BACKOFF = 0.0
    return aside


# The verbatim failure text github.com/kfet/agent >= v0.1.3 synthesises when a
# provider asserts stop_reason=error but supplies no diagnosis. This — not the
# "no usable content" wording — is the modern shape of "this candidate cannot
# answer", and the chain walk must cool the model off and advance past it.
_PROVIDER_ERR = (
    "side-query: provider reported stop_reason=error with no error message "
    "(provider=anthropic model=claude-fable-5 blocks: [])"
)


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

        # Progress is front-loaded — clients truncate the spinner label to
        # ~12 runes, so the tool name must come first, not "Calling ".
        self.assertEqual(
            ctx_gather.report_progress.call_args_list[0].args[0],
            "Bash — Find large Go files",
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


class TestConfigReadFreshness(unittest.TestCase):
    """aside.json must be honoured after the init handshake.

    `aside` is a builtin extension, so `ext_reload` refuses it and there is
    no way to re-import mid-session. The tool decorator also evaluates
    `_advisor()` at IMPORT time — before the host's init handshake has set
    `fir_ext.config_dirs` — so a memoized read captured the built-in default
    forever and a user's aside.json pin never took effect at all.
    """

    def setUp(self):
        import tempfile

        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self._prev_dirs = list(fir_ext.config_dirs)
        self.addCleanup(lambda: setattr(fir_ext, "config_dirs", self._prev_dirs))

    def _write(self, payload):
        import json as _json
        import os

        with open(os.path.join(self._tmp.name, "aside.json"), "w") as f:
            _json.dump(payload, f)

    def test_config_written_after_import_is_honoured(self):
        # Import with NO config dirs, exactly as the runtime does.
        fir_ext.config_dirs = []
        mod = _load_aside()
        self.assertEqual(mod._advisor()[0]["model"], mod._DEFAULT_ADVISOR_SPEC.split("/")[1])
        # Init handshake lands, user has a pin on disk.
        self._write({"advisor": "openai/gpt-pinned", "delegate": "openai/gpt-cheap"})
        fir_ext.config_dirs = [self._tmp.name]
        self.assertEqual(mod._advisor(), {"provider": "openai", "model": "gpt-pinned"})
        self.assertEqual(mod._delegate(), {"provider": "openai", "model": "gpt-cheap"})

    def test_edit_mid_session_takes_effect(self):
        fir_ext.config_dirs = [self._tmp.name]
        mod = _load_aside()
        self._write({"advisor": "openai/first"})
        self.assertEqual(mod._advisor()["model"], "first")
        self._write({"advisor": "openai/second"})
        self.assertEqual(mod._advisor()["model"], "second")

    def test_explicit_override_short_circuits_the_read(self):
        # Tests (and only tests) inject by assignment; that must win over
        # whatever is on disk.
        fir_ext.config_dirs = [self._tmp.name]
        self._write({"advisor": "openai/on-disk"})
        mod = _load_aside()
        mod._ADVISOR = {"provider": "anthropic", "model": "injected"}
        self.assertEqual(mod._advisor()["model"], "injected")
        mod._ADVISOR = None
        self.assertIsNone(mod._advisor())


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


class DefaultAdvisorFallbackIsSane(unittest.TestCase):
    """Guards aside.py's _DEFAULT_ADVISOR_CHAIN — the LAST-RESORT fallback.

    This replaces the old DefaultAdvisorTracksHighestAnthropicFlagship drift
    test. The default advisor chain is now resolved at call time from
    ctx.available_models() (see _dynamic_default_chain), so the hardcoded
    constant is only used when the live registry is unknown — pinning it to
    the registry's strongest flagship would force a manual bump for no
    behavioural gain, i.e. a test that lies about what matters.

    What still matters, and is asserted here: every element of the fallback
    chain must parse, must be a rankable Anthropic flagship, must be ordered
    strongest-first, and must actually exist in fir's bundled model registry
    (a fallback pointing at a model fir has never heard of is dead weight).
    """

    _ASIDE_PY = os.path.join(_ext_dir, "aside.py")
    _MODELS_GO = os.path.join(
        os.path.dirname(os.path.abspath(__file__)),
        "..",
        "..",
        "ai",
        "models_generated.go",
    )

    def _registry_ids(self):
        import re

        with open(self._MODELS_GO, encoding="utf-8") as f:
            models_src = f.read()
        id_re = re.compile(r'ID:\s*"([^"]+)"(?:[^}]*?)Provider:\s*"anthropic"', re.DOTALL)
        return set(id_re.findall(models_src))

    def test_fallback_chain_is_ranked_registered_flagships(self):
        mod = _load_aside()
        registry = self._registry_ids()
        ranks = []
        for spec in mod._DEFAULT_ADVISOR_CHAIN:
            parsed = mod._parse_advisor_spec(spec)
            self.assertIsNotNone(parsed, f"unparsable fallback spec: {spec}")
            self.assertEqual(parsed["provider"], "anthropic", spec)
            rank = mod._rank_flagship(parsed["model"])
            self.assertIsNotNone(rank, f"fallback spec is not a rankable flagship: {spec}")
            self.assertIn(
                parsed["model"],
                registry,
                f"fallback spec {spec} is not in fir's bundled model registry",
            )
            ranks.append(rank)
        self.assertEqual(ranks, sorted(ranks, reverse=True), "fallback chain not strongest-first")

    def test_default_advisor_spec_heads_the_chain(self):
        mod = _load_aside()
        self.assertEqual(mod._DEFAULT_ADVISOR_CHAIN[0], mod._DEFAULT_ADVISOR_SPEC)


class DefaultDelegateFallbackIsSane(unittest.TestCase):
    """Guards aside.py's _DEFAULT_DELEGATE_SPEC — the LAST-RESORT fallback.

    Replaces DefaultDelegateTracksHighestAnthropicHaiku for the same reason as
    the advisor drift test above: the live highest Haiku from
    ctx.available_models() wins at runtime, so pinning the constant to the
    registry head only forced manual bumps. It must still be a real,
    registered, rankable Haiku.
    """

    _MODELS_GO = DefaultAdvisorFallbackIsSane._MODELS_GO

    def test_fallback_delegate_is_a_registered_haiku(self):
        import re

        mod = _load_aside()
        parsed = mod._parse_advisor_spec(mod._DEFAULT_DELEGATE_SPEC)
        self.assertIsNotNone(parsed)
        self.assertEqual(parsed["provider"], "anthropic")
        self.assertIsNotNone(
            mod._rank_haiku(parsed["model"]),
            f"fallback delegate is not a rankable Haiku: {mod._DEFAULT_DELEGATE_SPEC}",
        )
        with open(self._MODELS_GO, encoding="utf-8") as f:
            models_src = f.read()
        id_re = re.compile(r'ID:\s*"([^"]+)"(?:[^}]*?)Provider:\s*"anthropic"', re.DOTALL)
        self.assertIn(parsed["model"], set(id_re.findall(models_src)))


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

    def test_flagship_rejects_nonflagship(self):
        self.assertIsNone(self.mod._rank_flagship("claude-haiku-4-5"))
        self.assertIsNone(self.mod._rank_flagship("claude-sonnet-5"))
        self.assertIsNone(self.mod._rank_flagship("claude-3-opus"))

    def test_flagship_ranks_bare_major_only_id(self):
        # Regression: _OPUS_RE once required a minor, so the live bare
        # claude-opus-5 was unrankable and "strongest available" silently
        # stayed on opus-4-8 — the exact drift this feature exists to fix.
        self.assertIsNotNone(self.mod._rank_flagship("claude-opus-5"))
        self.assertGreater(
            self.mod._rank_flagship("claude-opus-5"),
            self.mod._rank_flagship("claude-opus-4-8"),
        )
        self.assertEqual(
            self.mod._best_anthropic_flagship(["claude-opus-4-8", "claude-opus-5"]),
            "claude-opus-5",
        )

    def test_date_stamped_ranks_below_its_bare_alias(self):
        self.assertGreater(
            self.mod._rank_flagship("claude-opus-4-1"),
            self.mod._rank_flagship("claude-opus-4-1-20250805"),
        )
        self.assertGreater(
            self.mod._rank_haiku("claude-haiku-4-5"),
            self.mod._rank_haiku("claude-haiku-4-5-20251001"),
        )

    def test_date_stamp_without_minor_parses_as_date(self):
        # claude-opus-4-20250514: the minor group must NOT eat a prefix of the
        # date stamp — lengths are disjoint (1-2 vs 8), so this is major=4,
        # minor=0, dated.
        self.assertEqual(self.mod._rank_flagship("claude-opus-4-20250514"), (0, 4, 0, 0))

    def test_dated_only_haiku_is_still_rankable(self):
        # Observed live: the sole available Haiku was the date-stamped id.
        # Rejecting it disabled delegation entirely.
        self.assertEqual(
            self.mod._best_anthropic_haiku(["claude-haiku-4-5-20251001"]),
            "claude-haiku-4-5-20251001",
        )

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


class TestEmptyResponseClassification(unittest.TestCase):
    """Provider-asserted failure vs. a live model that rendered nothing.

    These are two different classes with two different routing policies, and
    telling them apart is the whole point of this module's chain walk:

      * a provider that asserts ``stop_reason=error`` and supplies no
        diagnosis has FAILED the call — cool the model off and advance;
      * a response whose blocks merely render to nothing (redacted thinking,
        thinking-only, zero-length text, or no blocks at all) came from a
        LIVE model and is a transient blip — retry once, advance, never cool
        the model off.

    Before ``github.com/kfet/agent`` v0.1.3 the two were indistinguishable:
    an errored message with an empty ``ErrorMessage`` fell through to the
    degenerate-content arm of ``SimplePrompt`` and surfaced wearing the
    second class's wording. v0.1.3 intercepts it and synthesises its own
    text, so the ambiguity is gone — and this class pins that boundary.
    """

    # The verbatim error text github.com/kfet/agent >= v0.1.3 synthesises in
    # ensureErrorMessage() when a provider reports an error stop reason with
    # no message. Pinned in full ON PURPOSE: the classifier keys on a short
    # stable substring so upstream can reformat the parenthesised tail, but
    # this constant must break loudly if the WORDING drifts, because nothing
    # else in fir would notice until an advisor chain silently dead-ended
    # again.
    AGENT_V013_PROVIDER_ERROR = (
        "side-query: provider reported stop_reason=error with no error message "
        "(provider=anthropic model=claude-fable-5 blocks: [])"
    )

    def setUp(self):
        self.mod = _load_aside()

    def test_agent_v013_provider_error_text_is_unavailability(self):
        err = self.AGENT_V013_PROVIDER_ERROR
        self.assertTrue(self.mod._is_provider_error_without_message(err))
        self.assertTrue(self.mod._is_model_unavailable_error(err))
        # It quotes a block summary, but it is NOT the transient class — a
        # loose match here would retry a call upstream already called terminal.
        self.assertFalse(self.mod._is_empty_content_error(err))

    def test_agent_v013_text_matches_no_legacy_signature(self):
        # The rule has to be structural: the synthesised text deliberately
        # avoids every retryable-error pattern upstream knows about, and it
        # contains none of our own unavailability substrings either. Without
        # _is_provider_error_without_message it would fall to the unknown-error
        # branch and stop the walk instead of advancing it.
        low = self.AGENT_V013_PROVIDER_ERROR.lower()
        for sig in self.mod._MODEL_UNAVAILABLE_SIGNATURES:
            self.assertNotIn(sig, low, f"signature {sig!r} would mask the structural rule")
        # Nor may it be mistaken for a request-shaped fault, which would
        # surface it to the caller instead of advancing the chain.
        self.assertFalse(self.mod._is_request_shaped_error(self.AGENT_V013_PROVIDER_ERROR))

    def test_provider_error_with_blocks_is_still_unavailability(self):
        # A partial stream that then errors silently is the same provider
        # assertion; the block summary is incidental.
        err = (
            "side-query: provider reported stop_reason=error with no error message "
            "(provider=anthropic model=claude-fable-5 blocks: [text(len=12)])"
        )
        self.assertTrue(self.mod._is_model_unavailable_error(err))

    def test_noblocks_with_stop_reason_error_is_transient_not_unavailability(self):
        # Deliberate reversal of an earlier rule. Against agent < v0.1.3 this
        # exact string WAS a laundered hard failure; from v0.1.3 a hard
        # failure can no longer reach this wording, so what remains is a live
        # model that rendered nothing — the class fleet telemetry measured as
        # a transient drip that succeeds on re-probe.
        err = "side-query: response had no usable content (blocks: []) (stop_reason=error)"
        self.assertTrue(self.mod._is_empty_content_error(err))
        self.assertFalse(self.mod._is_model_unavailable_error(err))

    def test_noblocks_without_stop_reason_suffix_is_transient(self):
        # The ambiguous residue: a provider that asserts neither an error nor
        # a stop reason. It lands in the transient class by design, which
        # degrades gracefully (retry, advance, executor) instead of cooling
        # off a model we have no evidence against.
        for err in (
            "side-query: response had no usable content (blocks: [])",
            "side-query: response had no usable content (blocks: []) (stop_reason=)",
        ):
            with self.subTest(err=err):
                self.assertTrue(self.mod._is_empty_content_error(err))
                self.assertFalse(self.mod._is_model_unavailable_error(err))

    def test_redacted_thinking_is_not_unavailability(self):
        # A live model that emitted a redacted thinking block generated
        # something. Useless, but it is NOT evidence the model is gone.
        err = (
            "side-query: response had no usable content "
            "(blocks: [thinking(th=0,sig=940)]) (stop_reason=error)"
        )
        self.assertEqual(
            self.mod._classify_empty_blocks("thinking(th=0,sig=940)"), "empty:redacted"
        )
        self.assertTrue(self.mod._is_empty_content_error(err))
        self.assertFalse(self.mod._is_model_unavailable_error(err))

    def test_thinking_only_is_not_unavailability(self):
        err = (
            "side-query: response had no usable content "
            "(blocks: [thinking(th=12,sig=940)]) (stop_reason=error)"
        )
        self.assertFalse(self.mod._is_model_unavailable_error(err))

    def test_empty_text_block_is_not_unavailability(self):
        err = (
            "side-query: response had no usable content (blocks: [text(len=0)]) (stop_reason=error)"
        )
        self.assertFalse(self.mod._is_model_unavailable_error(err))

    def test_noblocks_on_user_abort_is_not_unavailability(self):
        # Ctrl-C must never cool a model off, and the request-shaped carve-out
        # must claim it before the empty-content retry can fire another call.
        err = "side-query: response had no usable content (blocks: []) (stop_reason=aborted)"
        self.assertFalse(self.mod._is_model_unavailable_error(err))
        self.assertTrue(self.mod._is_request_shaped_error(err))

    def test_noblocks_on_clean_stop_is_not_unavailability(self):
        err = "side-query: response had no usable content (blocks: []) (stop_reason=stop)"
        self.assertFalse(self.mod._is_model_unavailable_error(err))
        self.assertTrue(self.mod._is_empty_content_error(err))

    def test_unrelated_error_is_neither_class(self):
        self.assertFalse(self.mod._is_provider_error_without_message("connection reset by peer"))
        self.assertFalse(self.mod._is_empty_content_error("connection reset by peer"))


class TestRequestShapedError(unittest.TestCase):
    """Errors attributable to the REQUEST — never retried on another route."""

    def setUp(self):
        self.mod = _load_aside()

    def test_overflow_is_request_shaped(self):
        self.assertTrue(
            self.mod._is_request_shaped_error("side-query: Input exceeds context window limit")
        )

    def test_cancellation_is_request_shaped(self):
        for s in (
            "side-query: context canceled",
            "side-query: context cancelled",
            "response had no usable content (blocks: []) (stop_reason=aborted)",
        ):
            self.assertTrue(self.mod._is_request_shaped_error(s), s)

    def test_transport_error_is_not_request_shaped(self):
        self.assertFalse(self.mod._is_request_shaped_error("connection reset by peer"))

    def test_unavailability_is_not_request_shaped(self):
        self.assertFalse(self.mod._is_request_shaped_error("not_found_error: gone"))


class TestShortError(unittest.TestCase):
    def setUp(self):
        self.mod = _load_aside()

    def test_flattens_whitespace(self):
        self.assertEqual(self.mod._short_error("a\n  b\tc"), "a b c")

    def test_truncates_long_error(self):
        out = self.mod._short_error("x" * 500)
        self.assertLessEqual(len(out), self.mod._SHORT_ERROR_CHARS)
        self.assertTrue(out.endswith("…"))


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
        # No advisor TRACE prefix — nothing advisory answered. But escalation
        # was requested and did not happen, so the caller is told: silence
        # here would let the executor treat its own reply as advisor-endorsed.
        text = result["content"][0]["text"]
        self.assertEqual(
            text, "[advisor unavailable — answered on executor model]\n\nexecutor reply"
        )

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

    def test_route_shaped_error_falls_through_to_executor(self):
        # A route-shaped failure (transport blip) must NOT hard-fail the
        # caller: the executor answers, and the note names the actual fault
        # rather than claiming an unavailability we never established.
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            if model is not None:
                raise RuntimeError("connection reset by peer")
            return "executor answer"

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(
            text.startswith("[advisor failed (connection reset by peer) — "),
            text,
        )
        self.assertIn("answered on executor model]", text)
        self.assertIn("executor answer", text)
        self.assertEqual(ctx.side_query.call_count, 2)
        # NOT memoized — a transport blip is no evidence the model is dead.
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

    def test_route_shaped_error_and_executor_failure_chains_both(self):
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            if model is not None:
                raise RuntimeError("connection reset by peer")
            raise RuntimeError("executor socket hangup")

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("advisor chain failed", text)
        self.assertIn("connection reset by peer", text)
        self.assertIn("executor fallback also failed", text)
        self.assertIn("executor socket hangup", text)

    def test_user_abort_surfaces_immediately(self):
        # Cancelling a side query must not fire another LLM call at the
        # executor, and must not memoize the model as dead.
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})

        def sq(question, model=None, provider=None, effort=None):
            raise RuntimeError("side-query: context canceled")

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        self.assertIn("context canceled", result["content"][0]["text"])
        self.assertEqual(ctx.side_query.call_count, 1)
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

    def test_abort_wearing_empty_content_wording_is_not_retried(self):
        # An abort surfaces as `(blocks: []) (stop_reason=aborted)`, which
        # matches the empty-content pattern — so the same-candidate retry
        # would have fired one more LLM call (and slept) after the user hit
        # Ctrl-C. The request-shaped check has to win inside the retry loop,
        # not merely after it returns.
        mod = self._mod(advisor={"provider": "anthropic", "model": "claude-fable-5"})
        mod._EMPTY_CONTENT_RETRY_BACKOFF = 1.5

        def sq(question, model=None, provider=None, effort=None):
            raise RuntimeError(
                "side-query: response had no usable content (blocks: []) (stop_reason=aborted)"
            )

        ctx = self._ctx(sq)
        with mock.patch.object(mod.time, "sleep") as sleep:
            result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        self.assertIn("stop_reason=aborted", result["content"][0]["text"])
        # One probe only: no retry, no chain advance, no executor fallback.
        self.assertEqual(ctx.side_query.call_count, 1)
        sleep.assert_not_called()
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

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
    """Availability circuit breaker — a failed model is skipped for a backoff
    window (not the whole session) so Layer A degrades to the next live
    flagship instead of re-probing on every call, while an intermittent model
    recovers on its own."""

    def _mod(self, advisor=None, delegate=None):
        mod = _load_aside()
        mod._ADVISOR = advisor
        mod._DELEGATE = delegate
        return mod

    def test_fresh_load_starts_with_empty_memo(self):
        mod = self._mod()
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

    def test_cooldown_expires_and_model_is_probed_again(self):
        # The whole point of a cooldown over a tombstone: an intermittent
        # model comes back without restarting the session.
        mod = self._mod()
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))
        # Rewind the deadline rather than sleeping — no wall-clock waits.
        _, failures = mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"]
        mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"] = (mod.time.monotonic() - 1, failures)
        self.assertFalse(mod._model_unavailable("anthropic", "claude-fable-5"))

    def test_backoff_doubles_per_consecutive_failure_and_caps(self):
        mod = self._mod()
        windows = []
        for _ in range(12):
            before = mod.time.monotonic()
            mod._mark_model_unavailable("anthropic", "claude-fable-5")
            until, _ = mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"]
            windows.append(until - before)
        self.assertAlmostEqual(windows[0], mod._UNAVAILABLE_BASE_COOLDOWN, delta=1.0)
        self.assertAlmostEqual(windows[1], mod._UNAVAILABLE_BASE_COOLDOWN * 2, delta=1.0)
        self.assertAlmostEqual(windows[2], mod._UNAVAILABLE_BASE_COOLDOWN * 4, delta=1.0)
        # Capped, never unbounded.
        self.assertAlmostEqual(windows[-1], mod._UNAVAILABLE_MAX_COOLDOWN, delta=1.0)

    def test_success_resets_the_breaker(self):
        mod = self._mod()
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        mod._mark_model_available("anthropic", "claude-fable-5")
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})
        # Fresh short backoff, not a resumed ratchet toward the cap.
        before = mod.time.monotonic()
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        until, _ = mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"]
        self.assertAlmostEqual(until - before, mod._UNAVAILABLE_BASE_COOLDOWN, delta=1.0)

    def test_answering_candidate_closes_its_breaker(self):
        # An intermittent head that fails once then answers must not stay
        # marked — otherwise one blip costs the best advisor for the session.
        mod = self._mod(advisor=[{"provider": "anthropic", "model": "claude-fable-5"}])
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        _, failures = mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"]
        mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"] = (mod.time.monotonic() - 1, failures)

        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(return_value="fable answered")
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        self.assertIn("fable answered", result["content"][0]["text"])
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

    def test_mark_ignores_falsy(self):
        mod = self._mod()
        mod._mark_model_unavailable(None, "x")
        mod._mark_model_unavailable("anthropic", None)
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})
        mod._mark_model_available(None, "x")
        mod._mark_model_available("anthropic", None)
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))

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
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))

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
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))
        self.assertTrue(mod._model_unavailable("anthropic", "claude-opus-4-8"))

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

    def test_provider_error_head_advances_to_next_candidate(self):
        # THE BUG: an unavailable model's failure carries no 404/not_found
        # signature at all — the provider asserts an error and says nothing
        # else, and agent >= v0.1.3 synthesises this text for it. The walk
        # must advance rather than dead-ending on a chain head with two
        # healthy candidates sitting behind it.
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
                raise RuntimeError(_PROVIDER_ERR)
            return "opus answered"

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(
            text.startswith(
                "[advisor: anthropic/claude-opus-4-8 (fallback: claude-fable-5 unavailable)]"
            ),
            text,
        )
        self.assertIn("opus answered", text)
        # Advanced to candidate 2 — no executor fallback needed, and NO retry
        # on the dead head: upstream already called this class terminal.
        self.assertEqual(calls, ["claude-fable-5", "claude-opus-4-8"])
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))

    def test_whole_chain_provider_error_falls_to_executor_with_note(self):
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
                raise RuntimeError(_PROVIDER_ERR)
            return "executor answered"

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor unavailable — answered on executor model]"), text)
        self.assertIn("executor answered", text)
        self.assertEqual(calls, ["claude-fable-5", "claude-opus-4-8", None])
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))
        self.assertTrue(mod._model_unavailable("anthropic", "claude-opus-4-8"))

    def test_empty_redacted_never_cools_off_a_live_model(self):
        # A redacted-thinking response is a real generation from a LIVE
        # model. It is retried once on the same candidate, then the walk
        # advances — but nothing is ever cooled off, and the note says what
        # actually happened rather than asserting an unavailability that was
        # never established.
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        mod._EMPTY_CONTENT_RETRY_BACKOFF = 0
        available = [
            {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
            {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
        ]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model is not None:
                raise RuntimeError(
                    "side-query: response had no usable content "
                    "(blocks: [thinking(th=0,sig=940)]) (stop_reason=error)"
                )
            return "executor answered"

        ctx = self._ctx(sq, available=available)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(
            text.startswith("[advisor returned no usable content — answered on executor model]"),
            text,
        )
        self.assertIn("executor answered", text)
        # Each candidate probed twice (one retry), then the executor.
        self.assertEqual(
            calls,
            ["claude-fable-5", "claude-fable-5", "claude-opus-4-8", "claude-opus-4-8", None],
        )
        # Nothing cooled off: the models are alive.
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

    def test_cooled_off_chain_still_signals_that_escalation_did_not_happen(self):
        # Everything in the chain is cooling off, so resolution yields an
        # EMPTY chain and no candidate is even probed. The executor answers —
        # but it must NOT answer silently: the caller asked for advisor
        # judgement and would otherwise treat its own model's answer as
        # advisor-endorsed.
        mod = self._mod(
            advisor=[
                {"provider": "anthropic", "model": "claude-fable-5"},
                {"provider": "anthropic", "model": "claude-opus-4-8"},
            ]
        )
        mod._mark_model_unavailable("anthropic", "claude-fable-5")
        mod._mark_model_unavailable("anthropic", "claude-opus-4-8")
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            return "executor answered"

        # Availability unknown ([]) → _resolve_role_chain skips cooling models.
        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor unavailable — answered on executor model]"), text)
        self.assertIn("executor answered", text)
        # Executor only — no candidate was probed.
        self.assertEqual(calls, [None])

    def test_plain_aside_without_escalation_answers_silently(self):
        # The other side of the same condition: no role was requested, so
        # there is nothing to report and no note belongs on the answer.
        mod = self._mod()

        def sq(question, model=None, provider=None, effort=None):
            return "executor answered"

        ctx = self._ctx(sq)
        result = mod._run_aside([], "q", ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual(result["content"][0]["text"], "executor answered")

    def test_intermittent_head_never_hard_fails_and_recovers(self):
        # The head is INTERMITTENT, not dead: it fails one call and answers
        # the next. Two properties must hold — escalation never becomes a hard
        # error while it is down, and the head comes back on its own without
        # restarting the session.
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
        fable_up = [False]
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model == "claude-fable-5" and not fable_up[0]:
                raise RuntimeError(_PROVIDER_ERR)
            return f"{model or 'executor'} answered"

        ctx = self._ctx(sq, available=available)

        # Call 1: head is down → answered by opus, NOT an error.
        r1 = mod._run_aside([], "q1", ctx, escalate=True)
        self.assertFalse(r1["is_error"])
        self.assertIn("claude-opus-4-8 answered", r1["content"][0]["text"])

        # Call 2, still inside the cooldown: the head is not re-probed.
        r2 = mod._run_aside([], "q2", ctx, escalate=True)
        self.assertFalse(r2["is_error"])
        self.assertNotIn("claude-fable-5", calls[2:])

        # The head recovers and its cooldown lapses.
        fable_up[0] = True
        _, failures = mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"]
        mod._UNAVAILABLE_UNTIL["anthropic/claude-fable-5"] = (mod.time.monotonic() - 1, failures)

        # Call 3: the head is tried again and answers — no restart needed.
        r3 = mod._run_aside([], "q3", ctx, escalate=True)
        self.assertFalse(r3["is_error"])
        text = r3["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor: anthropic/claude-fable-5]"), text)
        self.assertIn("claude-fable-5 answered", text)
        # Breaker closed by the success.
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})
        # At no point did escalation return an error.
        self.assertEqual(calls[0], "claude-fable-5")

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


# ---------------------------------------------------------------------------
# Empty-content retry class (transient upstream blip)
# ---------------------------------------------------------------------------

_EMPTY_ERR = "side-query: response had no usable content (blocks: [thinking(th=0,sig=940)])"
_EMPTY_NOBLOCKS_ERR = "side-query: response had no usable content (blocks: [])"


class TestEmptyContentPredicate(unittest.TestCase):
    """_is_empty_content_error recognises the whole 'no usable content' family."""

    def setUp(self):
        self.mod = _load_aside()

    def test_recognises_thinking_only_variant(self):
        self.assertTrue(self.mod._is_empty_content_error(_EMPTY_ERR))

    def test_recognises_no_blocks_variant(self):
        self.assertTrue(self.mod._is_empty_content_error(_EMPTY_NOBLOCKS_ERR))

    def test_recognises_blocking_path_wording(self):
        self.assertTrue(self.mod._is_empty_content_error("advisor returned no content"))

    def test_rejects_unrelated_errors(self):
        for msg in (
            "",
            "not_found_error: model gone",
            "side-query: Input exceeds context window limit",
            "connection reset by peer",
        ):
            self.assertFalse(self.mod._is_empty_content_error(msg), msg)

    def test_empty_content_is_not_model_unavailable(self):
        # The two classes must not overlap — otherwise an empty response
        # would memoize a perfectly live model as dead.
        self.assertFalse(self.mod._is_model_unavailable_error(_EMPTY_ERR))

    def test_backoff_constant_is_positive_in_production(self):
        import re

        with open(os.path.join(_ext_dir, "aside.py"), encoding="utf-8") as f:
            src = f.read()
        m = re.search(r"^_EMPTY_CONTENT_RETRY_BACKOFF = ([0-9.]+)$", src, re.M)
        self.assertIsNotNone(m, "_EMPTY_CONTENT_RETRY_BACKOFF constant not found")
        assert m is not None
        self.assertGreater(float(m.group(1)), 0)


_RETRY_CHAIN = [
    {"provider": "anthropic", "model": "claude-fable-5"},
    {"provider": "anthropic", "model": "claude-opus-4-8"},
]
_RETRY_AVAILABLE = [
    {"provider": "anthropic", "id": "claude-fable-5", "name": "Fable"},
    {"provider": "anthropic", "id": "claude-opus-4-8", "name": "Opus"},
]


class TestEmptyContentRetry(unittest.TestCase):
    """Empty content is its OWN retryable class: retry once on the same
    candidate, then advance the chain WITHOUT memoizing, then fall back to
    the executor model."""

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

    def test_empty_then_success_retries_same_candidate(self):
        mod = self._mod(advisor=list(_RETRY_CHAIN))
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if len(calls) == 1:
                raise RuntimeError(_EMPTY_ERR)
            return "fable answered on retry"

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("fable answered on retry", text)
        # Same candidate twice — no chain advance, no executor fallback.
        self.assertEqual(calls, ["claude-fable-5", "claude-fable-5"])
        self.assertTrue(text.startswith("[advisor: anthropic/claude-fable-5]"), text)
        # Transient, so the model's breaker is never opened.
        self.assertFalse(mod._model_unavailable("anthropic", "claude-fable-5"))

    def test_no_blocks_variant_also_retried(self):
        mod = self._mod(advisor=list(_RETRY_CHAIN))
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if len(calls) == 1:
                raise RuntimeError(_EMPTY_NOBLOCKS_ERR)
            return "answered"

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        self.assertEqual(calls, ["claude-fable-5", "claude-fable-5"])

    def test_empty_twice_advances_to_next_candidate(self):
        mod = self._mod(advisor=list(_RETRY_CHAIN))
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model == "claude-fable-5":
                raise RuntimeError(_EMPTY_ERR)
            return "opus answered"

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("opus answered", text)
        # fable probed twice (initial + retry), then the next candidate.
        self.assertEqual(calls, ["claude-fable-5", "claude-fable-5", "claude-opus-4-8"])
        self.assertTrue(
            text.startswith(
                "[advisor: anthropic/claude-opus-4-8 (fallback: claude-fable-5 unavailable)]"
            ),
            text,
        )

    def test_empty_content_never_memoizes_the_model(self):
        # The whole point of the separate class: a blip must not permanently
        # degrade the chain for the rest of the session.
        mod = self._mod(advisor=list(_RETRY_CHAIN))

        def sq(question, model=None, provider=None, effort=None):
            if model is None:
                return "executor answered"
            raise RuntimeError(_EMPTY_ERR)

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        self.assertEqual(mod._UNAVAILABLE_UNTIL, {})

    def test_chain_all_empty_falls_through_to_executor(self):
        mod = self._mod(advisor=list(_RETRY_CHAIN))
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model is None:
                return "executor answered"
            raise RuntimeError(_EMPTY_ERR)

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("executor answered", text)
        self.assertTrue(
            text.startswith("[advisor returned no usable content — answered on executor model]"),
            text,
        )
        # 2 probes per candidate, then one executor call.
        self.assertEqual(
            calls,
            [
                "claude-fable-5",
                "claude-fable-5",
                "claude-opus-4-8",
                "claude-opus-4-8",
                None,
            ],
        )

    def test_executor_fallback_also_retried_once_on_empty(self):
        mod = self._mod(advisor=[{"provider": "anthropic", "model": "claude-fable-5"}])
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if model is None and calls.count(None) == 2:
                return "executor answered on retry"
            raise RuntimeError(_EMPTY_ERR)

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        self.assertIn("executor answered on retry", result["content"][0]["text"])
        self.assertEqual(calls, ["claude-fable-5", "claude-fable-5", None, None])

    def test_everything_empty_surfaces_chained_error(self):
        mod = self._mod(advisor=list(_RETRY_CHAIN))

        def sq(question, model=None, provider=None, effort=None):
            raise RuntimeError(_EMPTY_ERR)

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        text = result["content"][0]["text"]
        self.assertIn("chain exhausted", text)
        self.assertIn("claude-fable-5", text)
        self.assertIn("claude-opus-4-8", text)
        self.assertIn("executor fallback also failed", text)
        self.assertIn("no usable content", text)

    def test_mixed_dead_and_empty_notes_unavailable(self):
        # A 404 anywhere in the walk means the note keeps the "unavailable"
        # wording; the 404'd model is cooled off, the empty one is not.
        mod = self._mod(advisor=list(_RETRY_CHAIN))

        def sq(question, model=None, provider=None, effort=None):
            if model == "claude-fable-5":
                raise RuntimeError("not_found_error: fable gone")
            if model == "claude-opus-4-8":
                raise RuntimeError(_EMPTY_ERR)
            return "executor answered"

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        text = result["content"][0]["text"]
        self.assertTrue(text.startswith("[advisor unavailable — answered on executor model]"), text)
        self.assertTrue(mod._model_unavailable("anthropic", "claude-fable-5"))
        self.assertFalse(mod._model_unavailable("anthropic", "claude-opus-4-8"))

    def test_overflow_after_empty_is_surfaced_not_advanced(self):
        # Retry is scoped to empty content only: a hard error on the retry
        # surfaces immediately rather than walking the chain.
        mod = self._mod(advisor=list(_RETRY_CHAIN))
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if len(calls) == 1:
                raise RuntimeError(_EMPTY_ERR)
            raise RuntimeError("side-query: Input exceeds context window limit")

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertTrue(result["is_error"])
        self.assertIn("context window full", result["content"][0]["text"])
        self.assertEqual(calls, ["claude-fable-5", "claude-fable-5"])

    def test_retry_and_advance_are_visible_on_a_card(self):
        mod = self._mod(advisor=list(_RETRY_CHAIN))

        def sq(question, model=None, provider=None, effort=None):
            if model == "claude-fable-5":
                raise RuntimeError(_EMPTY_ERR)
            return "opus answered"

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        mod._run_aside([], "q", ctx, escalate=True)
        slugs = [c.kwargs.get("slug") for c in ctx.put_observable.call_args_list]
        self.assertIn("retry:empty", slugs)
        self.assertIn("advance:empty", slugs)

    def test_backoff_is_slept_between_attempts(self):
        mod = self._mod(advisor=[{"provider": "anthropic", "model": "claude-fable-5"}])
        mod._EMPTY_CONTENT_RETRY_BACKOFF = 1.5
        calls = []

        def sq(question, model=None, provider=None, effort=None):
            calls.append(model)
            if len(calls) == 1:
                raise RuntimeError(_EMPTY_ERR)
            return "answered"

        ctx = self._ctx(sq, available=_RETRY_AVAILABLE)
        with mock.patch.object(mod.time, "sleep") as sleep:
            mod._run_aside([], "q", ctx, escalate=True)
        sleep.assert_called_once_with(1.5)


# ---------------------------------------------------------------------------
# Dynamic default advisor/delegate resolution from available_models()
# ---------------------------------------------------------------------------


class TestDynamicDefaultChain(unittest.TestCase):
    """The DEFAULT chain is resolved from the live model registry, so a new
    flagship is picked up with zero code changes. Explicit aside.json values
    still win."""

    def _ctx(self, available, result="reply"):
        ctx = _blocking_ctx()
        ctx.side_query = mock.MagicMock(return_value=result)
        if available is None:
            del ctx.available_models
        else:
            ctx.available_models = mock.MagicMock(return_value=available)
        return ctx

    def _default_mod(self):
        """Module whose advisor/delegate came from the bundled default."""
        return self._mod_with_config(None)

    def _mod_with_config(self, cfg):
        """Module whose advisor/delegate resolve from *cfg*.

        The config is re-read on every access, so the patch must stay mounted
        for the whole test rather than just long enough to warm a cache — it
        is registered as a cleanup, not scoped to a ``with`` block.
        """
        mod = _load_aside()
        mod._ADVISOR = mod._ADVISOR_UNSET
        mod._DELEGATE = mod._ADVISOR_UNSET
        patcher = mock.patch.object(mod, "_read_existing_config", return_value=cfg)
        patcher.start()
        self.addCleanup(patcher.stop)
        return mod

    def test_ranks_live_flagships_strongest_first(self):
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": "claude-opus-4-8"},
            {"provider": "anthropic", "id": "claude-opus-5-0"},
            {"provider": "anthropic", "id": "claude-fable-6"},
            {"provider": "anthropic", "id": "claude-haiku-4-5"},
            {"provider": "openai", "id": "gpt-9"},
        ]
        chain = mod._dynamic_default_chain(self._ctx(available), "advisor")
        self.assertEqual(
            [c["model"] for c in chain],
            ["claude-fable-6", "claude-opus-5-0", "claude-opus-4-8"],
        )

    def test_chain_is_capped(self):
        mod = self._default_mod()
        available = [{"provider": "anthropic", "id": f"claude-opus-{n}-0"} for n in range(1, 9)]
        chain = mod._dynamic_default_chain(self._ctx(available), "advisor")
        self.assertEqual(len(chain), mod._DYNAMIC_ADVISOR_CHAIN_LEN)

    def test_date_stamped_duplicate_is_deduped_keeping_bare(self):
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": "claude-opus-4-8-20250805"},
            {"provider": "anthropic", "id": "claude-opus-4-8"},
        ]
        chain = mod._dynamic_default_chain(self._ctx(available), "advisor")
        self.assertEqual([c["model"] for c in chain], ["claude-opus-4-8"])

    def test_date_stamped_kept_when_it_is_the_only_spelling(self):
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": "claude-opus-4-1-20250805"},
            {"provider": "anthropic", "id": "claude-opus-4-8"},
        ]
        chain = mod._dynamic_default_chain(self._ctx(available), "advisor")
        # Used VERBATIM — never normalised to a bare alias the API may reject.
        self.assertEqual(
            [c["model"] for c in chain], ["claude-opus-4-8", "claude-opus-4-1-20250805"]
        )

    def test_delegate_resolves_dated_haiku_when_only_spelling(self):
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": "claude-haiku-4-5-20251001"},
            {"provider": "anthropic", "id": "claude-opus-5"},
        ]
        chain = mod._dynamic_default_chain(self._ctx(available), "delegate")
        self.assertEqual([c["model"] for c in chain], ["claude-haiku-4-5-20251001"])

    def test_real_live_host_model_set(self):
        # Verbatim available_models() from a live host on 2026-08-11 — the set
        # that exposed both ranking bugs (bare claude-opus-5 dropped; only the
        # dated Haiku live, so the delegate resolved to None).
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": i}
            for i in (
                "claude-fable-5",
                "claude-haiku-4-5-20251001",
                "claude-opus-4-5-20251101",
                "claude-opus-4-6",
                "claude-opus-4-7",
                "claude-opus-4-8",
                "claude-opus-5",
                "claude-sonnet-4-5-20250929",
                "claude-sonnet-4-6",
                "claude-sonnet-5",
            )
        ]
        ctx = self._ctx(available)
        self.assertEqual(
            [c["model"] for c in mod._dynamic_default_chain(ctx, "advisor")],
            ["claude-fable-5", "claude-opus-5", "claude-opus-4-8"],
        )
        self.assertEqual(
            [c["model"] for c in mod._dynamic_default_chain(ctx, "delegate")],
            ["claude-haiku-4-5-20251001"],
        )

    def test_delegate_resolves_highest_live_haiku(self):
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": "claude-haiku-4-5"},
            {"provider": "anthropic", "id": "claude-haiku-5-1"},
            {"provider": "anthropic", "id": "claude-opus-5-0"},
        ]
        chain = mod._dynamic_default_chain(self._ctx(available), "delegate")
        self.assertEqual([c["model"] for c in chain], ["claude-haiku-5-1"])

    def test_degrades_to_static_chain_when_registry_empty(self):
        mod = self._default_mod()
        self.assertIsNone(mod._dynamic_default_chain(self._ctx([]), "advisor"))
        self.assertIsNone(mod._dynamic_default_chain(self._ctx([]), "delegate"))

    def test_degrades_to_static_chain_when_no_anthropic_entries(self):
        mod = self._default_mod()
        available = [{"provider": "openai", "id": "gpt-9"}]
        self.assertIsNone(mod._dynamic_default_chain(self._ctx(available), "advisor"))

    def test_degrades_to_static_chain_when_registry_raises(self):
        mod = self._default_mod()
        ctx = _blocking_ctx()
        ctx.available_models = mock.MagicMock(side_effect=RuntimeError("bridge down"))
        self.assertIsNone(mod._dynamic_default_chain(ctx, "advisor"))

    def test_old_host_without_verb_uses_static_chain(self):
        mod = self._default_mod()
        ctx = self._ctx(None)
        self.assertIsNone(mod._dynamic_default_chain(ctx, "advisor"))
        self.assertEqual(
            [c["model"] for c in mod._resolve_advisor_chain(ctx)],
            ["claude-fable-5", "claude-opus-4-8", "claude-opus-4-7"],
        )

    def test_default_routes_to_live_flagship_not_stale_constant(self):
        # The bug this fixes: hosts run claude-opus-5 while the baked chain
        # tail is opus-4-8 — escalation must NOT route below the live head.
        mod = self._default_mod()
        available = [
            {"provider": "anthropic", "id": "claude-opus-5-0"},
            {"provider": "anthropic", "id": "claude-opus-4-8"},
        ]
        ctx = self._ctx(available, result="opus5 answered")
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        self.assertEqual(ctx.side_query.call_args.kwargs["model"], "claude-opus-5-0")
        self.assertTrue(
            result["content"][0]["text"].startswith("[advisor: anthropic/claude-opus-5-0]"),
            result["content"][0]["text"],
        )

    def test_explicit_config_wins_over_dynamic(self):
        mod = self._mod_with_config({"advisor": "anthropic/claude-opus-4-7"})
        available = [
            {"provider": "anthropic", "id": "claude-opus-5-0"},
            {"provider": "anthropic", "id": "claude-opus-4-7"},
        ]
        ctx = self._ctx(available)
        self.assertEqual([c["model"] for c in mod._resolve_advisor_chain(ctx)], ["claude-opus-4-7"])

    def test_explicit_off_still_disables_the_role(self):
        mod = self._mod_with_config({"advisor": "off", "delegate": "none"})
        self.assertIsNone(mod._advisor())
        self.assertIsNone(mod._delegate())
        available = [{"provider": "anthropic", "id": "claude-opus-5-0"}]
        ctx = self._ctx(available, result="executor answered")
        result = mod._run_aside([], "q", ctx, escalate=True)
        self.assertFalse(result["is_error"])
        # No advisor → executor model, no dynamic substitution.
        self.assertIsNone(ctx.side_query.call_args.kwargs["model"])

    def test_explicit_chain_wins_over_dynamic(self):
        mod = self._mod_with_config(
            {"advisor": ["anthropic/claude-opus-4-7", "anthropic/claude-opus-4-8"]}
        )
        available = [
            {"provider": "anthropic", "id": "claude-opus-5-0"},
            {"provider": "anthropic", "id": "claude-opus-4-7"},
            {"provider": "anthropic", "id": "claude-opus-4-8"},
        ]
        self.assertEqual(
            [c["model"] for c in mod._resolve_advisor_chain(self._ctx(available))],
            ["claude-opus-4-7", "claude-opus-4-8"],
        )
