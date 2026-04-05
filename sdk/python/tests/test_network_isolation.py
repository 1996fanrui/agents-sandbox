from __future__ import annotations

import asyncio
import os
import re
import shutil
import socket
import subprocess
import tempfile
import threading
from pathlib import Path

import pytest

from agents_sandbox import (
    AgentsSandboxClient,
    CompanionContainerSpec,
    MountSpec,
)

from tests.test_real_runtime import (
    RUNTIME_IMAGE,
    _can_grant_net_admin,
    _cleanup_runtime_resources,
    _ensure_runtime_image,
    _network_name,
    _primary_container_name,
    _running_test_daemon,
    _sanitize_runtime_name,
    _wait_for_client,
)
from tests.smoke_support import daemon_socket_path

COMPANION_NAME = "netcheck"
COMPANION_IMAGE = "nginx:alpine"
SANDBOX_ID = "net-isolation"


def _companion_container_name(sandbox_id: str, companion_name: str) -> str:
    return f"agbox-cc-{_sanitize_runtime_name(sandbox_id)}-{_sanitize_runtime_name(companion_name)}"


def _start_tcp_listener() -> tuple[socket.socket, int, threading.Event]:
    """Start a TCP listener on a random port bound to 0.0.0.0.

    Returns the server socket, the bound port, and a stop event.
    The listener accepts connections in a background thread.
    """
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("0.0.0.0", 0))
    srv.listen(5)
    port = srv.getsockname()[1]
    stop_event = threading.Event()

    def _accept_loop() -> None:
        srv.settimeout(0.5)
        while not stop_event.is_set():
            try:
                conn, _ = srv.accept()
                conn.close()
            except socket.timeout:
                continue
            except OSError:
                break

    t = threading.Thread(target=_accept_loop, daemon=True)
    t.start()
    return srv, port, stop_event


def _get_gateway_ip(sandbox_id: str) -> str:
    """Get the gateway IP of the sandbox Docker network."""
    network = _network_name(sandbox_id)
    result = subprocess.run(
        [
            "docker", "network", "inspect", network,
            "--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}",
        ],
        capture_output=True, text=True, check=True,
    )
    gw = result.stdout.strip()
    if not gw:
        raise AssertionError(f"no gateway IP found for network {network}")
    return gw


def _get_host_interface_ips() -> list[str]:
    """Discover the host's real network interface IPs (global scope, IPv4)."""
    result = subprocess.run(
        ["ip", "-4", "addr", "show", "scope", "global"],
        capture_output=True, text=True, check=True,
    )
    # Lines like: inet 192.168.1.100/24 brd ...
    ips = re.findall(r"inet\s+(\d+\.\d+\.\d+\.\d+)/", result.stdout)
    return ips


def _wait_for_companion_running(container_name: str, timeout: float = 60.0) -> None:
    """Wait until a companion container is running."""
    import time
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = subprocess.run(
            ["docker", "inspect", "--format", "{{.State.Running}}", container_name],
            capture_output=True, text=True, check=False,
        )
        if result.returncode == 0 and result.stdout.strip() == "true":
            return
        time.sleep(0.5)
    raise AssertionError(f"companion container {container_name} did not become running within {timeout}s")


def _cleanup_companion(sandbox_id: str, companion_name: str) -> None:
    container = _companion_container_name(sandbox_id, companion_name)
    subprocess.run(
        ["docker", "rm", "--force", container],
        check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True,
    )


def test_sandbox_cannot_reach_host_but_can_reach_internet(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required")
    if shutil.which("docker") is None:
        pytest.skip("docker is required")
    if not _can_grant_net_admin():
        if os.environ.get("AGBOX_REQUIRE_INTEGRATION"):
            pytest.fail("CAP_NET_ADMIN is required for integration tests but sudo -n setcap is not available")
        pytest.skip("CAP_NET_ADMIN required; grant passwordless sudo or run as root")

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    runtime_dir = Path(tempfile.mkdtemp(prefix="agbox-"))
    socket_path = daemon_socket_path(runtime_dir)
    _ensure_runtime_image(repo_root)
    _cleanup_runtime_resources(SANDBOX_ID)
    _cleanup_companion(SANDBOX_ID, COMPANION_NAME)

    # Start a TCP listener on the host
    srv_socket, listener_port, stop_event = _start_tcp_listener()

    sandbox_id = ""
    try:
        with _running_test_daemon(repo_root, runtime_dir):
            try:
                sandbox_id = asyncio.run(
                    _run_network_isolation_test(
                        socket_path=socket_path,
                        workspace=workspace,
                        listener_port=listener_port,
                    )
                )
            finally:
                _cleanup_companion(sandbox_id or SANDBOX_ID, COMPANION_NAME)
                _cleanup_runtime_resources(sandbox_id or SANDBOX_ID)
    finally:
        stop_event.set()
        srv_socket.close()
        shutil.rmtree(runtime_dir, ignore_errors=True)


async def _run_network_isolation_test(
    *,
    socket_path: Path,
    workspace: Path,
    listener_port: int,
) -> str:
    client = await _wait_for_client(socket_path)
    async with client:
        sandbox = await client.create_sandbox(
            image=RUNTIME_IMAGE,
            sandbox_id=SANDBOX_ID,
            mounts=(MountSpec(source=str(workspace), target="/workspace", writable=True),),
            companion_containers=(
                CompanionContainerSpec(
                    name=COMPANION_NAME,
                    image=COMPANION_IMAGE,
                ),
            ),
        )
        sid = sandbox.sandbox_id

        # Wait for companion container to be running
        companion_container = _companion_container_name(sid, COMPANION_NAME)
        _wait_for_companion_running(companion_container)

        gateway_ip = _get_gateway_ip(sid)
        host_ips = _get_host_interface_ips()

        # --- Negative tests: primary container must NOT reach host ---
        # Use curl --connect-timeout: exit 7 = connection refused, 28 = timeout (both = blocked = GOOD)
        # exit 0 = connected = SECURITY VIOLATION
        exec_handle = await client.run(
            sid,
            ("curl", "--connect-timeout", "3", "-s", "-o", "/dev/null", f"http://{gateway_ip}:{listener_port}"),
            cwd="/workspace",
        )
        assert exec_handle.exit_code != 0, (
            f"SECURITY VIOLATION: primary container reached host via gateway IP {gateway_ip}:{listener_port}"
        )

        for host_ip in host_ips:
            exec_handle = await client.run(
                sid,
                ("curl", "--connect-timeout", "3", "-s", "-o", "/dev/null", f"http://{host_ip}:{listener_port}"),
                cwd="/workspace",
            )
            assert exec_handle.exit_code != 0, (
                f"SECURITY VIOLATION: primary container reached host via interface IP {host_ip}:{listener_port}"
            )

        # --- Positive tests: primary container CAN reach internet ---
        exec_handle = await client.run(
            sid,
            ("curl", "--connect-timeout", "5", "-s", "-o", "/dev/null", "http://1.1.1.1"),
            cwd="/workspace",
        )
        assert exec_handle.exit_code == 0, "primary container cannot reach internet (1.1.1.1)"

        # --- Positive test: primary container CAN reach companion by DNS alias ---
        # Companion runs nginx on port 80. Retry to handle Docker DNS registration delay.
        for attempt in range(10):
            exec_handle = await client.run(
                sid,
                ("curl", "--connect-timeout", "3", "-s", "-o", "/dev/null", f"http://{COMPANION_NAME}:80"),
                cwd="/workspace",
            )
            if exec_handle.exit_code == 0:
                break
            await asyncio.sleep(3)
        assert exec_handle.exit_code == 0, (
            f"primary container cannot reach companion '{COMPANION_NAME}' on port 80 (exit {exec_handle.exit_code})"
        )

        # --- Negative tests: companion container must NOT reach host ---
        # Companion image (nginx:alpine) has wget (busybox applet) but not curl.
        result = subprocess.run(
            [
                "docker", "exec", companion_container,
                "wget", "-q", "-O", "/dev/null", "--timeout=3", f"http://{gateway_ip}:{listener_port}",
            ],
            capture_output=True, text=True, check=False,
        )
        assert result.returncode != 0, (
            f"SECURITY VIOLATION: companion container reached host via gateway IP {gateway_ip}:{listener_port}"
        )

        for host_ip in host_ips:
            result = subprocess.run(
                [
                    "docker", "exec", companion_container,
                    "wget", "-q", "-O", "/dev/null", "--timeout=3", f"http://{host_ip}:{listener_port}",
                ],
                capture_output=True, text=True, check=False,
            )
            assert result.returncode != 0, (
                f"SECURITY VIOLATION: companion container reached host via interface IP {host_ip}:{listener_port}"
            )

        # --- Positive test: companion container CAN reach internet ---
        result = subprocess.run(
            [
                "docker", "exec", companion_container,
                "wget", "-q", "-O", "/dev/null", "--timeout=5", "http://1.1.1.1",
            ],
            capture_output=True, text=True, check=False,
        )
        assert result.returncode == 0, "companion container cannot reach internet (1.1.1.1)"

        return sid
