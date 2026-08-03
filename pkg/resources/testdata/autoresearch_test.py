"""End-to-end tests for the autoresearch extension.

These use real git repos and real `git worktree` sub-worktrees on disk — the
benchmark lock exists precisely to survive that layout, so mocking git out
would test nothing.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock as _mock
from unittest.mock import MagicMock

_sdk_path = os.path.join(os.path.dirname(__file__), "..", "..", "extension", "sdk", "python")
sys.path.insert(0, _sdk_path)
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "builtin_extensions"))

import fir_ext

with _mock.patch.object(fir_ext, "run"):
    import autoresearch

BENCH = """#!/usr/bin/env bash
echo "METRIC score=42"
"""

TAMPERED_BENCH = """#!/usr/bin/env bash
echo "METRIC score=999999"
"""


_GIT = shutil.which("git") or "/usr/bin/git"


def _git(cwd, *args):
    subprocess.run([_GIT, *args], cwd=cwd, check=True, capture_output=True, text=True)


def _payload(result):
    """Decode a tool result's JSON text content."""
    return json.loads(result["content"][0]["text"])


class TestAutoresearchLock(unittest.TestCase):
    def setUp(self):
        self.ctx = MagicMock(spec=fir_ext.Context)
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)

        self.repo = os.path.join(self.tmp, "repo")
        os.makedirs(self.repo)
        _git(self.repo, "init", "-q", "-b", "main")
        _git(self.repo, "config", "user.email", "t@t")
        _git(self.repo, "config", "user.name", "t")
        self.bench = Path(self.repo) / "autoresearch_bench.sh"
        self.bench.write_text(BENCH)
        self.bench.chmod(0o755)
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-qm", "init")

    def _worktree(self, name, bench_text=None):
        """Create a sub-worktree, optionally rewriting its benchmark."""
        path = os.path.join(self.tmp, name)
        _git(self.repo, "worktree", "add", "-q", "-b", name, path)
        if bench_text is not None:
            (Path(path) / "autoresearch_bench.sh").write_text(bench_text)
            _git(path, "commit", "-aqm", "tamper")
        return path

    # 2. run_experiment works with no lock (back-compat)
    def test_runs_unlocked_and_notes_it(self):
        out = _payload(autoresearch.run_experiment({"cwd": self.repo}, self.ctx))
        self.assertTrue(out["success"])
        self.assertEqual(out["metrics"], {"score": 42.0})
        self.assertIn("wall_ms", out)
        self.assertIn("No benchmark lock set", out["lock_note"])

    # 3. lock_benchmark writes the lock
    def test_lock_benchmark_writes_lock(self):
        out = _payload(autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx))
        lock = json.loads(Path(out["lock_path"]).read_text())
        self.assertEqual(Path(out["lock_path"]).parent, Path(self.repo))
        self.assertEqual(lock["sha256"], out["sha256"])
        self.assertEqual(lock["path"], str(self.bench))
        self.assertTrue(lock["timestamp"])
        self.assertTrue(lock["commit"])

    def test_lock_benchmark_errors_without_script(self):
        empty = os.path.join(self.tmp, "empty")
        os.makedirs(empty)
        with self.assertRaises(fir_ext.ToolError) as cm:
            autoresearch.lock_benchmark({"cwd": empty}, self.ctx)
        self.assertIn("No autoresearch_bench.sh", str(cm.exception))

    # 4. sub-worktree that edits the bench script → refused
    def test_tampered_subworktree_is_refused(self):
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        wt = self._worktree("exp-tampered", TAMPERED_BENCH)

        result = autoresearch.run_experiment({"cwd": wt}, self.ctx)
        self.assertTrue(result["is_error"])
        out = _payload(result)
        self.assertFalse(out["success"])
        self.assertEqual(out["metrics"], {})
        self.assertIn("Benchmark modified since lock", out["error"])
        self.assertNotEqual(out["locked_sha256"], out["current_sha256"])

    # An experiment must not be able to authorise itself with its own lock file.
    def test_selfsigned_lock_in_subworktree_is_refused_as_ambiguous(self):
        """A forged in-worktree lock must never authorise its own benchmark."""
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        wt = self._worktree("exp-selfsigned", TAMPERED_BENCH)
        autoresearch.lock_benchmark({"cwd": wt}, self.ctx)  # forged lock, in-worktree

        with self.assertRaises(fir_ext.ToolError) as cm:
            autoresearch.run_experiment({"cwd": wt}, self.ctx)
        self.assertIn("Ambiguous benchmark lock", str(cm.exception))
        self.assertIn("2 distinct locks", str(cm.exception))

    def test_campaign_root_ignores_stranger_lock_in_sibling_worktree(self):
        """A stale/other-campaign lock elsewhere in the repo must not be silently used."""
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        stranger = self._worktree("other-campaign")
        (Path(stranger) / "autoresearch_bench.sh").write_text(TAMPERED_BENCH)
        autoresearch.lock_benchmark({"cwd": stranger}, self.ctx)

        # Re-running the baseline from the campaign root is legitimate: it must
        # not be refused with a bogus "benchmark modified" against the stranger.
        with self.assertRaises(fir_ext.ToolError) as cm:
            autoresearch.run_experiment({"cwd": self.repo}, self.ctx)
        self.assertIn("Ambiguous benchmark lock", str(cm.exception))
        self.assertNotIn("Benchmark modified", str(cm.exception))

        # …and the documented escape hatch makes the legitimate run succeed.
        out = _payload(
            autoresearch.run_experiment(
                {"cwd": self.repo, "lock_path": "autoresearch.lock"}, self.ctx
            )
        )
        self.assertTrue(out["success"])

    def test_only_lock_in_repo_is_used_from_campaign_root(self):
        """The single-lock case: a baseline re-run from the campaign root just works."""
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        self._worktree("exp-untouched")  # sibling worktree, no lock of its own

        out = _payload(autoresearch.run_experiment({"cwd": self.repo}, self.ctx))
        self.assertTrue(out["success"])
        self.assertNotIn("lock_note", out)

    def test_committed_lock_inherited_by_subworktree_is_one_logical_lock(self):
        """The campaign commits its lock; sub-worktrees inherit an identical copy."""
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        _git(self.repo, "add", "-A")
        _git(self.repo, "commit", "-qm", "commit the lock")

        clean = self._worktree("exp-inherited")
        self.assertTrue((Path(clean) / "autoresearch.lock").exists())
        out = _payload(autoresearch.run_experiment({"cwd": clean}, self.ctx))
        self.assertTrue(out["success"])
        self.assertEqual(out["metrics"], {"score": 42.0})

        # Same inherited lock, but this experiment rewrote the benchmark: the
        # refusal must be the precise one, not the ambiguity fallback.
        tampered = self._worktree("exp-inherited-tampered", TAMPERED_BENCH)
        result = autoresearch.run_experiment({"cwd": tampered}, self.ctx)
        self.assertTrue(result["is_error"])
        self.assertIn("Benchmark modified since lock", _payload(result)["error"])

    def test_deleted_benchmark_under_lock_is_refused(self):
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        wt = self._worktree("exp-deleted")
        (Path(wt) / "autoresearch_bench.sh").unlink()

        with self.assertRaises(fir_ext.ToolError) as cm:
            autoresearch.run_experiment({"cwd": wt}, self.ctx)
        self.assertIn("deleted or renamed", str(cm.exception))

    def test_unreadable_lock_is_refused(self):
        out = _payload(autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx))
        Path(out["lock_path"]).write_text("not json")
        with self.assertRaises(fir_ext.ToolError) as cm:
            autoresearch.run_experiment({"cwd": self.repo}, self.ctx)
        self.assertIn("unreadable", str(cm.exception))

    def test_lock_without_sha256_is_refused(self):
        out = _payload(autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx))
        Path(out["lock_path"]).write_text(json.dumps({"path": str(self.bench)}))
        with self.assertRaises(fir_ext.ToolError) as cm:
            autoresearch.run_experiment({"cwd": self.repo}, self.ctx)
        self.assertIn("malformed", str(cm.exception))

    # 5. honest sub-worktree runs fine and reports wall_ms
    def test_untouched_subworktree_runs(self):
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        wt = self._worktree("exp-clean")

        out = _payload(autoresearch.run_experiment({"cwd": wt}, self.ctx))
        self.assertTrue(out["success"])
        self.assertEqual(out["metrics"], {"score": 42.0})
        self.assertIsInstance(out["wall_ms"], float)
        self.assertNotIn("lock_note", out)

    def test_explicit_lock_path_override(self):
        out = _payload(autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx))
        wt = self._worktree("exp-override", TAMPERED_BENCH)
        result = autoresearch.run_experiment({"cwd": wt, "lock_path": out["lock_path"]}, self.ctx)
        self.assertTrue(result["is_error"])

    def test_relative_lock_path_resolves_against_experiment_cwd(self):
        autoresearch.lock_benchmark({"cwd": self.repo}, self.ctx)
        wt = self._worktree("exp-relative")
        shutil.copy(Path(self.repo) / "autoresearch.lock", Path(wt) / "campaign.lock")

        # Relative to the experiment's cwd, not to fir's process cwd.
        out = _payload(
            autoresearch.run_experiment({"cwd": wt, "lock_path": "campaign.lock"}, self.ctx)
        )
        self.assertTrue(out["success"])
        self.assertNotIn("lock_note", out)

    # 6. efficiency stats: wall_ms persisted, /autoresearch reports them
    def test_wall_ms_logged_and_summarised(self):
        run = _payload(autoresearch.run_experiment({"cwd": self.repo}, self.ctx))
        autoresearch.log_experiment(
            {
                "cwd": self.repo,
                "description": "baseline",
                "metrics": run["metrics"],
                "primary_metric": "score",
                "status": "baseline",
                "wall_ms": run["wall_ms"],
            },
            self.ctx,
        )
        autoresearch.log_experiment(
            {
                "cwd": self.repo,
                "description": "a win",
                "metrics": {"score": 50.0},
                "primary_metric": "score",
                "baseline_value": 42.0,
                "status": "keep",
                "wall_ms": 2000.0,
            },
            self.ctx,
        )

        records = [
            json.loads(x)
            for x in (Path(self.repo) / "autoresearch.jsonl").read_text().splitlines()
            if x.strip()
        ]
        self.assertEqual(records[0]["wall_ms"], run["wall_ms"])
        self.assertEqual(records[1]["wall_ms"], 2000.0)

        msg = autoresearch.cmd_autoresearch([self.repo], self.ctx)["message"]
        self.assertIn("Efficiency:", msg)
        self.assertIn("keep rate 50% (1/2)", msg)
        self.assertIn("wall/win", msg)

    def test_log_experiment_without_wall_ms_omits_field(self):
        autoresearch.log_experiment(
            {
                "cwd": self.repo,
                "description": "legacy call",
                "metrics": {"score": 1.0},
                "primary_metric": "score",
                "status": "baseline",
            },
            self.ctx,
        )
        record = json.loads((Path(self.repo) / "autoresearch.jsonl").read_text().strip())
        self.assertNotIn("wall_ms", record)
        msg = autoresearch.cmd_autoresearch([self.repo], self.ctx)["message"]
        self.assertIn("total wall 0ms", msg)


if __name__ == "__main__":
    unittest.main()
