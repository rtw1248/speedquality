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

    def tty_client(self, answer):
        master, slave = pty.openpty()
        task = subprocess.Popen(["bash", str(RUNNER), "-p", "hb,bj", "-s", "100"],
                                env=self.environment, stdin=slave, stdout=slave, stderr=slave,
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
                if not sent and "是否继续并行测速？".encode() in data:
                    os.write(master, answer.encode())
                    sent = True
                if task.poll() is not None and not ready:
                    break
            self.assertTrue(sent, data.decode(errors="replace"))
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
