"""Tests for the doctor extension."""

import json
import os
import sys
import tempfile
import time
import unittest
from unittest.mock import MagicMock

# Patch config dirs before importing doctor
_tmpdir = tempfile.mkdtemp()

# Add SDK and builtin_extensions to path
_sdk_path = os.path.join(
    os.path.dirname(__file__),
    "..",
    "..",
    "extension",
    "sdk",
    "python",
)
sys.path.insert(0, _sdk_path)
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "builtin_extensions"))

from unittest import mock as _mock

import fir_ext

# Seed config_dirs so doctor uses our tmpdir as the global log location.
fir_ext.config_dirs = [_tmpdir]

with _mock.patch.object(fir_ext, "run"):
    import doctor


class TestDoctor(unittest.TestCase):
    def setUp(self):
        """Reset state between tests."""
        doctor._session.clear()
        doctor._tool_errors.clear()
        doctor._session_end_fired = False
        doctor._doctor_log().unlink(missing_ok=True)
        self.ctx = MagicMock(spec=doctor.fir_ext.Context)
        # Default: core reports a healthy configuration.
        self.ctx.agent_info.return_value = {}

    def test_session_with_tool_errors_records_failure(self):
        # Simulate session lifecycle
        doctor.on_session_start({"session_id": "s1", "cwd": "/tmp"}, self.ctx)
        doctor.on_tool_execution_end(
            {
                "tool_name": "bash",
                "tool_call_id": "tc1",
                "is_error": True,
                "error_text": "command not found: foo",
            },
            self.ctx,
        )
        doctor.on_session_end({"reason": "normal"}, self.ctx)

        records = doctor._read_records()
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["session_id"], "s1")
        self.assertEqual(records[0]["tool_error_count"], 1)
        self.assertEqual(records[0]["tool_errors"][0]["tool"], "bash")

    def test_clean_session_not_recorded(self):
        doctor.on_session_start({"session_id": "s2", "cwd": "/tmp"}, self.ctx)
        doctor.on_session_end({"reason": "normal"}, self.ctx)

        records = doctor._read_records()
        self.assertEqual(len(records), 0)

    def test_error_exit_recorded_even_without_tool_errors(self):
        doctor.on_session_start({"session_id": "s3", "cwd": "/tmp"}, self.ctx)
        doctor.on_session_end({"reason": "error", "error": "panic: oh no"}, self.ctx)

        records = doctor._read_records()
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["exit_error"], "panic: oh no")

    def test_doctor_query_filters_by_tool(self):
        # Write two records
        doctor._append_record(
            {
                "type": "session_failure",
                "session_id": "s1",
                "tool_errors": [{"tool": "bash", "error_text": "fail"}],
            }
        )
        doctor._append_record(
            {
                "type": "session_failure",
                "session_id": "s2",
                "tool_errors": [{"tool": "write", "error_text": "perm denied"}],
            }
        )

        result = json.loads(doctor.doctor_query({"tool_name": "bash"}, self.ctx))
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["session_id"], "s1")

    def test_doctor_query_filters_by_pattern(self):
        doctor._append_record(
            {
                "type": "session_failure",
                "session_id": "s1",
                "tool_errors": [{"tool": "bash", "error_text": "connection refused"}],
            }
        )
        doctor._append_record(
            {
                "type": "session_failure",
                "session_id": "s2",
                "tool_errors": [{"tool": "bash", "error_text": "file not found"}],
            }
        )

        result = json.loads(doctor.doctor_query({"pattern": "connection"}, self.ctx))
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["session_id"], "s1")

    def test_doctor_summary_empty(self):
        result = doctor.doctor_summary({}, self.ctx)
        self.assertIn("No failures", result)

    def test_doctor_summary_with_data(self):
        doctor._append_record(
            {
                "type": "session_failure",
                "session_id": "s1",
                "end_time": time.time(),
                "tool_errors": [
                    {"tool": "bash", "error_text": "fail"},
                    {"tool": "bash", "error_text": "fail"},
                    {"tool": "write", "error_text": "perm"},
                ],
            }
        )

        result = doctor.doctor_summary({}, self.ctx)
        self.assertIn("Total failed sessions: 1", result)
        self.assertIn("bash: 2", result)

    def test_shutdown_fallback_records_when_no_session_end(self):
        doctor.on_session_start({"session_id": "s_old", "cwd": "/tmp"}, self.ctx)
        doctor.on_tool_execution_end(
            {
                "tool_name": "bash",
                "tool_call_id": "tc1",
                "is_error": True,
                "error_text": "boom",
            },
            self.ctx,
        )
        # session_end never fires; only shutdown
        doctor.on_session_shutdown({}, self.ctx)

        records = doctor._read_records()
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["exit_reason"], "shutdown")

    # --- configuration diagnostics (from core, via agent.info) ---

    def _with_diagnostics(self, diagnostics):
        self.ctx.agent_info.return_value = {"diagnostics": diagnostics}

    def test_summary_includes_configuration_diagnostics(self):
        self._with_diagnostics(
            [
                {
                    "code": "stale_default_model",
                    "severity": "warning",
                    "summary": "defaultModel is pinned to anthropic/claude-opus-4-6, "
                    "shadowing the newer anthropic/claude-opus-5",
                    "remediation": 'Edit /cfg/settings.json: set "defaultModel": "claude-opus-5"',
                    "file": "/cfg/settings.json",
                }
            ]
        )
        result = doctor.doctor_summary({}, self.ctx)
        self.assertIn("[WARNING]", result)
        self.assertIn("claude-opus-4-6", result)
        self.assertIn("/cfg/settings.json", result)
        # Still reports the (empty) failure history rather than hiding it.
        self.assertIn("No failures", result)

    def test_summary_silent_when_no_diagnostics(self):
        self._with_diagnostics([])
        result = doctor.doctor_summary({}, self.ctx)
        self.assertNotIn("Configuration:", result)

    def test_diagnostics_unavailable_is_not_an_error(self):
        self.ctx.agent_info.side_effect = RuntimeError("no session")
        self.assertEqual(doctor._diagnostics_text(self.ctx), "")
        self.assertIn("No failures", doctor.doctor_summary({}, self.ctx))

    def test_command_renders_diagnostics(self):
        self._with_diagnostics(
            [{"severity": "warning", "summary": "pinned", "remediation": "edit it"}]
        )
        self.assertIn("edit it", doctor.cmd_doctor([], self.ctx)["message"])


if __name__ == "__main__":
    unittest.main()
