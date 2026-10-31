"""Exercise full-sized synthetic media with a hard container memory limit.

Only a compiled test binary is copied. The container has no external
network, application settings, production volume, or Telegram credentials.
"""

import os
from pathlib import Path
import subprocess
import tempfile
import uuid


def check():
    with tempfile.TemporaryDirectory(prefix="stash-bot-media-check-") as directory:
        binary = Path(directory) / "media-tests"
        env = os.environ.copy()
        env["CGO_ENABLED"] = "0"
        env["GOOS"] = "linux"
        arch = subprocess.run(
            ["docker", "version", "--format", "{{.Server.Arch}}"],
            check=True, capture_output=True, text=True, timeout=15,
        ).stdout.strip()
        if arch not in {"amd64", "arm64"}:
            raise RuntimeError("unsupported media fixture architecture")
        env["GOARCH"] = arch
        subprocess.run(
            ["go", "test", "-c", "-o", str(binary), "./internal/bot"],
            check=True, env=env, timeout=180,
        )
        name = "stash-bot-media-check-" + uuid.uuid4().hex
        try:
            subprocess.run(
                ["docker", "create", "--name", name, "--network", "none",
                 "--memory", "512m", "--memory-swap", "512m",
                 "--env", "STASH_MEDIA_MEMORY_TEST=1",
                 "alpine:" + os.environ["ALPINE_VERSION"], "/media-tests",
                 "-test.run", "^TestMediaLargeTransferWithinMemoryLimit$",
                 "-test.v", "-test.timeout=120s"],
                check=True, timeout=180,
            )
            subprocess.run(["docker", "cp", str(binary), name + ":/media-tests"], check=True, timeout=30)
            subprocess.run(["docker", "start", "--attach", name], check=True, timeout=150)
            result = subprocess.run(
                ["docker", "inspect", "--format", "{{.State.ExitCode}}", name],
                check=True, capture_output=True, text=True, timeout=10,
            )
            if result.stdout.strip() != "0":
                raise RuntimeError("512 MiB media fixture failed")
        finally:
            subprocess.run(
                ["docker", "rm", "--force", "--volumes", name],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30,
            )


if __name__ == "__main__":
    check()
