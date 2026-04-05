from __future__ import annotations

import asyncio
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
import os
import signal
import shutil
import subprocess
import sys
import tempfile
import time

import pytest

from agents_sandbox import (
    AgentsSandboxClient,
    MountSpec,
    SandboxClientError,
)
from agents_sandbox._grpc_client import SandboxGrpcClient
from tests.smoke_support import daemon_socket_path


def _can_grant_net_admin() -> bool:
    """Check whether we can grant CAP_NET_ADMIN to a binary via setcap.

    On Linux the daemon requires CAP_NET_ADMIN for nftables-based network
    isolation.  If passwordless sudo is unavailable, the test daemon will
    refuse to start, so integration tests must be skipped.
    """
    if sys.platform != "linux":
        return True  # macOS uses --add-host; no capability needed.
    if os.geteuid() == 0:
        return True
    if shutil.which("setcap") is None:
        return False
    result = subprocess.run(
        ["sudo", "-n", "true"],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


RUNTIME_IMAGE_REPOSITORY = "ghcr.io/agents-sandbox/coding-runtime"
CODING_RUNTIME_VERSION_TAG = os.environ.get("CODING_RUNTIME_VERSION_TAG", "0.1.0")
CODING_RUNTIME_IMAGE_TAG = os.environ.get(
    "CODING_RUNTIME_IMAGE_TAG",
    CODING_RUNTIME_VERSION_TAG,
)
RUNTIME_IMAGE = f"{RUNTIME_IMAGE_REPOSITORY}:{CODING_RUNTIME_IMAGE_TAG}"
LATEST_RUNTIME_IMAGE = f"{RUNTIME_IMAGE_REPOSITORY}:latest"


def test_sdk_can_create_real_sandbox_and_exec(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real runtime smoke test")
    if shutil.which("docker") is None:
        pytest.skip("docker is required for the real runtime smoke test")
    if not _can_grant_net_admin():
        if os.environ.get("AGBOX_REQUIRE_INTEGRATION"):
            pytest.fail("CAP_NET_ADMIN is required for integration tests but sudo -n setcap is not available")
        pytest.skip("CAP_NET_ADMIN required; grant passwordless sudo or run as root")

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    runtime_dir = Path(tempfile.mkdtemp(prefix="agbox-"))
    socket_path = daemon_socket_path(runtime_dir)
    _ensure_runtime_image(repo_root)
    _cleanup_runtime_resources("real-runtime-exec")

    sandbox_id = ""
    try:
        with _running_test_daemon(repo_root, runtime_dir):
            try:
                sandbox_id = asyncio.run(
                    _run_real_runtime_exec_flow(
                        socket_path=socket_path,
                        workspace=workspace,
                    )
                )
                _wait_for_container_absent(_primary_container_name(sandbox_id))
            finally:
                _cleanup_runtime_resources(sandbox_id or "real-runtime-exec")
    finally:
        shutil.rmtree(runtime_dir, ignore_errors=True)


def test_sdk_rejects_empty_image_in_real_runtime(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real runtime smoke test")
    if shutil.which("docker") is None:
        pytest.skip("docker is required for the real runtime smoke test")
    if not _can_grant_net_admin():
        if os.environ.get("AGBOX_REQUIRE_INTEGRATION"):
            pytest.fail("CAP_NET_ADMIN is required for integration tests but sudo -n setcap is not available")
        pytest.skip("CAP_NET_ADMIN required; grant passwordless sudo or run as root")

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    runtime_dir = Path(tempfile.mkdtemp(prefix="agbox-"))
    socket_path = daemon_socket_path(runtime_dir)
    _cleanup_runtime_resources("real-runtime-empty-image")

    try:
        with _running_test_daemon(repo_root, runtime_dir):
            with pytest.raises(SandboxClientError):
                asyncio.run(
                    _run_real_runtime_create_with_image(
                        socket_path=socket_path,
                        workspace=workspace,
                        image="",
                    )
                )
    finally:
        shutil.rmtree(runtime_dir, ignore_errors=True)


def test_sdk_can_project_claude_directory_with_symlink(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real runtime smoke test")
    if shutil.which("docker") is None:
        pytest.skip("docker is required for the real runtime smoke test")
    if not _can_grant_net_admin():
        if os.environ.get("AGBOX_REQUIRE_INTEGRATION"):
            pytest.fail("CAP_NET_ADMIN is required for integration tests but sudo -n setcap is not available")
        pytest.skip("CAP_NET_ADMIN required; grant passwordless sudo or run as root")

    fake_home = tmp_path / "home"
    claude_root = fake_home / ".claude"
    claude_root.mkdir(parents=True)
    (claude_root / "settings.json").write_text('{"theme":"dark"}', encoding="utf-8")
    (claude_root / "settings-link.json").symlink_to("settings.json")
    (fake_home / ".claude.json").write_text("{}", encoding="utf-8")

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    runtime_dir = Path(tempfile.mkdtemp(prefix="agbox-"))
    socket_path = daemon_socket_path(runtime_dir)
    go_cache = tmp_path / "go-cache"
    go_cache.mkdir()
    _ensure_runtime_image(repo_root)
    _cleanup_runtime_resources("real-runtime-claude")

    sandbox_id = ""
    try:
        with _running_test_daemon(
            repo_root,
            runtime_dir,
            env={"HOME": str(fake_home), "GOCACHE": str(go_cache)},
        ):
            try:
                sandbox_id = asyncio.run(
                    _run_real_runtime_projection_flow(
                        socket_path=socket_path,
                        workspace=workspace,
                    )
                )
            finally:
                _cleanup_runtime_resources(sandbox_id or "real-runtime-claude")
    finally:
        shutil.rmtree(runtime_dir, ignore_errors=True)


async def _run_real_runtime_exec_flow(*, socket_path: Path, workspace: Path) -> str:
    client = await _wait_for_client(socket_path)
    async with client:
        sandbox = await client.create_sandbox(
            image=RUNTIME_IMAGE,
            sandbox_id="real-runtime-exec",
            mounts=(MountSpec(source=str(workspace), target="/workspace", writable=True),),
        )

        primary_container = _primary_container_name(sandbox.sandbox_id)
        inspect_result = subprocess.run(
            ["docker", "inspect", "--format", "{{.State.Running}}", primary_container],
            check=True,
            capture_output=True,
            text=True,
        )
        assert inspect_result.stdout.strip() == "true"

        exec_handle = await client.run(
            sandbox.sandbox_id,
            ("sh", "-lc", "printf ready"),
            cwd="/workspace",
        )
        assert exec_handle.exit_code == 0
        # Exec output is written to log files on the host; not available in-band.

        deleted = await client.delete_sandbox(sandbox.sandbox_id, wait=True)
        assert deleted.state.name == "DELETED"
        return sandbox.sandbox_id


async def _run_real_runtime_create_with_image(
    *,
    socket_path: Path,
    workspace: Path,
    image: str,
) -> None:
    client = await _wait_for_client(socket_path)
    async with client:
        await client.create_sandbox(
            image=image,
            sandbox_id="real-runtime-empty-image",
            mounts=(MountSpec(source=str(workspace), target="/workspace", writable=True),),
        )


async def _run_real_runtime_projection_flow(*, socket_path: Path, workspace: Path) -> str:
    client = await _wait_for_client(socket_path)
    async with client:
        sandbox = await client.create_sandbox(
            image=RUNTIME_IMAGE,
            sandbox_id="real-runtime-claude",
            mounts=(MountSpec(source=str(workspace), target="/workspace", writable=True),),
            builtin_tools=("claude",),
        )

        exec_handle = await client.run(
            sandbox.sandbox_id,
            (
                "sh",
                "-lc",
                "test -L /home/agbox/.claude/settings-link.json && "
                "cat /home/agbox/.claude/settings-link.json",
            ),
            cwd="/workspace",
        )
        assert exec_handle.exit_code == 0
        # Exec output is written to log files on the host; not available in-band.
        return sandbox.sandbox_id


async def _wait_for_client(socket_path: Path) -> AgentsSandboxClient:
    deadline = time.monotonic() + 30.0
    while time.monotonic() < deadline:
        if socket_path.exists():
            client = _new_client(
                socket_path,
                timeout_seconds=20.0,
                stream_timeout_seconds=20.0,
                operation_timeout_seconds=60.0,
            )
            try:
                ping = await client.ping()
            except Exception:  # noqa: BLE001
                client.close()
                await asyncio.sleep(0.1)
                continue
            assert ping.daemon == "agboxd"
            return client
        await asyncio.sleep(0.1)
    raise AssertionError("AgentsSandbox daemon did not become ready in time")


def _new_client(socket_path: str | Path, **kwargs: object) -> AgentsSandboxClient:
    client = AgentsSandboxClient(**kwargs)
    client.close()
    client.socket_path = str(socket_path)
    client._rpc_client = SandboxGrpcClient(str(socket_path), timeout_seconds=client._timeout_seconds)
    return client


@contextmanager
def _running_test_daemon(
    repo_root: Path,
    runtime_dir: Path,
    *,
    env: dict[str, str] | None = None,
) -> Iterator[None]:
    merged_env = os.environ.copy()
    merged_env["XDG_RUNTIME_DIR"] = str(runtime_dir)
    merged_env["XDG_DATA_HOME"] = str(runtime_dir)
    merged_env["XDG_CONFIG_HOME"] = str(runtime_dir)
    merged_env["HOME"] = str(runtime_dir)
    if env is not None:
        merged_env.update(env)
    daemon_path = runtime_dir / "agboxd-test"
    subprocess.run(
        ["go", "build", "-o", str(daemon_path), "./cmd/agboxd"],
        cwd=repo_root,
        env=os.environ.copy(),
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    # Grant CAP_NET_ADMIN so the test daemon can manage nftables rules.
    if sys.platform == "linux" and shutil.which("setcap") is not None:
        subprocess.run(
            ["sudo", "-n", "setcap", "cap_net_admin+ep", str(daemon_path)],
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    process = subprocess.Popen(
        [str(daemon_path)],
        cwd=repo_root,
        env=merged_env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
        start_new_session=True,
    )
    try:
        yield
    finally:
        _terminate_process_group(process)


def _terminate_process_group(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return

    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return

    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            return
        process.wait(timeout=10)


def _ensure_runtime_image(repo_root: Path) -> None:
    if _image_exists(RUNTIME_IMAGE) and _image_exists(LATEST_RUNTIME_IMAGE):
        return
    subprocess.run(
        [
            "docker",
            "build",
            "--tag",
            RUNTIME_IMAGE,
            "--tag",
            LATEST_RUNTIME_IMAGE,
            str(repo_root / "images" / "coding-runtime"),
        ],
        check=True,
        text=True,
    )


def _image_exists(image: str) -> bool:
    inspect_result = subprocess.run(
        ["docker", "image", "inspect", image],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    return inspect_result.returncode == 0


def _wait_for_container_absent(container_name: str) -> None:
    deadline = time.monotonic() + 60.0
    while time.monotonic() < deadline:
        inspect_result = subprocess.run(
            ["docker", "inspect", container_name],
            capture_output=True,
            text=True,
            check=False,
        )
        if inspect_result.returncode != 0:
            return
        time.sleep(0.1)
    raise AssertionError("expected container removal was not observed before timeout")


def _cleanup_runtime_resources(sandbox_id: str) -> None:
    subprocess.run(
        ["docker", "rm", "--force", "--volumes", _primary_container_name(sandbox_id)],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    subprocess.run(
        ["docker", "network", "rm", _network_name(sandbox_id)],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
    )


def _primary_container_name(sandbox_id: str) -> str:
    return f"agbox-primary-{_sanitize_runtime_name(sandbox_id)}"


def _network_name(sandbox_id: str) -> str:
    return f"agbox-net-{_sanitize_runtime_name(sandbox_id)}"


def _sanitize_runtime_name(value: str) -> str:
    return (
        value.replace("/", "-")
        .replace("\\", "-")
        .replace(":", "-")
        .replace(" ", "-")
        .replace(".", "-")
        .replace("_", "-")
    )
