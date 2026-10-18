"""Exercise real launcher processes with mock transport, including PTY input."""

import hashlib
import json
import base64
import io
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[1]
REAL_PROBE = sys.argv.pop(1)
FIXTURES = ROOT / "tests/fixtures"
RUNNER = ROOT / "run.sh"


class ClientLifecycle(unittest.TestCase):
    def setUp(self):
        self.workspace = tempfile.TemporaryDirectory(prefix="sq-lifecycle-test.")
        self.addCleanup(self.workspace.cleanup)
        self.base = Path(self.workspace.name)
        self.bin = self.base / "bin"
        self.bin.mkdir()
        (self.bin / "curl").symlink_to(FIXTURES / "mock-platform-curl.sh")
        self.temp = self.base / "tmp"
        self.temp.mkdir()
        digest = hashlib.sha256((FIXTURES / "mock-probe.sh").read_bytes()).hexdigest()
        (self.base / "checksums.txt").write_text("".join(
            f"{digest}  sqprobe-linux-{arch}\n" for arch in ["amd64", "arm64"]))
        (self.base / "report.txt").write_text("https://reports.example/r/AbCdEfGhIjKl\n")
        self.environment = dict(os.environ, **{
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "TMPDIR": str(self.temp),
            "SPEEDQUALITY_PROBE_BIN": "",
            "SPEEDQUALITY_PROBE_BASE": "https://reports.example/bin/v1.2.1",
            "SPEEDQUALITY_REPORT_BASE": "https://reports.example",
            "SPEEDQUALITY_REPORT_RESPONSE_FILE": str(self.base / "report.txt"),
            "SPEEDQUALITY_CACHE_DIR": str(self.base / "cache"),
            "SPEEDQUALITY_LEASE_DIR": str(FIXTURES / "leases"),
            "SPEEDQUALITY_NQ_VERIFY_BIN": REAL_PROBE,
            "SPEEDQUALITY_HAS_IPV4": "1", "SPEEDQUALITY_HAS_IPV6": "1",
            "MOCK_PLATFORM_LOG": str(self.base / "platform.log"),
            "MOCK_PLATFORM_CHECKSUMS_FILE": str(self.base / "checksums.txt"),
            "MOCK_PLATFORM_PROBE_SOURCE": str(FIXTURES / "mock-probe.sh"),
            "MOCK_PLATFORM_LEASE_DIR": str(FIXTURES / "leases"),
            "MOCK_ARGS_FILE": str(self.base / "args.txt"),
            "MOCK_PROBE_STARTED_FILE": str(self.base / "started.txt"),
            "MOCK_TARGET_MBPS": "100", "TERM": "dumb",
        })

    def run_client(self, args=None, changes=None):
        return subprocess.run(["bash", str(RUNNER)] + (args or ["-p", "hb", "-s", "100", "-v4"]),
                              env=dict(self.environment, **(changes or {})), stdin=subprocess.DEVNULL,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=20)

    def assert_clean(self):
        self.assertEqual(list(self.temp.iterdir()), [])
        self.assertFalse((self.base / "cache").exists())

    def wait_file(self, file):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if file.exists() and file.stat().st_size:
                return
            time.sleep(0.03)
        self.fail(f"task never reached fixture: {file}")

    def start_blocked(self):
        fifo = self.base / "hold.fifo"
        os.mkfifo(fifo)
        output = (self.base / "first.out").open("w")
        task = subprocess.Popen(["bash", str(RUNNER), "-p", "hb", "-s", "100", "-v4"],
                                env=dict(self.environment, MOCK_PROBE_WAIT_FIFO=str(fifo)),
                                stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT,
                                start_new_session=True)

        def finish():
            if task.poll() is None:
                os.killpg(task.pid, signal.SIGTERM)
                task.wait(timeout=10)
            output.close()
        self.addCleanup(finish)
        self.wait_file(self.base / "started.txt")
        return task, Path((self.base / "started.txt").read_text().strip())

    def tty_client(self, answer, args=None, changes=None, prompt="是否继续并行测速？"):
        master, slave = pty.openpty()
        task = subprocess.Popen(["bash", str(RUNNER)] + (args or ["-p", "hb,bj", "-s", "100"]),
                                env=dict(self.environment, **(changes or {})), stdin=slave, stdout=slave, stderr=slave,
                                start_new_session=True)
        os.close(slave)
        data = b""
        sent = False
        deadline = time.monotonic() + 15
        try:
            while time.monotonic() < deadline:
                ready, _, _ = select.select([master], [], [], 0.1)
                if ready:
                    try:
                        chunk = os.read(master, 65536)
                    except OSError:
                        break
                    if not chunk:
                        break
                    data += chunk
                if not sent and prompt.encode() in data:
                    os.write(master, answer.encode())
                    sent = True
                if task.poll() is not None and not ready:
                    break
            self.assertTrue(sent, data.decode(errors="replace"))
            if task.poll() is None:
                try:
                    task.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    self.fail("launcher did not finish:\n" + data.decode(errors="replace"))
            return task.wait(timeout=3), data.decode(errors="replace")
        finally:
            if task.poll() is None:
                os.killpg(task.pid, signal.SIGTERM)
                task.wait(timeout=5)
            os.close(master)

    def test_one_download_per_command_all_provinces_and_families(self):
        for count in [1, 2]:
            result = self.run_client(["-p", "hb,bj,sh,gd,sc", "-s", "100"])
            self.assertEqual(result.returncode, 0, result.stdout)
            log = (self.base / "platform.log").read_text().splitlines()
            self.assertEqual(log.count("download-probe"), count)
            self.assertEqual(log.count("download-checksums"), count)
            measured = (self.base / "args.txt").read_text().splitlines()
            self.assertEqual(len(measured), count * 10)
            self.assert_clean()

    def test_sq_rejects_second_argument_before_download(self):
        for second in [["--sq", "https://reports.example/r/ZYXWVUTSRQPO"],
                       ["--sq=https://reports.example/r/ZYXWVUTSRQPO"]]:
            result = self.run_client(["-p", "hb", "--sq", "https://reports.example/r/AbCdEfGhIjKl"] + second)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertIn("含本次最多 2 份", result.stdout)
            self.assertFalse((self.base / "platform.log").exists())
            self.assert_clean()

    def test_sq_failures_stop_before_binary_session_or_measurement(self):
        for status, code, message in [
            (409, "sq_limit_exceeded", "该链接已包含 2 份 SQ，不能继续叠加"),
            (422, "source_identity_unavailable", "旧报告缺少可核验的出口身份摘要"),
            (403, "source_identity_mismatch", "出口身份不匹配"),
            (404, "source_unavailable", "SQ 报告不存在或已过期"),
        ]:
            with self.subTest(status=status):
                response = self.base / "sq-preflight.json"
                response.write_text(json.dumps({"error": code, "message": message}, ensure_ascii=False))
                result = self.run_client(["-p", "hb", "--sq", "https://reports.example/r/AbCdEfGhIjKl"], {
                    "MOCK_SQ_PREFLIGHT_STATUS": str(status), "MOCK_SQ_PREFLIGHT_RESPONSE": str(response)})
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn(message, result.stdout)
                self.assertIn("测速尚未开始", result.stdout)
                log = (self.base / "platform.log").read_text()
                self.assertNotIn("download-probe", log)
                self.assertNotIn("session ", log)
                self.assertFalse((self.base / "started.txt").exists())
                self.assert_clean()

    def test_sq_preflight_runs_before_download_and_measurement(self):
        result = self.run_client(["-p", "hb", "--sq", "https://reports.example/r/AbCdEfGhIjKl?tab=sq"])
        self.assertEqual(result.returncode, 0, result.stdout)
        log = (self.base / "platform.log").read_text()
        self.assertLess(log.index("sq-preflight"), log.index("download-probe"))
        self.assertIn("共 2 份", result.stdout)
        self.assert_clean()

    def sq_selection_settings(self, first_available=1):
        response = self.base / "sq-choices.json"
        response.write_text(json.dumps({
            "error": "sq_selection_required", "message": "选择要保留的 SQ",
            "choice_1_label": "2026/10/16 08:02:03（北京时间） | 湖北、北京 | 100 Mbps | IPv4 + IPv6",
            "choice_2_label": "2026/10/17 22:04:05（北京时间） | 广东 | 400 Mbps | IPv6",
            "choice_1_available": first_available, "choice_2_available": 1,
        }, ensure_ascii=False))
        return {"MOCK_SQ_SELECTION_RESPONSE": str(response)}

    def test_joint_sq_can_keep_either_measurement_from_the_terminal(self):
        args = ["-p", "hb", "-s", "100", "-v4", "--sq", "https://reports.example/r/AbCdEfGhIjKl"]
        for answer in ["invalid\n\n1\n", "2\n"]:
            keep = answer.strip()[-1]
            with self.subTest(keep=keep):
                (self.base / "platform.log").unlink(missing_ok=True)
                (self.base / "args.txt").unlink(missing_ok=True)
                status, output = self.tty_client(answer, args, self.sq_selection_settings(), "请选择要保留的 SQ")
                self.assertEqual(status, 0, output)
                self.assertIn("需要手动选择：测速尚未开始，正在等待你的输入", output)
                self.assertIn("输入 1 或 2 后按回车继续", output)
                self.assertIn("  1) 2026/10/16 08:02:03（北京时间） | 湖北、北京 | 100 Mbps | IPv4 + IPv6", output.splitlines())
                self.assertIn("  2) 2026/10/17 22:04:05（北京时间） | 广东 | 400 Mbps | IPv6", output.splitlines())
                self.assertIn(f"已选择保留：SQ {keep}", output)
                self.assertIn("选择完成，开始准备测速", output)
                log = (self.base / "platform.log").read_text()
                self.assertEqual(log.count("sq-preflight"), 2)
                self.assertLess(log.index(f"sq-keep {keep}"), log.index("download-probe"))
                self.assertEqual(len((self.base / "args.txt").read_text().splitlines()), 1)
                self.assertIn("分享报告", output)
                self.assert_clean()

    def test_joint_sq_noninteractive_requires_a_terminal_before_measurement(self):
        args = ["-p", "hb", "-s", "100", "-v4", "--sq", "https://reports.example/r/AbCdEfGhIjKl"]
        settings = self.sq_selection_settings()
        result = self.run_client(args, settings)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("需要在 SSH 终端手动选择", result.stdout)
        self.assertIn("测速尚未开始", result.stdout)
        self.assertNotIn("--sq-keep", result.stdout)
        log = (self.base / "platform.log").read_text()
        self.assertNotIn("download-probe", log)
        self.assertNotIn("session ", log)
        self.assertFalse((self.base / "started.txt").exists())
        self.assert_clean()

    def test_joint_sq_expired_choice_and_cancel_do_not_start_measurements(self):
        args = ["-p", "hb", "-s", "100", "-v4", "--sq", "https://reports.example/r/AbCdEfGhIjKl"]
        status, output = self.tty_client("1\n0\n", args, self.sq_selection_settings(first_available=0),
                                         "请选择要保留的 SQ")
        self.assertEqual(status, 0, output)
        self.assertIn("第 1 份 SQ 已过期或不可用", output)
        self.assertIn("已取消本次测速", output)
        log = (self.base / "platform.log").read_text()
        self.assertNotIn("download-probe", log)
        self.assertNotIn("session ", log)
        self.assertFalse((self.base / "started.txt").exists())
        self.assert_clean()

    def test_joint_sq_selection_checks_both_authenticated_exits(self):
        args = ["-p", "hb", "-s", "100", "--sq", "https://reports.example/r/AbCdEfGhIjKl"]
        status, output = self.tty_client("2\n", args, dict(self.sq_selection_settings(), MOCK_SQ_V6_ONLY="1"),
                                         "请选择要保留的 SQ")
        self.assertEqual(status, 0, output)
        log = (self.base / "platform.log").read_text()
        self.assertIn("sq-keep 2 -4", log)
        self.assertIn("sq-keep 2 -6", log)
        self.assertLess(log.index("sq-keep 2 -6"), log.index("download-probe"))
        self.assertEqual(output.count("请选择要保留的 SQ"), 1)
        self.assert_clean()

    def test_removed_sq_keep_option_stops_before_network_requests(self):
        for option in [["--sq-keep", "1"], ["--sq-keep=2"], ["--sq-keep=3"], ["--sq-keep="]]:
            result = self.run_client(["-p", "hb", "--sq", "https://reports.example/r/AbCdEfGhIjKl"] + option)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertIn("--sq-keep 参数已移除", result.stdout)
            self.assertIn("在 SSH 终端按提示选择", result.stdout)
            self.assertIn("测速尚未开始", result.stdout)
            self.assertFalse((self.base / "platform.log").exists())
            self.assertFalse((self.base / "started.txt").exists())
            self.assert_clean()

    def test_linked_nq_candidates_are_verified_then_nearest_is_selected(self):
        direct = "DirectNQReportToken1234"
        inherited = "InheritedNQReportToken1234"
        info = self.base / "ipinfo.json"
        info.write_text(json.dumps({"ip": "203.0.113.9", "asn": 64500, "ts": int(time.time())}))
        reference_time = int(time.time())

        def record(token, age, address="203.0.113.9"):
            archive = io.BytesIO()
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as output:
                if age is not None:
                    stamp = time.strftime("%Y-%m-%d %H:%M:%S CST", time.gmtime(reference_time - age + 8 * 3600))
                    output.writestr("header_info.log", f"报告时间：{stamp}\n")
                output.writestr("ip_quality.log", f"IP质量体检报告：{address}\n自治系统号：AS64500\n")
            (self.base / f"{token}.json").write_text(json.dumps({"success": True, "data": {
                "asn": 64500, "result": base64.b64encode(archive.getvalue()).decode()}}))

        settings = {"MOCK_NQ_IPINFO": str(info), "MOCK_NQ_RECORD_DIR": str(self.base),
                    "MOCK_NQ_SELECTED_FILE": str(self.base / "selected.txt"),
                    "MOCK_SQ_NQ_URL": f"https://nodequality.com/r/{inherited}",
                    "SPEEDQUALITY_NQ_BINDING_ENABLED": "1", "SPEEDQUALITY_REPORT_RESPONSE_FILE": ""}
        args = ["-p", "hb", "-v4", "--sq", "https://reports.example/r/AbCdEfGhIjKl",
                "--nq", f"https://nodequality.com/r/{direct}"]
        for direct_age, inherited_age, inherited_ip, winner in [
            (3600, 60, "203.0.113.9", inherited),
            (3600, 60, "198.51.100.1", direct),
            (60, 60, "203.0.113.9", direct),
            (None, 3600, "203.0.113.9", inherited),
            (None, None, "203.0.113.9", direct),
            (3600, -60, "203.0.113.9", inherited),
        ]:
            record(direct, direct_age)
            record(inherited, inherited_age, inherited_ip)
            result = self.run_client(args, settings)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertEqual((self.base / "selected.txt").read_text().strip(), f"https://nodequality.com/r/{winner}")
            self.assertLess(result.stdout.index("逐份校验身份"), result.stdout.index("开始运行 SpeedQuality 测速"))
            self.assert_clean()

    def test_download_failures_do_not_measure_and_remove_partial_files(self):
        for code, message in [(6, "域名解析失败"), (7, "无法连接"), (22, "HTTP 404"),
                              (23, "无法写入文件"), (28, "超时"), (35, "HTTPS")]:
            with self.subTest(code=code):
                result = self.run_client(changes={"MOCK_PROBE_DOWNLOAD_EXIT": str(code),
                                                  "MOCK_PROBE_DOWNLOAD_HTTP": "404"})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(message, result.stdout)
                self.assertIn("测速尚未开始", result.stdout)
                self.assertFalse((self.base / "started.txt").exists())
                self.assert_clean()

    def test_checksum_download_and_mismatch_fail_closed(self):
        result = self.run_client(changes={"MOCK_CHECKSUM_DOWNLOAD_EXIT": "28"})
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("download-probe", (self.base / "platform.log").read_text())
        self.assert_clean()
        result = self.run_client(changes={"SPEEDQUALITY_PROBE_SHA256": "0" * 64})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SHA-256 校验失败", result.stdout)
        self.assertFalse((self.base / "started.txt").exists())
        self.assert_clean()

    def test_independent_parallel_tasks_and_interrupt_cleanup(self):
        first, binary = self.start_blocked()
        second = self.run_client()
        self.assertNotEqual(second.returncode, 0, second.stdout)
        self.assertIn("非交互运行已停止", second.stdout)
        self.assertTrue(binary.exists())
        self.assertEqual((self.base / "platform.log").read_text().count("download-probe"), 1)
        second = self.run_client(["-p", "bj", "-s", "100", "-v4", "--allow-parallel"])
        self.assertEqual(second.returncode, 0, second.stdout)
        self.assertIn("检测到另一个SQ测速任务", second.stdout)
        self.assertTrue(binary.exists())
        self.assertEqual(len(list(self.temp.iterdir())), 1)
        os.killpg(first.pid, signal.SIGINT)
        self.assertEqual(first.wait(timeout=10), 130)
        self.assert_clean()

    def test_interactive_default_cancel_and_yes_only_once(self):
        first, binary = self.start_blocked()
        status, output = self.tty_client("\n")
        self.assertEqual(status, 0, output)
        self.assertIn("已取消本次测速", output)
        self.assertTrue(binary.exists())
        status, output = self.tty_client("invalid\ny\n")
        self.assertEqual(status, 0, output)
        self.assertEqual(output.count("是否继续并行测速？"), 2)
        self.assertEqual(output.count("检测到另一个SQ测速任务"), 1)
        self.assertIn("分享报告", output)
        self.assertTrue(binary.exists())
        os.killpg(first.pid, signal.SIGTERM)
        self.assertEqual(first.wait(timeout=10), 143)
        self.assert_clean()

    def test_external_binary_and_unrelated_cache_are_preserved(self):
        cache = self.base / "cache/v1.2.0"
        cache.mkdir(parents=True)
        unrelated = cache / "sqprobe-linux-amd64"
        unrelated.write_text("my unrelated file")
        result = self.run_client()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(unrelated.read_text(), "my unrelated file")
        external = self.base / "my-probe"
        shutil.copy2(FIXTURES / "mock-probe.sh", external)
        result = self.run_client(changes={"SPEEDQUALITY_PROBE_BIN": str(external)})
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue(external.exists())
        self.assertEqual(list(self.temp.iterdir()), [])

    def test_stale_pid_record_does_not_block(self):
        record = Path(tempfile.mkdtemp(prefix=f"speedquality-active.{os.getuid()}.", dir="/tmp"))
        self.addCleanup(shutil.rmtree, record)
        boot = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        # A real process with the wrong start time must not count as a live task.
        (record / "owner").write_text(f"{os.getpid()} 0 {boot}\n")
        result = self.run_client()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("另一个SQ测速任务", result.stdout)
        self.assert_clean()

    def test_two_simultaneous_starts_register_atomically(self):
        fifo = self.base / "hold.fifo"
        os.mkfifo(fifo)
        tasks, logs = [], []
        try:
            for index in range(2):
                log = (self.base / f"race-{index}.out").open("w")
                logs.append(log)
                tasks.append(subprocess.Popen(
                    ["bash", str(RUNNER), "-p", "hb", "-s", "100", "-v4"],
                    env=dict(self.environment, MOCK_PROBE_WAIT_FIFO=str(fifo)),
                    stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                    start_new_session=True))
            self.wait_file(self.base / "started.txt")
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline and all(t.poll() is None for t in tasks):
                time.sleep(0.03)
            self.assertEqual(sum(t.poll() is None for t in tasks), 1)
            self.assertEqual((self.base / "platform.log").read_text().count("download-probe"), 1)
        finally:
            for task in tasks:
                if task.poll() is None:
                    os.killpg(task.pid, signal.SIGTERM)
                task.wait(timeout=10)
            for log in logs:
                log.close()
        self.assert_clean()

    def test_probe_survives_until_nq_binding_and_report(self):
        stamp = time.strftime("%Y-%m-%d %H:%M:%S UTC", time.gmtime())
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as zipped:
            zipped.writestr("header_info.log", f"报告时间：{stamp}  脚本版本：test\n")
            zipped.writestr("ip_quality.log", "IP质量体检报告：45.78.*.*\n自治系统号：AS25820\n")
        record = self.base / "nq.json"
        record.write_text(json.dumps({"success": True, "data": {
            "token": "IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB", "asn": "25820",
            "result": base64.b64encode(archive.getvalue()).decode()}}))
        current = self.base / "ip.json"
        current.write_text(json.dumps({"ip": "45.78.123.4", "asn": 25820}))
        result = self.run_client(["-p", "hb", "-s", "100", "-v4", "--nq",
                                  "https://nodequality.com/r/IHfGBj2jD8OT7BqBNUbCWTWV3XRIbpMB"], {
            "SPEEDQUALITY_NQ_BINDING_ENABLED": "1",
            "SPEEDQUALITY_NODEQUALITY_IPINFO_FILE": str(current),
            "SPEEDQUALITY_NODEQUALITY_RECORD_FILE": str(record)})
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("服务器身份校验通过", result.stdout)
        self.assertIn("分享报告", result.stdout)
        self.assertEqual((self.base / "platform.log").read_text().count("download-probe"), 1)
        self.assert_clean()


if __name__ == "__main__":
    unittest.main(verbosity=2)
