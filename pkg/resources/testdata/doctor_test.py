"""Tests for the doctor extension."""

import json
import os
import sys
import tempfile
import time
import unittest
from unittest.mock import MagicMock

# Patch config dir before importing doctor
_tmpdir = tempfile.mkdtemp()
os.environ["FIR_CONFIG_DIR"] = _tmpdir

# Add SDK and builtin_extensions to path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "extension", "sdk", "python"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "builtin_extensions"))

from unittest import mock as _mock  # noqa: E402

import fir_ext  # noqa: E402

with _mock.patch.object(fir_ext, "run"):
    import doctor  # noqa: E402


class TestDoctor(unittest.TestCase):
    def setUp(self):
        """Reset state between tests."""
        doctor._session.clear()
        doctor._tool_errors.clear()
        doctor._session_end_fired = False
        doctor.DOCTOR_LOG.unlink(missing_ok=True)
        self.ctx = MagicMock(spec=doctor.fir_ext.Context)

    def test_session_with_tool_errors_records_failure(self):
        # Simulate session lifecycle
        doctor.on_session_start({"session_id": "s1", "cwd": "/tmp"}, self.ctx)
        doctor.on_tool_execution_end({
            "tool_name": "bash",
            "tool_call_id": "tc1",
            "is_error": True,
            "error_text": "command not found: foo",
        }, self.ctx)
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
        doctor._append_record({
            "type": "session_failure", "session_id": "s1",
            "tool_errors": [{"tool": "bash", "error_text": "fail"}],
        })
        doctor._append_record({
            "type": "session_failure", "session_id": "s2",
            "tool_errors": [{"tool": "write", "error_text": "perm denied"}],
        })

        result = json.loads(doctor.doctor_query({"tool_name": "bash"}, self.ctx))
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["session_id"], "s1")

    def test_doctor_query_filters_by_pattern(self):
        doctor._append_record({
            "type": "session_failure", "session_id": "s1",
            "tool_errors": [{"tool": "bash", "error_text": "connection refused"}],
        })
        doctor._append_record({
            "type": "session_failure", "session_id": "s2",
            "tool_errors": [{"tool": "bash", "error_text": "file not found"}],
        })

        result = json.loads(doctor.doctor_query({"pattern": "connection"}, self.ctx))
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["session_id"], "s1")

    def test_doctor_summary_empty(self):
        result = doctor.doctor_summary({}, self.ctx)
        self.assertIn("No failures", result)

    def test_doctor_summary_with_data(self):
        doctor._append_record({
            "type": "session_failure", "session_id": "s1",
            "end_time": time.time(),
            "tool_errors": [
                {"tool": "bash", "error_text": "fail"},
                {"tool": "bash", "error_text": "fail"},
                {"tool": "write", "error_text": "perm"},
            ],
        })

        result = doctor.doctor_summary({}, self.ctx)
        self.assertIn("Total failed sessions: 1", result)
        self.assertIn("bash: 2", result)

    def test_shutdown_fallback_records_when_no_session_end(self):
        doctor.on_session_start({"session_id": "s_old", "cwd": "/tmp"}, self.ctx)
        doctor.on_tool_execution_end({
            "tool_name": "bash", "tool_call_id": "tc1",
            "is_error": True, "error_text": "boom",
        }, self.ctx)
        # session_end never fires; only shutdown
        doctor.on_session_shutdown({}, self.ctx)

        records = doctor._read_records()
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["exit_reason"], "shutdown")


if __name__ == "__main__":
    unittest.main()
