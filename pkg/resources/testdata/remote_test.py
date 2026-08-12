#!/usr/bin/env python3
"""Tests for the remote builtin extension (ssh exec + tmux driving).

Everything here is a pure-function test except the final class, which does a
real ssh round trip to localhost and skips cleanly when that is unavailable.
"""

import json
import os
import subprocess
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


def _load_remote():
    if "remote" in sys.modules:
        del sys.modules["remote"]
    fir_ext._tools.clear()
    fir_ext._tool_handlers.clear()
    fir_ext._event_handlers.clear()
    fir_ext._hook_handlers.clear()
    fir_ext._commands.clear()
    fir_ext._command_handlers.clear()
    with mock.patch.object(fir_ext, "run"):
        import remote

    # Snapshot the specs at load time: other extension test modules in the
    # same unittest process clear fir_ext's global registries when they load.
    return remote, list(fir_ext._tools)


remote, REMOTE_TOOL_SPECS = _load_remote()


def _payload(result):
    """Decode the JSON envelope out of a tool result."""
    return json.loads(result["content"][0]["text"])


class TestToolRegistration(unittest.TestCase):
    def test_all_six_tools_registered(self):
        names = {t["name"] for t in REMOTE_TOOL_SPECS}
        self.assertEqual({"rexec", "rjob", "rput", "rget", "rtmux", "rhosts"}, names)

    def test_descriptions_teach_the_boundary(self):
        specs = {t["name"]: t for t in REMOTE_TOOL_SPECS}
        self.assertIn("spawn", specs["rexec"]["description"].lower())
        self.assertIn("delegation", specs["rtmux"]["description"].lower())
        self.assertIn("brief", specs["rput"]["description"].lower())

    def test_required_params_declared(self):
        specs = {t["name"]: t for t in REMOTE_TOOL_SPECS}
        self.assertEqual(["host", "command"], specs["rexec"]["parameters"]["required"])
        self.assertEqual(["host", "action"], specs["rtmux"]["parameters"]["required"])


class TestSSHArgv(unittest.TestCase):
    def test_owns_the_flags(self):
        argv = remote._ssh_argv("box", ["bash", "-l", "-s"])
        joined = " ".join(argv)
        self.assertIn("BatchMode=yes", joined)
        self.assertIn("ConnectTimeout=8", joined)
        self.assertIn("ServerAliveInterval=15", joined)
        self.assertIn("ControlMaster=auto", joined)
        self.assertIn("ControlPath=~/.ssh/fir-cm-%C", joined)
        self.assertIn("ControlPersist=120", joined)

    def test_host_then_double_dash_then_remote_argv(self):
        argv = remote._ssh_argv("box", ["bash", "-l", "-s"])
        self.assertEqual(["box", "--", "bash", "-l", "-s"], argv[-5:])

    def test_never_wraps_command_in_bash_lc(self):
        """The whole design: no shell string is ever handed to ssh."""
        argv = remote._ssh_argv("box", ["bash", "-l", "-s"])
        self.assertNotIn("-c", argv)
        for token in argv:
            self.assertNotIn("bash -lc", token)

    def test_scp_shares_the_flags(self):
        argv = remote._scp_argv(["/tmp/a"], "box:/tmp/b")
        self.assertEqual("scp", argv[0])
        self.assertIn("ControlMaster=auto", " ".join(argv))
        self.assertIn("-r", argv)
        self.assertEqual(["/tmp/a", "box:/tmp/b"], argv[-2:])


class TestScriptBuilding(unittest.TestCase):
    def test_command_passes_through_verbatim(self):
        cmd = "echo \"it's $HOME\" | grep -o '*' # weird 'quotes'"
        script = remote._build_script(cmd, None)
        self.assertIn(cmd, script)

    def test_cwd_becomes_a_quoted_cd(self):
        script = remote._build_script("ls", "/tmp/a dir'x")
        self.assertIn("cd -- '/tmp/a dir'\"'\"'x'", script)
        self.assertTrue(script.index("cd --") < script.index("ls"))

    def test_no_cwd_means_no_cd(self):
        self.assertNotIn("cd --", remote._build_script("ls", None))

    def test_heredoc_carries_content_verbatim(self):
        text = "multi\nline 'quoted' $stuff `backticks` \\backslash\n"
        frag = remote._write_file_cmd('"$t"', text)
        first, rest = frag.split("\n", 1)
        delim = first.split("<<")[1].strip("'")
        self.assertTrue(first.startswith('cat > "$t" <<\''), first)
        self.assertEqual(text + delim, rest)

    def test_heredoc_delimiter_is_quoted_so_nothing_expands(self):
        """An unquoted delimiter would let the remote shell expand $stuff."""
        frag = remote._write_file_cmd('"$t"', "echo $HOME\n")
        self.assertRegex(frag.splitlines()[0], r"<<'FIR_EOF_[0-9a-f]{16}'$")

    def test_heredoc_delimiter_rerolls_on_collision(self):
        """Collision is checked, not merely improbable."""
        clash = "FIR_EOF_" + "a" * 16
        rolls = iter(["a" * 16, "b" * 16])
        with mock.patch.object(remote.secrets, "token_hex", lambda _n: next(rolls)):
            delim = remote._heredoc_delimiter(f"line\n{clash}\nmore\n")
        self.assertEqual("FIR_EOF_" + "b" * 16, delim)

    def test_heredoc_content_missing_trailing_newline_still_terminates(self):
        frag = remote._write_file_cmd('"$t"', "echo hi")
        delim = frag.splitlines()[0].split("<<")[1].strip("'")
        self.assertEqual(delim, frag.splitlines()[-1])


class TestDetachScript(unittest.TestCase):
    def setUp(self):
        self.script = remote._detach_script("fir-1-abc", "make all\n", "/srv/x", "box")

    @staticmethod
    def _heredoc_bodies(script):
        """Every quoted-heredoc payload staged by *script*, in order."""
        import re

        lines = script.splitlines()
        bodies = []
        i = 0
        while i < len(lines):
            match = re.search(r"<<'(FIR_EOF_[0-9a-f]+)'$", lines[i])
            if match:
                delim = match.group(1)
                j = i + 1
                body = []
                while j < len(lines) and lines[j] != delim:
                    body.append(lines[j])
                    j += 1
                bodies.append("\n".join(body) + "\n")
                i = j
            i += 1
        return bodies

    @classmethod
    def _decode_runner(cls, script):
        """Pull the staged runner script back out of its heredoc."""
        return next(b for b in cls._heredoc_bodies(script) if b.startswith("#!/bin/bash"))

    def _runner(self):
        return self._decode_runner(self.script)

    def test_carries_command_in_a_quoted_heredoc_not_base64(self):
        """base64 -d is spelled -D on older macOS — the very hosts we target."""
        self.assertNotIn("base64", self.script)
        self.assertIn("make all\n\n", self._heredoc_bodies(self.script))

    def test_prefers_systemd_run(self):
        self.assertIn("systemd-run --user --collect --quiet --unit=fir-1-abc", self.script)

    def test_fallback_double_forks_and_detaches_io(self):
        self.assertIn("( setsid /bin/bash", self.script)
        self.assertIn("</dev/null >/dev/null 2>&1 & ) &", self.script)

    def test_last_resort_launcher_needs_no_setsid(self):
        """macOS has neither systemd-run nor setsid — it must still launch."""
        self.assertIn("command -v setsid", self.script)
        self.assertIn("( set -m; nohup /bin/bash", self.script)
        # `set -m` is the bit that gives the runner its own process group, so
        # the runner's own $$ (recorded as .pid) stays a valid kill -TERM -PID
        # target for rjob kill.
        self.assertIn("echo nohup >", self.script)

    def test_writes_log_rc_and_pid_on_the_remote_box(self):
        self.assertIn("fir-1-abc.log", self.script)
        self.assertIn("$HOME/.cache/fir/rjobs", self.script)
        runner = self._runner()
        self.assertIn("fir-1-abc.rc", runner)
        self.assertIn("fir-1-abc.pid", runner)
        self.assertIn("fir-1-abc.log", runner)

    def test_runner_cds_into_cwd(self):
        runner = self._runner()
        self.assertIn("cd -- /srv/x", runner)
        self.assertIn("echo $? >", runner)

    def test_failed_cd_still_records_an_exit_code(self):
        """Otherwise the job sits in state=unknown forever with an empty log."""
        runner = self._runner()
        self.assertIn("fir: cd failed:", runner)
        self.assertIn("/srv/x", runner)
        self.assertIn("echo 127 >", runner)

    def test_failed_cd_message_quotes_the_cwd(self):
        """A cwd with a quote or $( ) must not corrupt the runner script."""
        import shlex

        cwd = '/srv/a"b$(id)'
        script = remote._detach_script("fir-1-abc", "true\n", cwd, "box")
        runner = TestDetachScript._decode_runner(script)
        # Every mention of the cwd is shell-quoted; none of it is bare inside
        # a double-quoted echo where $( ) would be expanded.
        self.assertNotIn(f'"fir: cd failed: {cwd}"', runner)
        self.assertEqual(2, runner.count(shlex.quote(cwd)))

    def test_job_id_validation(self):
        self.assertTrue(remote._is_safe_job_id("fir-1700000000-abc123"))
        self.assertFalse(remote._is_safe_job_id("../../etc/passwd"))
        self.assertFalse(remote._is_safe_job_id(""))
        self.assertFalse(remote._is_safe_job_id("a" * 100))


class TestClassify(unittest.TestCase):
    def test_ok(self):
        self.assertEqual("ok", remote._classify(0, ""))

    def test_remote_nonzero_is_not_a_transport_failure(self):
        self.assertEqual("nonzero_exit", remote._classify(1, "make: *** error"))
        self.assertEqual("nonzero_exit", remote._classify(127, "not found"))

    def test_auth(self):
        self.assertEqual("auth_failed", remote._classify(255, "Permission denied (publickey)."))
        self.assertEqual("auth_failed", remote._classify(255, "Host key verification failed."))

    def test_unreachable(self):
        self.assertEqual("unreachable", remote._classify(255, "ssh: Could not resolve hostname x"))
        self.assertEqual("unreachable", remote._classify(255, "connect: Connection refused"))
        self.assertEqual("unreachable", remote._classify(255, "something odd"))

    def test_255_from_the_remote_command_itself_is_treated_as_transport(self):
        # Ambiguous by construction; we document the heuristic by pinning it.
        self.assertEqual("unreachable", remote._classify(255, ""))

    def test_a_remote_file_permission_error_is_not_an_auth_failure(self):
        """scp exits 1 with 'Permission denied' for an unwritable remote path.

        Real ssh auth failures always print the method list — `Permission
        denied (publickey)` — so the trailing paren is what distinguishes
        "your key was rejected" from "that directory is root-owned".
        """
        self.assertEqual("nonzero_exit", remote._classify(1, "scp: /root/x: Permission denied"))
        self.assertEqual("nonzero_exit", remote._classify(126, "bash: x: Permission denied"))
        # The method list is the tell for a genuine ssh auth rejection.
        self.assertEqual(
            "auth_failed", remote._classify(255, "user@box: Permission denied (publickey).")
        )


class TestEnvelope(unittest.TestCase):
    def test_empty_stdout_is_explicit(self):
        env = remote._envelope("ok", "box")
        self.assertEqual(0, env["stdout_bytes"])
        self.assertIs(False, env["stdout_truncated"])
        self.assertIn("job_id", env)

    def test_all_required_keys_present(self):
        env = remote._envelope("ok", "box", stdout="hi", duration_ms=5)
        for key in (
            "outcome",
            "host",
            "exit_code",
            "stdout",
            "stdout_bytes",
            "stdout_truncated",
            "stderr",
            "duration_ms",
            "connect_reused",
            "job_id",
        ):
            self.assertIn(key, env)
        self.assertEqual(2, env["stdout_bytes"])

    def test_error_outcomes_are_tool_errors(self):
        for outcome in ("timeout", "unreachable", "auth_failed", "no_tmux", "no_target"):
            self.assertTrue(remote._result(remote._envelope(outcome, "box"))["is_error"], outcome)

    def test_nonzero_exit_is_not_a_tool_error(self):
        result = remote._result(remote._envelope("nonzero_exit", "box", exit_code=2))
        self.assertFalse(result["is_error"])
        self.assertEqual(2, _payload(result)["exit_code"])

    def test_ok_is_not_a_tool_error(self):
        self.assertFalse(remote._result(remote._envelope("ok", "box"))["is_error"])

    def test_result_is_never_empty_content(self):
        result = remote._result(remote._envelope("ok", "box"))
        self.assertTrue(result["content"][0]["text"].strip())


class TestTruncation(unittest.TestCase):
    def test_short_text_untouched(self):
        text, truncated = remote._truncate("hello")
        self.assertEqual("hello", text)
        self.assertFalse(truncated)

    def test_keeps_head_and_tail(self):
        body = "HEAD" + ("x" * (remote._MAX_STREAM_BYTES * 2)) + "TAIL"
        text, truncated = remote._truncate(body)
        self.assertTrue(truncated)
        self.assertTrue(text.startswith("HEAD"))
        self.assertTrue(text.endswith("TAIL"))
        self.assertIn("elided by fir remote", text)
        self.assertLess(len(text), len(body))

    def test_multibyte_output_respects_the_byte_budget(self):
        body = "\u00e9" * remote._MAX_STREAM_BYTES  # 2 bytes each
        text, truncated = remote._truncate(body)
        self.assertTrue(truncated)
        overhead = 80  # the elision marker itself
        self.assertLessEqual(len(text.encode("utf-8")), remote._MAX_STREAM_BYTES + overhead)

    def test_envelope_reports_untruncated_byte_count(self):
        body = "y" * (remote._MAX_STREAM_BYTES * 2)
        env = remote._envelope("ok", "box", stdout=body)
        self.assertTrue(env["stdout_truncated"])
        self.assertEqual(len(body), env["stdout_bytes"])


class TestSSHConfigParsing(unittest.TestCase):
    CONFIG = """
# comment
Host alpha alpha.local
    HostName alpha.example.net
    User kfet
    Port 2222

Host *
    ServerAliveInterval 30

Host beta
    HostName=10.0.0.9
"""

    def test_parses_stanzas(self):
        hosts = remote._parse_ssh_config(self.CONFIG)
        names = [h["host"] for h in hosts]
        self.assertEqual(["alpha", "alpha.local", "*", "beta"], names)

    def test_aliases_share_keywords(self):
        hosts = {h["host"]: h for h in remote._parse_ssh_config(self.CONFIG)}
        self.assertEqual("alpha.example.net", hosts["alpha"]["hostname"])
        self.assertEqual("alpha.example.net", hosts["alpha.local"]["hostname"])
        self.assertEqual("kfet", hosts["alpha"]["user"])
        self.assertEqual("2222", hosts["alpha"]["port"])

    def test_equals_form(self):
        hosts = {h["host"]: h for h in remote._parse_ssh_config(self.CONFIG)}
        self.assertEqual("10.0.0.9", hosts["beta"]["hostname"])

    def test_only_a_leading_key_equals_value_is_split_on_equals(self):
        """`HostName a=b` is one value; only `HostName=a` is the equals form."""
        hosts = {h["host"]: h for h in remote._parse_ssh_config("Host g\n  HostName a=b\n")}
        self.assertEqual("a=b", hosts["g"]["hostname"])

    def test_wildcards_flagged_as_patterns(self):
        hosts = {h["host"]: h for h in remote._parse_ssh_config(self.CONFIG)}
        self.assertTrue(hosts["*"]["pattern"])
        self.assertFalse(hosts["alpha"]["pattern"])

    def test_empty_config(self):
        self.assertEqual([], remote._parse_ssh_config(""))

    def test_include_is_inlined(self):
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            inc = Path(tmp) / "extra"
            inc.write_text("Host gamma\n    HostName g.example\n")
            main = Path(tmp) / "config"
            main.write_text(f"Include {inc}\nHost delta\n")
            hosts = remote._parse_ssh_config(remote._read_ssh_config(main))
            names = [h["host"] for h in hosts]
            self.assertEqual(["gamma", "delta"], names)

    def test_missing_config_is_empty_not_an_exception(self):
        from pathlib import Path

        self.assertEqual("", remote._read_ssh_config(Path("/nonexistent/ssh/config")))

    def test_absolute_include_glob_never_raises(self):
        """A pattern with `..` used to blow up Path('/').glob and crash rhosts."""
        import tempfile
        from pathlib import Path

        with tempfile.TemporaryDirectory() as tmp:
            sub = Path(tmp) / "d"
            sub.mkdir()
            (sub / "extra").write_text("Host viadots\n")
            main = Path(tmp) / "config"
            main.write_text(f"Include {sub}/../d/*\n")
            hosts = remote._parse_ssh_config(remote._read_ssh_config(main))
            self.assertEqual(["viadots"], [h["host"] for h in hosts])


class TestTmuxScripts(unittest.TestCase):
    def test_every_script_guards_on_tmux(self):
        for script in (
            remote._tmux_ls_script(),
            remote._tmux_new_script("s", None, None),
            remote._tmux_send_script("s", "hi", [], 10),
            remote._tmux_cap_script("s", 10),
            remote._tmux_kill_script("s"),
        ):
            self.assertIn("command -v tmux", script)
            self.assertIn(str(remote._RC_NO_TMUX), script)

    def test_new_quotes_command_and_cwd_for_the_model(self):
        script = remote._tmux_new_script("s1", "fir 'do the thing'", "/srv/my dir")
        self.assertIn("'fir '\"'\"'do the thing'\"'\"''", script)
        self.assertIn("'/srv/my dir'", script)
        self.assertIn("-c", script)

    def test_new_without_command_is_a_plain_shell(self):
        script = remote._tmux_new_script("s1", None, None)
        self.assertIn("tmux new-session -d -s s1", script)

    def test_new_waits_for_an_interactive_shell_to_be_ready(self):
        """Typeahead sent into a booting shell is echoed and then discarded."""
        script = remote._tmux_new_script("s1", None, None)
        self.assertIn("sleep 0.25", script)
        self.assertIn('[ "$cur" = "$prev" ]', script)
        # One capture per iteration: an emptiness test and a stability test
        # taken from two different captures can agree on a moving pane.
        self.assertEqual(1, script.count("capture-pane"))

    def test_new_with_a_command_does_not_wait(self):
        script = remote._tmux_new_script("s1", "fir --mode acp", None)
        self.assertNotIn("sleep 0.25", script)
        self.assertNotIn("capture-pane", script)

    def test_failures_propagate_the_exit_code(self):
        for script in (
            remote._tmux_new_script("s", None, None),
            remote._tmux_send_script("s", "hi", [], 10),
            remote._tmux_cap_script("s", 10),
            remote._tmux_kill_script("s"),
        ):
            self.assertIn("|| exit $?", script)

    def test_send_literal_then_keys(self):
        script = remote._tmux_send_script("s1", "echo $HOME", ["Enter", "C-c"], 5)
        self.assertIn("send-keys -t s1 -l -- 'echo $HOME'", script)
        self.assertIn("send-keys -t s1 Enter", script)
        self.assertIn("send-keys -t s1 C-c", script)
        self.assertLess(script.index("-l --"), script.index("Enter"))

    def test_send_returns_a_tail(self):
        script = remote._tmux_send_script("s1", "x", [], 5)
        self.assertIn("---TAIL---", script)
        self.assertIn("capture-pane -p -t s1 -S -5", script)

    def test_cap_uses_capture_pane_and_cursor_flag(self):
        script = remote._tmux_cap_script("s1", 200)
        self.assertIn("capture-pane -p -t s1 -S -200", script)
        self.assertIn("cursor_flag", script)

    def test_auto_session_name(self):
        name = remote._new_session_name()
        self.assertTrue(name.startswith("fir-"))
        self.assertNotEqual(name, remote._new_session_name())


class TestTmuxParsing(unittest.TestCase):
    OUT = (
        "---SESSIONS---\n"
        "work\t1\t1700000000\n"
        "idle\t0\t1700000100\n"
        "---PANES---\n"
        "work\t0\tmain\tfir\t4242\n"
        "work\t0\tmain\tfir\t4243\n"
        "work\t1\tlogs\ttail\t4300\n"
        "idle\t0\tzsh\tzsh\t99\n"
    )

    def test_shape(self):
        sessions = remote._parse_tmux_ls(self.OUT)
        self.assertEqual(["work", "idle"], [s["name"] for s in sessions])
        work = sessions[0]
        self.assertTrue(work["attached"])
        self.assertEqual(1700000000, work["activity_ts"])
        self.assertEqual(2, len(work["windows"]))
        self.assertEqual(
            {"idx": 0, "name": "main", "pane_cmd": "fir", "pid": 4242},
            work["windows"][0],
        )
        self.assertFalse(sessions[1]["attached"])

    def test_no_sessions(self):
        self.assertEqual([], remote._parse_tmux_ls("---SESSIONS---\n---PANES---\n"))

    def test_pane_for_unknown_session_ignored(self):
        out = "---SESSIONS---\n---PANES---\nghost\t0\tw\tsh\t1\n"
        self.assertEqual([], remote._parse_tmux_ls(out))

    def test_strip_ansi(self):
        self.assertEqual("plain", remote._strip_ansi("\x1b[1;32mplain\x1b[0m"))
        self.assertEqual("t", remote._strip_ansi("\x1b]0;title\x07t"))

    def test_split_marker(self):
        before, after = remote._split_marker("a\nb\n---CURSOR---\n1", "---CURSOR---")
        self.assertEqual("a\nb", before)
        self.assertEqual("1", after)

    def test_split_marker_absent(self):
        before, after = remote._split_marker("a\nb", "---X---")
        self.assertEqual("a\nb", before)
        self.assertEqual("", after)


class TestCaptureMemo(unittest.TestCase):
    def setUp(self):
        remote._capture_hashes.clear()

    def test_first_capture_is_changed(self):
        unchanged, digest = remote._capture_unchanged("box", "s1", "hello")
        self.assertFalse(unchanged)
        self.assertTrue(digest)

    def test_identical_capture_is_unchanged(self):
        remote._capture_unchanged("box", "s1", "hello")
        unchanged, _ = remote._capture_unchanged("box", "s1", "hello")
        self.assertTrue(unchanged)

    def test_changed_pane_reported(self):
        remote._capture_unchanged("box", "s1", "hello")
        unchanged, _ = remote._capture_unchanged("box", "s1", "hello\nworld")
        self.assertFalse(unchanged)

    def test_memo_is_per_host_and_target(self):
        remote._capture_unchanged("box", "s1", "hello")
        self.assertFalse(remote._capture_unchanged("other", "s1", "hello")[0])
        self.assertFalse(remote._capture_unchanged("box", "s2", "hello")[0])


class TestTmuxOutcome(unittest.TestCase):
    def test_no_tmux(self):
        env = remote._envelope(
            "nonzero_exit", "box", exit_code=remote._RC_NO_TMUX, stderr="tmux not found"
        )
        out = remote._tmux_outcome(env)
        self.assertEqual("no_tmux", out["outcome"])
        self.assertIn("install tmux", out["hint"])
        self.assertTrue(remote._result(out)["is_error"])

    def test_no_target(self):
        env = remote._envelope(
            "nonzero_exit", "box", exit_code=1, stderr="can't find session: nope"
        )
        self.assertEqual("no_target", remote._tmux_outcome(env)["outcome"])

    def test_other_nonzero_left_alone(self):
        env = remote._envelope("nonzero_exit", "box", exit_code=3, stderr="boom")
        self.assertEqual("nonzero_exit", remote._tmux_outcome(env)["outcome"])

    def test_ok_left_alone(self):
        env = remote._envelope("ok", "box")
        self.assertEqual("ok", remote._tmux_outcome(env)["outcome"])


class TestRjobParsing(unittest.TestCase):
    def test_running(self):
        out = (
            'META:{"job_id":"fir-1-a","host":"box"}\n'
            "PID:1234\nSTATE:running\nRC:\nLOGBYTES:12\n---LOG---\nline one\nline two\n"
        )
        info = remote._parse_rjob_stdout(out)
        self.assertEqual("running", info["state"])
        self.assertIsNone(info["job_exit_code"])
        self.assertEqual(1234, info["pid"])
        self.assertEqual("fir-1-a", info["meta"]["job_id"])
        self.assertEqual(12, info["log_bytes"])
        self.assertEqual("line one\nline two", info["log"])

    def test_done_with_exit_code(self):
        out = "META:{}\nPID:9\nSTATE:done\nRC:3\nLOGBYTES:0\n---LOG---\n"
        info = remote._parse_rjob_stdout(out)
        self.assertEqual("done", info["state"])
        self.assertEqual(3, info["job_exit_code"])

    def test_log_lines_that_look_like_headers_are_not_reparsed(self):
        out = "META:{}\nPID:9\nSTATE:done\nRC:0\nLOGBYTES:9\n---LOG---\nSTATE:running\n"
        info = remote._parse_rjob_stdout(out)
        self.assertEqual("done", info["state"])
        self.assertEqual("STATE:running", info["log"])

    def test_kill_script_handles_both_launchers(self):
        script = remote._rjob_script("fir-1-a", "kill", 10)
        self.assertIn("systemctl --user stop fir-1-a", script)
        self.assertIn('kill -TERM -"$P"', script)

    def test_status_script_guards_missing_job(self):
        script = remote._rjob_script("fir-1-a", "status", 10)
        self.assertIn(str(remote._RC_NO_JOB), script)
        self.assertIn("---LOG---", script)

    def test_tail_honours_lines(self):
        self.assertIn("tail -n 7", remote._rjob_script("fir-1-a", "tail", 7))


class TestToolValidation(unittest.TestCase):
    """Argument validation must fail loudly, never silently return nothing."""

    def setUp(self):
        self.ctx = mock.MagicMock()

    def test_rexec_requires_host_and_command(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rexec({"command": "ls"}, self.ctx)
        with self.assertRaises(fir_ext.ToolError):
            remote.rexec({"host": "box", "command": "   "}, self.ctx)

    def test_rjob_rejects_bad_id(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rjob({"host": "box", "id": "../evil"}, self.ctx)

    def test_rjob_rejects_bad_action(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rjob({"host": "box", "id": "fir-1-a", "action": "nuke"}, self.ctx)

    def test_rtmux_rejects_bad_action(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rtmux({"host": "box", "action": "dance"}, self.ctx)

    def test_rtmux_send_needs_target(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rtmux({"host": "box", "action": "send", "text": "x"}, self.ctx)

    def test_rtmux_send_needs_text_or_keys(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rtmux({"host": "box", "action": "send", "target": "s"}, self.ctx)

    def test_option_like_hosts_are_rejected(self):
        for call in (
            lambda: remote.rexec({"host": "-oProxyCommand=x", "command": "ls"}, self.ctx),
            lambda: remote.rjob({"host": "-x", "id": "fir-1-a"}, self.ctx),
            lambda: remote.rtmux({"host": "-x", "action": "ls"}, self.ctx),
            lambda: remote.rput({"host": "-x", "local": __file__, "remote": "/tmp/x"}, self.ctx),
            lambda: remote.rget({"host": "-x", "remote": "/tmp/x", "local": "/tmp/y"}, self.ctx),
        ):
            with self.assertRaises(fir_ext.ToolError):
                call()

    def test_rput_rejects_missing_local_path(self):
        with self.assertRaises(fir_ext.ToolError):
            remote.rput({"host": "box", "local": "/nope/missing", "remote": "/tmp/x"}, self.ctx)

    def test_non_numeric_timeout_is_a_tool_error_not_a_crash(self):
        """A bare ValueError would surface as an unstructured failure."""
        with self.assertRaises(fir_ext.ToolError):
            remote.rexec({"host": "box", "command": "ls", "timeout_s": "30s"}, self.ctx)
        with self.assertRaises(fir_ext.ToolError):
            remote.rtmux({"host": "box", "action": "cap", "target": "s", "lines": "lots"}, self.ctx)


class TestToolPlumbing(unittest.TestCase):
    """Tool bodies against a stubbed _ssh_exec — no network."""

    def setUp(self):
        self.ctx = mock.MagicMock()
        remote._capture_hashes.clear()

    def test_rexec_ships_script_on_stdin_with_remote_timeout(self):
        captured = {}

        def fake_run(argv, stdin_data, timeout_s):
            captured["argv"] = argv
            captured["stdin"] = stdin_data
            captured["timeout"] = timeout_s
            return 0, "done\n", "", False

        with mock.patch.object(remote, "_run_local", fake_run):
            with mock.patch.object(remote, "_connection_reused", return_value=True):
                result = remote.rexec(
                    {"host": "box", "command": "make all", "timeout_s": 30}, self.ctx
                )
        env = _payload(result)
        self.assertEqual("ok", env["outcome"])
        self.assertEqual("done\n", env["stdout"])
        self.assertEqual(5, env["stdout_bytes"])
        self.assertTrue(env["connect_reused"])
        # The user's script is the ENTIRE stdin payload — no framing, no
        # encoding, byte-for-byte what the caller wrote.
        self.assertEqual(remote._build_script("make all", None), captured["stdin"])
        # No GNU `timeout` in the argv — the bound is a bash supervisor, which
        # is what makes a Mac (no coreutils) work at all.
        self.assertNotIn("timeout", captured["argv"])
        self.assertEqual(["fir-remote", "30", "5"], captured["argv"][-3:])
        self.assertIn("bash -l -s &", captured["argv"][-4])
        self.assertGreater(captured["timeout"], 30)

    def test_rexec_nonzero_is_signal_not_an_error(self):
        with mock.patch.object(remote, "_run_local", return_value=(2, "", "boom", False)):
            result = remote.rexec({"host": "box", "command": "false"}, self.ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual("nonzero_exit", _payload(result)["outcome"])

    def test_rexec_unreachable_is_an_error(self):
        stderr = "ssh: Could not resolve hostname box"
        with mock.patch.object(remote, "_run_local", return_value=(255, "", stderr, False)):
            result = remote.rexec({"host": "box", "command": "true"}, self.ctx)
        self.assertTrue(result["is_error"])
        env = _payload(result)
        self.assertEqual("unreachable", env["outcome"])
        self.assertIn("Could not resolve", env["stderr"])

    def test_rexec_timeout_reports_partial_output_and_duration(self):
        with mock.patch.object(remote, "_run_local", return_value=(124, "partial", "", True)):
            result = remote.rexec({"host": "box", "command": "sleep 999", "timeout_s": 5}, self.ctx)
        env = _payload(result)
        self.assertTrue(result["is_error"])
        self.assertEqual("timeout", env["outcome"])
        self.assertEqual("partial", env["stdout"])
        self.assertEqual(5, env["timeout_s"])
        self.assertIn("duration_ms", env)

    def test_rexec_detach_returns_job_id_immediately(self):
        with mock.patch.object(remote, "_run_local", return_value=(0, "systemd\n", "", False)):
            result = remote.rexec({"host": "box", "command": "make all", "detach": True}, self.ctx)
        env = _payload(result)
        self.assertTrue(env["job_id"].startswith("fir-"))
        self.assertEqual("systemd", env["launcher"])
        self.assertIn(env["job_id"], env["log_path"])

    def test_missing_timeout_binary_is_now_the_users_problem_not_ours(self):
        """We no longer run GNU `timeout`, so a 127 about it is user signal.

        Emitting an infrastructure hint here would misattribute the user's
        own failing command to the transport.
        """
        err = "bash: line 1: timeout: command not found\n"
        with mock.patch.object(remote, "_run_local", return_value=(127, "", err, False)):
            result = remote.rexec({"host": "box", "command": "timeout 5 ls"}, self.ctx)
        env = _payload(result)
        self.assertEqual("nonzero_exit", env["outcome"])
        self.assertNotIn("hint", env)

    def test_command_not_found_is_plain_nonzero_signal(self):
        err = "bash: line 1: frobnicate: command not found\n"
        with mock.patch.object(remote, "_run_local", return_value=(127, "", err, False)):
            env = _payload(remote.rexec({"host": "box", "command": "frobnicate"}, self.ctx))
        self.assertNotIn("hint", env)

    def test_rjob_status_parses_state(self):
        out = "META:{}\nPID:5\nSTATE:done\nRC:0\nLOGBYTES:3\n---LOG---\nok\n"
        with mock.patch.object(remote, "_run_local", return_value=(0, out, "", False)):
            result = remote.rjob({"host": "box", "id": "fir-1-a"}, self.ctx)
        env = _payload(result)
        self.assertEqual("done", env["state"])
        self.assertEqual(0, env["job_exit_code"])
        self.assertEqual("ok", env["stdout"])

    def test_rjob_missing_job_is_no_target(self):
        with mock.patch.object(
            remote, "_run_local", return_value=(remote._RC_NO_JOB, "", "no such job", False)
        ):
            result = remote.rjob({"host": "box", "id": "fir-1-a"}, self.ctx)
        self.assertTrue(result["is_error"])
        self.assertEqual("no_target", _payload(result)["outcome"])

    def test_rtmux_ls_returns_structure_not_raw_text(self):
        with mock.patch.object(
            remote, "_run_local", return_value=(0, TestTmuxParsing.OUT, "", False)
        ):
            result = remote.rtmux({"host": "box", "action": "ls"}, self.ctx)
        env = _payload(result)
        self.assertEqual(2, env["session_count"])
        self.assertEqual("work", env["sessions"][0]["name"])
        self.assertEqual("", env["stdout"])

    def test_rtmux_no_tmux_is_a_tool_error(self):
        with mock.patch.object(
            remote,
            "_run_local",
            return_value=(remote._RC_NO_TMUX, "", "tmux not found on remote host", False),
        ):
            result = remote.rtmux({"host": "box", "action": "ls"}, self.ctx)
        self.assertTrue(result["is_error"])
        env = _payload(result)
        self.assertEqual("no_tmux", env["outcome"])
        self.assertIn("install tmux or use rexec detach=True", env["hint"])

    def test_rtmux_new_generates_a_name(self):
        with mock.patch.object(remote, "_run_local", return_value=(0, "", "", False)):
            result = remote.rtmux({"host": "box", "action": "new"}, self.ctx)
        self.assertTrue(_payload(result)["name"].startswith("fir-"))

    def test_rtmux_cap_dedupes_identical_panes(self):
        out = "hello world\n---CURSOR---\n1\n"
        with mock.patch.object(remote, "_run_local", return_value=(0, out, "", False)):
            cap = {"host": "box", "action": "cap", "target": "s"}
            first = _payload(remote.rtmux(cap, self.ctx))
            second = _payload(remote.rtmux(cap, self.ctx))
        self.assertFalse(first["unchanged"])
        self.assertEqual("hello world", first["capture"])
        self.assertEqual(11, first["capture_bytes"])
        self.assertFalse(first["capture_truncated"])
        self.assertTrue(first["cursor_visible"])
        self.assertTrue(second["unchanged"])
        self.assertEqual("", second["capture"])
        self.assertEqual(first["capture_hash"], second["capture_hash"])

    def test_rtmux_send_invalidates_the_capture_memo(self):
        cap_out = "hello\n---CURSOR---\n0\n"
        with mock.patch.object(remote, "_run_local", return_value=(0, cap_out, "", False)):
            remote.rtmux({"host": "box", "action": "cap", "target": "s"}, self.ctx)
        send_out = "---TAIL---\nhello\n"
        with mock.patch.object(remote, "_run_local", return_value=(0, send_out, "", False)):
            sent = _payload(
                remote.rtmux(
                    {
                        "host": "box",
                        "action": "send",
                        "target": "s",
                        "text": "x",
                        "keys": ["Enter"],
                    },
                    self.ctx,
                )
            )
        self.assertEqual("hello", sent["stdout"])
        self.assertEqual(["Enter"], sent["sent_keys"])
        with mock.patch.object(remote, "_run_local", return_value=(0, cap_out, "", False)):
            again = _payload(
                remote.rtmux({"host": "box", "action": "cap", "target": "s"}, self.ctx)
            )
        self.assertFalse(again["unchanged"])

    def test_rhosts_without_probe_does_not_touch_the_network(self):
        with mock.patch.object(remote, "_read_ssh_config", return_value="Host a\n  User u\n"):
            with mock.patch.object(remote, "_run_local", side_effect=AssertionError("no ssh")):
                result = remote.rhosts({}, self.ctx)
        env = _payload(result)
        self.assertEqual(1, env["host_count"])
        self.assertEqual("a", env["hosts"][0]["host"])
        # rhosts shares the one envelope shape, so a model that learned the
        # keys from rexec can read this result without special-casing.
        for key in ("outcome", "exit_code", "stdout_bytes", "connect_reused", "job_id"):
            self.assertIn(key, env)

    def test_rhosts_probe_classifies_and_skips_wildcards(self):
        config = "Host a\nHost b\nHost *\n"

        def fake_run(argv, stdin_data, timeout_s):
            if "a" in argv:
                return 0, "", "", False
            return 255, "", "Permission denied (publickey).", False

        with mock.patch.object(remote, "_read_ssh_config", return_value=config):
            with mock.patch.object(remote, "_run_local", fake_run):
                env = _payload(remote.rhosts({"probe": True}, self.ctx))
        self.assertEqual(["a"], env["reachable"])
        self.assertEqual(["b"], env["auth_failed"])
        self.assertEqual([], env["unreachable"])
        wildcard = next(h for h in env["hosts"] if h["host"] == "*")
        self.assertNotIn("status", wildcard)

    def test_rput_and_rget_build_scp_argv(self):
        captured = {}

        def fake_run(argv, stdin_data, timeout_s):
            captured.setdefault("argvs", []).append(argv)
            return 0, "", "", False

        with mock.patch.object(remote, "_run_local", fake_run):
            put = _payload(
                remote.rput({"host": "box", "local": __file__, "remote": "/tmp/x"}, self.ctx)
            )
            get = _payload(
                remote.rget({"host": "box", "remote": "/tmp/x", "local": __file__}, self.ctx)
            )
        self.assertEqual("ok", put["outcome"])
        self.assertEqual("put", put["direction"])
        self.assertGreater(put["local_bytes"], 0)
        self.assertEqual("box:/tmp/x", captured["argvs"][0][-1])
        self.assertEqual("get", get["direction"])
        self.assertEqual("box:/tmp/x", captured["argvs"][1][-2])

    def test_scp_remote_permission_error_is_signal_not_auth_failure(self):
        """scp exits 1 for an unwritable remote path; that is not an auth failure."""
        err = "scp: dest open '/root/x': Permission denied\n"
        with mock.patch.object(remote, "_run_local", return_value=(1, "", err, False)):
            result = remote.rput({"host": "box", "local": __file__, "remote": "/root/x"}, self.ctx)
        self.assertFalse(result["is_error"])
        self.assertEqual("nonzero_exit", _payload(result)["outcome"])

    def test_scp_real_auth_failure_is_an_error(self):
        err = "user@box: Permission denied (publickey).\n"
        with mock.patch.object(remote, "_run_local", return_value=(1, "", err, False)):
            result = remote.rput({"host": "box", "local": __file__, "remote": "/tmp/x"}, self.ctx)
        self.assertTrue(result["is_error"])
        self.assertEqual("auth_failed", _payload(result)["outcome"])

    def test_sub_second_timeout_never_disables_the_remote_timeout(self):
        """A sub-second bound must floor to 1s, not degrade to "no limit"."""
        captured = {}

        def fake_run(argv, stdin_data, timeout_s):
            captured["argv"] = argv
            captured["stdin"] = stdin_data
            return 124, "", "", True

        with mock.patch.object(remote, "_run_local", fake_run):
            result = remote.rexec({"host": "box", "command": "true", "timeout_s": 0.4}, self.ctx)
        self.assertEqual(["fir-remote", "1", "5"], captured["argv"][-3:])
        # The envelope reports the bound that was actually applied, not the
        # sub-second value that would have meant "no limit".
        self.assertEqual(1, _payload(result)["timeout_s"])

    def test_detach_log_path_is_resolvable_not_a_literal_dollar_home(self):
        with mock.patch.object(remote, "_run_local", return_value=(0, "systemd\n", "", False)):
            env = _payload(remote.rexec({"host": "box", "command": "x", "detach": True}, self.ctx))
        self.assertTrue(env["log_path"].startswith("~/.cache/fir/rjobs/"), env["log_path"])
        self.assertNotIn("$HOME", env["log_path"])


class TestRemoteSupervisor(unittest.TestCase):
    """The one-line remote bound that replaced GNU `timeout -k`."""

    def setUp(self):
        self.sup = remote._REMOTE_SUPERVISOR

    def test_invokes_no_timeout_binary(self):
        """The whole point: macOS has no `timeout`, Homebrew calls it gtimeout."""
        self.assertNotIn("timeout", self.sup)
        self.assertNotIn("gtimeout", self.sup)

    def test_is_one_line_and_free_of_single_quotes(self):
        """Both are load-bearing: it must survive one pass of ANY login shell.

        A newline breaks csh's quoting; a single quote would force shlex into
        the `'"'"'` seam form, which is exactly the nested-quoting shape this
        extension exists to never emit.
        """
        self.assertNotIn("\n", self.sup)
        self.assertNotIn("'", self.sup)

    def test_is_valid_bash(self):
        """A syntax error here fails every call to every host."""
        proc = subprocess.run(
            [_bash(), "-n", "-c", self.sup, "fir-remote", "5", "2"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(0, proc.returncode, proc.stderr)

    def test_ampersand_is_not_followed_by_a_semicolon(self):
        """`cmd &;` is a syntax error — the joiner has to know that."""
        self.assertNotIn("&;", self.sup)
        self.assertEqual("a & b;", remote._oneline(["a &", "b"]))

    def test_reads_the_user_script_from_the_inherited_stdin(self):
        """No staging: `bash -l -s` reads the ssh channel directly.

        `set -m` is what allows it — POSIX only redirects an async command's
        stdin to /dev/null when job control is OFF.
        """
        self.assertIn("set -m", self.sup)
        self.assertIn("bash -l -s &", self.sup)
        self.assertLess(self.sup.index("set -m"), self.sup.index("bash -l -s &"))
        self.assertNotIn("mktemp", self.sup)
        self.assertNotIn("cat >", self.sup)

    def test_watchdog_signals_the_group_term_then_kill_after_grace(self):
        self.assertIn("kill -TERM -$__fir_c", self.sup)
        self.assertIn("kill -KILL -$__fir_c", self.sup)
        self.assertLess(
            self.sup.index("kill -TERM -$__fir_c"), self.sup.index("kill -KILL -$__fir_c")
        )
        # single-pid fallbacks for a bash built without job control
        self.assertIn("|| kill -TERM $__fir_c", self.sup)
        self.assertIn("|| kill -KILL $__fir_c", self.sup)

    def test_watchdog_runs_detached_from_stdin(self):
        """Otherwise it competes with the child for the user's script."""
        self.assertIn("} </dev/null 2>/dev/null &", self.sup)

    def test_expiry_is_decided_by_elapsed_seconds_and_reports_124(self):
        self.assertIn("__fir_e=$SECONDS", self.sup)
        self.assertIn("[ $__fir_e -ge $__fir_n ] && __fir_rc=124", self.sup)

    def test_elapsed_is_sampled_before_the_watchdog_kill(self):
        """Sampling after could let a slow signal invent a timeout."""
        # rindex: the abort handler also kills the watchdog, earlier in the text.
        self.assertLess(self.sup.index("__fir_e=$SECONDS"), self.sup.rindex("kill -TERM -$__fir_w"))

    def test_a_signal_at_the_supervisor_is_forwarded_to_the_group(self):
        """sshd tearing the session down must not orphan the command.

        This is *not* the local-timeout case: when the local side gives up,
        the remote is usually never signalled (no pty means no HUP, and the
        ssh mux holds the channel open), and the watchdog is what bounds it —
        exactly as GNU `timeout` behaved.
        """
        self.assertIn("trap __fir_abort TERM HUP INT", self.sup)
        self.assertLess(self.sup.index("__fir_abort()"), self.sup.index("trap __fir_abort"))

    def test_abort_uses_the_saved_grace_not_a_positional(self):
        """Inside a function $2 is the *function's* arg — a classic footgun."""
        abort = self.sup[self.sup.index("__fir_abort()") : self.sup.index("trap __fir_abort")]
        self.assertIn("sleep $__fir_g", abort)
        self.assertNotIn("$2", abort)

    def test_child_keeps_the_real_stderr(self):
        """`exec 2>/dev/null` must come after the fork, or output is lost."""
        self.assertLess(self.sup.index("bash -l -s &"), self.sup.index("exec 2>/dev/null"))

    def test_argv_carries_the_bound_as_positional_params(self):
        argv = remote._supervisor_argv(30, 5)
        self.assertEqual(["bash", "-c"], argv[:2])
        self.assertEqual(["fir-remote", "30", "5"], argv[3:])
        self.assertIn("__fir_n=$1", argv[2])

    def test_argv_quotes_the_supervisor_for_the_remote_shell(self):
        """ssh joins argv with spaces and the remote login shell re-splits."""
        argv = remote._supervisor_argv(30, 5)
        self.assertTrue(argv[2].startswith("'") and argv[2].endswith("'"), argv[2][:40])
        self.assertEqual(remote._REMOTE_SUPERVISOR, argv[2][1:-1])

    def test_ssh_argv_disables_the_tty(self):
        """A background job reading a tty takes SIGTTIN and runs nothing (149)."""
        self.assertIn("-T", remote._ssh_argv("box", ["bash"]))

    def test_scp_never_gets_dash_t(self):
        """scp -T is a different option entirely — it disables name checking."""
        self.assertNotIn("-T", remote._scp_argv(["a"], "box:b"))


def _bash() -> str:
    import shutil

    return shutil.which("bash") or "/bin/bash"


class TestSupervisorExecution(unittest.TestCase):
    """Run the supervisor for real under local bash — no ssh, no network.

    This is the honest test of the macOS bug: `timeout` on PATH is poisoned so
    that any call to it fails loudly, which is what a host without GNU
    coreutils looks like.
    """

    def setUp(self):
        import tempfile

        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        bindir = os.path.join(self.tmp.name, "bin")
        os.makedirs(bindir)
        for name in ("timeout", "gtimeout"):
            stub = os.path.join(bindir, name)
            with open(stub, "w") as fh:
                fh.write("#!/bin/sh\necho 'poisoned: no GNU timeout here' >&2\nexit 127\n")
            os.chmod(stub, 0o700)
        self.env = dict(os.environ)
        self.env["PATH"] = bindir + os.pathsep + self.env.get("PATH", "")

    def _argv(self, timeout_s, grace):
        return [_bash(), *remote._supervisor_argv(timeout_s, grace)[1:]]

    def _run(self, script, timeout_s, grace=1, local_timeout=60):
        # argv[2] is quoted for a remote shell; running it locally through
        # subprocess (no shell) means we pass the unquoted original.
        argv = self._argv(timeout_s, grace)
        argv[2] = remote._REMOTE_SUPERVISOR
        with mock.patch.dict(os.environ, self.env, clear=True):
            return remote._run_local(argv, script, local_timeout)

    def test_normal_command_works_on_a_host_without_gnu_timeout(self):
        rc, out, err, timed_out = self._run("echo hello\necho oops >&2\n", 30)
        self.assertEqual(0, rc, err)
        self.assertFalse(timed_out)
        self.assertIn("hello", out)
        self.assertIn("oops", err)
        self.assertNotIn("poisoned", err)

    def test_login_shell_semantics_are_preserved(self):
        rc, out, _err, _t = self._run("shopt -q login_shell && echo login-shell\n", 30)
        self.assertEqual(0, rc)
        self.assertIn("login-shell", out)

    def test_dollar_zero_is_unchanged_from_before_the_bound_existed(self):
        """The script is still the stdin of a `bash -l -s`, not a staged file."""
        rc, out, _err, _t = self._run('echo "[$0]"\n', 30)
        self.assertEqual(0, rc)
        self.assertEqual("[bash]", out.strip())

    def test_hostile_quoting_survives_verbatim(self):
        script = "echo \"it's fine\"; printf '%s\\n' 'a b'  # 'unbalanced\n"
        rc, out, _err, _t = self._run(script, 30)
        self.assertEqual(0, rc)
        self.assertIn("it's fine", out)
        self.assertIn("a b", out)

    def test_exit_code_is_the_commands_own(self):
        rc, _out, _err, _t = self._run("exit 7\n", 30)
        self.assertEqual(7, rc)

    def test_death_by_signal_keeps_128_plus_signal(self):
        rc, _out, _err, _t = self._run("kill -9 $$\n", 30)
        self.assertEqual(137, rc)

    def test_nothing_is_staged_on_disk(self):
        self._run("true\n", 30)
        leftover = [n for n in os.listdir(self.tmp.name) if n != "bin"]
        self.assertEqual([], leftover)

    def test_expiry_kills_the_whole_process_group_and_reports_124(self):
        script = "sleep 300 &\necho PID:$!\necho partial\necho perr >&2\nsleep 300\n"
        rc, out, err, _t = self._run(script, 1, grace=1)
        self.assertEqual(124, rc)
        self.assertIn("partial", out)
        self.assertIn("perr", err)
        # A job-control shell would print "Terminated: 15" into the user's
        # stderr here; the supervisor hides its own job notices.
        self.assertNotIn("Terminated", err)
        pid = int(next(ln for ln in out.splitlines() if ln.startswith("PID:")).split(":")[1])
        self._assert_gone(pid)

    def test_a_term_at_the_supervisor_kills_the_group_and_exits_124(self):
        """The trap path: sshd tearing the session down, not a local timeout."""
        import select

        argv = self._argv(300, 1)
        argv[2] = remote._REMOTE_SUPERVISOR
        with mock.patch.dict(os.environ, self.env, clear=True):
            proc = subprocess.Popen(
                argv,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                start_new_session=True,
            )
        self.addCleanup(proc.kill)
        stdin, stdout, stderr = proc.stdin, proc.stdout, proc.stderr
        if stdin is None or stdout is None or stderr is None:
            raise AssertionError("subprocess pipes were not created")
        self.addCleanup(stderr.close)
        self.addCleanup(stdout.close)
        stdin.write("sleep 300 &\necho PID:$!\nsleep 300\n")
        stdin.close()
        ready, _, _ = select.select([stdout], [], [], 20)
        self.assertTrue(ready, "supervisor produced no output within 20s")
        pid = int(stdout.readline().split(":")[1])
        proc.terminate()  # the supervisor itself, in its own session
        self.assertEqual(124, proc.wait(timeout=30))
        self._assert_gone(pid)

    def _assert_gone(self, pid):
        """A backgrounded grandchild must die with the group, not linger."""
        import time as _time

        deadline = _time.time() + 20
        while _time.time() < deadline:
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                return
            except PermissionError:  # reparented and reused — good enough
                return
            _time.sleep(0.02)
        self.fail(f"pid {pid} survived the timeout kill — orphaned process group")


def _localhost_ssh_works() -> bool:
    try:
        argv = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "localhost", "true"]
        proc = subprocess.run(
            argv,
            capture_output=True,
            timeout=20,
        )
        return proc.returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


class TestBinaryOutput(unittest.TestCase):
    """Non-UTF-8 remote output must not blow up the envelope."""

    def test_run_local_replaces_undecodable_bytes(self):
        rc, out, err, timed_out = remote._run_local(
            ["/bin/sh", "-c", "printf '\\377\\376ok'"], None, 30
        )
        self.assertEqual(0, rc)
        self.assertFalse(timed_out)
        self.assertTrue(out.endswith("ok"))
        self.assertEqual("", err)

    def test_envelope_survives_replacement_characters(self):
        env = remote._envelope("ok", "box", stdout="\ufffd\ufffdok")
        self.assertEqual(8, env["stdout_bytes"])


@unittest.skipUnless(
    os.environ.get("FIR_REMOTE_INTEGRATION") and _localhost_ssh_works(),
    "set FIR_REMOTE_INTEGRATION=1 and enable passwordless ssh to localhost",
)
class TestLocalhostIntegration(unittest.TestCase):
    """Real ssh round trips against localhost."""

    ctx = None

    def test_rexec_roundtrip_with_hostile_quoting(self):
        cmd = "echo \"it's fine\"; printf '%s\\n' 'a b'  # 'unbalanced\n"
        env = _payload(remote.rexec({"host": "localhost", "command": cmd}, self.ctx))
        self.assertEqual("ok", env["outcome"])
        self.assertIn("it's fine", env["stdout"])
        self.assertIn("a b", env["stdout"])

    def test_rexec_nonzero(self):
        env = _payload(remote.rexec({"host": "localhost", "command": "exit 7"}, self.ctx))
        self.assertEqual("nonzero_exit", env["outcome"])
        self.assertEqual(7, env["exit_code"])

    def test_rexec_timeout(self):
        env = _payload(
            remote.rexec({"host": "localhost", "command": "sleep 30", "timeout_s": 2}, self.ctx)
        )
        self.assertEqual("timeout", env["outcome"])

    def test_detach_and_rjob(self):
        import time as _time

        env = _payload(
            remote.rexec(
                {"host": "localhost", "command": "echo started; exit 4", "detach": True},
                self.ctx,
            )
        )
        self.assertEqual("ok", env["outcome"])
        job_id = env["job_id"]
        for _ in range(40):
            status = _payload(remote.rjob({"host": "localhost", "id": job_id}, self.ctx))
            if status["state"] == "done":
                break
            _time.sleep(0.25)
        self.assertEqual("done", status["state"])
        self.assertEqual(4, status["job_exit_code"])
        self.assertIn("started", status["stdout"])


if __name__ == "__main__":
    unittest.main()
