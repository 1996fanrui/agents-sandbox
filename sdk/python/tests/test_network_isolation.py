from __future__ import annotations

import asyncio
import os
import re
import shlex
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request
from collections.abc import Awaitable, Callable
from pathlib import Path

import pytest

from agents_sandbox import (
    AgentsSandboxClient,
    CompanionContainerSpec,
    MountSpec,
    PortMapping,
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
PUBLISHED_PORT_RESPONSE = "agents-sandbox-published-port-ok"


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
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = subprocess.run(
            ["docker", "inspect", "--format", "{{.State.Running}}", container_name],
            capture_output=True, text=True, check=False,
        )
        if result.returncode == 0 and result.stdout.strip() == "true":
            return
        time.sleep(0.5)
    raise AssertionError(
        f"companion container {container_name} did not become running within {timeout}s"
    )


def _cleanup_companion(sandbox_id: str, companion_name: str) -> None:
    container = _companion_container_name(sandbox_id, companion_name)
    subprocess.run(
        ["docker", "rm", "--force", container],
        check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True,
    )


def _start_port_mapped_container(container_name: str) -> int:
    """Start an nginx container with a randomly assigned host port mapping.

    Returns the host port that Docker mapped to nginx port 80.  The caller is
    responsible for removing the container via _stop_port_mapped_container.
    """
    subprocess.run(
        ["docker", "rm", "--force", container_name],
        check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    subprocess.run(
        ["docker", "run", "-d", "--name", container_name, "-p", "0:80", "nginx:alpine"],
        check=True, stdout=subprocess.DEVNULL,
    )
    result = subprocess.run(
        ["docker", "port", container_name, "80"],
        capture_output=True, text=True, check=True,
    )
    # Output is like "0.0.0.0:32768\n" or "[::]:32768\n"; take the last token.
    host_port = int(result.stdout.strip().rsplit(":", 1)[-1])
    return host_port


def _stop_port_mapped_container(container_name: str) -> None:
    subprocess.run(
        ["docker", "rm", "--force", container_name],
        check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )


def _get_default_docker_bridge_ip() -> str:
    """Return the gateway IP of Docker's default bridge network (typically 172.17.0.1)."""
    result = subprocess.run(
        [
            "docker", "network", "inspect", "bridge",
            "--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}",
        ],
        capture_output=True, text=True, check=False,
    )
    gw = result.stdout.strip()
    return gw if gw else "172.17.0.1"


def _reserve_unused_host_port() -> tuple[socket.socket, int]:
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        srv.bind(("127.0.0.1", 0))
        return srv, srv.getsockname()[1]
    except Exception:
        srv.close()
        raise


def _require_network_integration(repo_root: Path) -> None:
    if shutil.which("go") is None:
        pytest.skip("go is required")
    if shutil.which("docker") is None:
        pytest.skip("docker is required")
    if not _can_grant_net_admin():
        if os.environ.get("AGBOX_REQUIRE_INTEGRATION"):
            pytest.fail("CAP_NET_ADMIN is required for integration tests but sudo -n setcap is not available")
        pytest.skip("CAP_NET_ADMIN required; grant passwordless sudo or run as root")
    _ensure_runtime_image(repo_root)


NetworkScenario = Callable[[Path, Path, str], Awaitable[str]]


def _run_network_scenario(tmp_path: Path, sandbox_id: str, scenario: NetworkScenario) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    _require_network_integration(repo_root)

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    runtime_dir = Path(tempfile.mkdtemp(prefix="agbox-"))
    socket_path = daemon_socket_path(runtime_dir)
    _cleanup_runtime_resources(sandbox_id)
    _cleanup_companion(sandbox_id, COMPANION_NAME)

    created_sandbox_id = ""
    try:
        with _running_test_daemon(repo_root, runtime_dir):
            try:
                created_sandbox_id = asyncio.run(scenario(socket_path, workspace, sandbox_id))
            finally:
                _cleanup_companion(created_sandbox_id or sandbox_id, COMPANION_NAME)
                _cleanup_runtime_resources(created_sandbox_id or sandbox_id)
    finally:
        shutil.rmtree(runtime_dir, ignore_errors=True)


async def _create_network_sandbox(
    *,
    client: AgentsSandboxClient,
    workspace: Path,
    sandbox_id: str,
    ports: tuple[PortMapping, ...] = (),
) -> tuple[str, str, str]:
    sandbox = await client.create_sandbox(
        image=RUNTIME_IMAGE,
        sandbox_id=sandbox_id,
        mounts=(MountSpec(source=str(workspace), target="/workspace", writable=True),),
        ports=ports,
        companion_containers=(
            CompanionContainerSpec(
                name=COMPANION_NAME,
                image=COMPANION_IMAGE,
            ),
        ),
    )
    sid = sandbox.sandbox_id

    companion_container = _companion_container_name(sid, COMPANION_NAME)
    _wait_for_companion_running(companion_container)
    gateway_ip = _get_gateway_ip(sid)
    return sid, companion_container, gateway_ip


async def _assert_primary_cannot_reach(
    client: AgentsSandboxClient,
    sandbox_id: str,
    url: str,
    label: str,
) -> None:
    exec_handle = await client.run(
        sandbox_id,
        ("curl", "--connect-timeout", "3", "-s", "-o", "/dev/null", url),
        cwd="/workspace",
    )
    assert exec_handle.exit_code != 0, f"SECURITY VIOLATION: primary container reached {label} at {url}"


def _assert_companion_cannot_reach(container_name: str, url: str, label: str) -> None:
    result = subprocess.run(
        [
            "docker", "exec", container_name,
            "wget", "-q", "-O", "/dev/null", "--timeout=3", url,
        ],
        capture_output=True, text=True, check=False,
    )
    assert result.returncode != 0, f"SECURITY VIOLATION: companion container reached {label} at {url}"


async def _start_primary_http_server(client: AgentsSandboxClient, sandbox_id: str) -> None:
    expected_response = shlex.quote(PUBLISHED_PORT_RESPONSE)
    exec_handle = await client.run(
        sandbox_id,
        (
            "sh",
            "-lc",
            "set -eu\n"
            "server_dir=/tmp/agbox-port-test\n"
            'mkdir -p "$server_dir"\n'
            'cat > "$server_dir/server.js" <<\'JS\'\n'
            "const http = require('http');\n"
            "const response = process.env.PUBLISHED_PORT_RESPONSE;\n"
            "const server = http.createServer((req, res) => {\n"
            "  res.writeHead(200, {'content-type': 'text/plain'});\n"
            "  res.end(`${response}\\n`);\n"
            "});\n"
            "server.listen(8443, '0.0.0.0');\n"
            "JS\n"
            f"PUBLISHED_PORT_RESPONSE={expected_response} "
            'nohup node "$server_dir/server.js" >"$server_dir/server.log" 2>&1 &\n'
            "server_pid=$!\n"
            "attempt=0\n"
            'while [ "$attempt" -lt 30 ]; do\n'
            "  attempt=$((attempt + 1))\n"
            f"  if curl --fail --silent --show-error --max-time 2 http://127.0.0.1:8443/ | grep -Fx {expected_response}; then\n"
            "    exit 0\n"
            "  fi\n"
            '  if ! kill -0 "$server_pid" 2>/dev/null; then\n'
            '    cat "$server_dir/server.log" >&2 || true\n'
            "    exit 1\n"
            "  fi\n"
            "  sleep 0.2\n"
            "done\n"
            'cat "$server_dir/server.log" >&2 || true\n'
            "exit 1",
        ),
        cwd="/workspace",
    )
    assert exec_handle.exit_code == 0, (
        "failed to start primary HTTP server\n"
        f"{_primary_http_server_diagnostics(sandbox_id)}"
    )


def _primary_http_server_diagnostics(sandbox_id: str) -> str:
    container_name = _primary_container_name(sandbox_id)
    result = subprocess.run(
        [
            "docker", "exec", container_name,
            "sh", "-lc",
            "set +e\n"
            "echo '--- /tmp/agbox-port-test ---'\n"
            "ls -la /tmp/agbox-port-test 2>&1\n"
            "echo '--- server.log ---'\n"
            "cat /tmp/agbox-port-test/server.log 2>&1\n"
            "echo '--- container curl ---'\n"
            "curl --verbose --max-time 2 http://127.0.0.1:8443/ 2>&1",
        ],
        capture_output=True, text=True, check=False,
    )
    return (
        f"docker exec diagnostics for {container_name} exited {result.returncode}\n"
        f"stdout:\n{result.stdout}\n"
        f"stderr:\n{result.stderr}"
    )


def _wait_for_host_http_response(url: str, expected: str, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=3) as response:
                body = response.read().decode("utf-8").strip()
            if body == expected:
                return
            raise AssertionError(f"unexpected response body {body!r}")
        except Exception as exc:  # noqa: BLE001
            last_error = exc
            time.sleep(0.5)
    raise AssertionError(f"host did not receive expected response from {url}: {last_error}") from last_error


async def _assert_primary_can_reach_companion(client: AgentsSandboxClient, sandbox_id: str) -> None:
    for _ in range(10):
        exec_handle = await client.run(
            sandbox_id,
            ("curl", "--connect-timeout", "3", "-s", "-o", "/dev/null", f"http://{COMPANION_NAME}:80"),
            cwd="/workspace",
        )
        if exec_handle.exit_code == 0:
            return
        await asyncio.sleep(3)
    raise AssertionError(f"primary container cannot reach companion '{COMPANION_NAME}' on port 80")


def test_sandbox_published_port_allows_host_ingress_without_sandbox_egress(tmp_path: Path) -> None:
    sandbox_id = "net-published-port"
    port_reservation: socket.socket | None
    port_reservation, published_host_port = _reserve_unused_host_port()

    async def scenario(socket_path: Path, workspace: Path, sandbox_id: str) -> str:
        nonlocal port_reservation
        client = await _wait_for_client(socket_path)
        async with client:
            if port_reservation is None:
                raise AssertionError("host port reservation was already released")
            port_reservation.close()
            port_reservation = None
            sid, companion_container, gateway_ip = await _create_network_sandbox(
                client=client,
                workspace=workspace,
                sandbox_id=sandbox_id,
                ports=(
                    PortMapping(container_port=8443, host_port=published_host_port, protocol="tcp"),
                ),
            )
            await _start_primary_http_server(client, sid)

            host_url = f"http://127.0.0.1:{published_host_port}"
            try:
                _wait_for_host_http_response(host_url, PUBLISHED_PORT_RESPONSE)
            except AssertionError as exc:
                raise AssertionError(
                    f"{exc}\n{_primary_http_server_diagnostics(sid)}"
                ) from exc

            egress_url = f"http://{gateway_ip}:{published_host_port}"
            await _assert_primary_cannot_reach(client, sid, egress_url, "sandbox published host port")
            _assert_companion_cannot_reach(companion_container, egress_url, "sandbox published host port")
            return sid

    try:
        _run_network_scenario(tmp_path, sandbox_id, scenario)
    finally:
        if port_reservation is not None:
            port_reservation.close()


def test_sandbox_host_isolation_blocks_primary_and_companion_egress(tmp_path: Path) -> None:
    sandbox_id = "net-host-isolation"
    dnat_container = "agbox-nettest-dnat"
    _stop_port_mapped_container(dnat_container)

    srv_socket, listener_port, stop_event = _start_tcp_listener()
    dnat_port = _start_port_mapped_container(dnat_container)
    try:
        async def scenario(socket_path: Path, workspace: Path, sandbox_id: str) -> str:
            client = await _wait_for_client(socket_path)
            async with client:
                sid, companion_container, gateway_ip = await _create_network_sandbox(
                    client=client,
                    workspace=workspace,
                    sandbox_id=sandbox_id,
                )
                host_ips = _get_host_interface_ips()
                default_bridge_ip = _get_default_docker_bridge_ip()

                blocked_urls = [(f"http://{gateway_ip}:{listener_port}", "sandbox gateway host listener")]
                blocked_urls.extend(
                    (f"http://{host_ip}:{listener_port}", "host physical interface listener")
                    for host_ip in host_ips
                )
                blocked_urls.extend(
                    [
                        (f"http://{gateway_ip}:{dnat_port}", "unrelated published port via sandbox gateway"),
                        (
                            f"http://{default_bridge_ip}:{dnat_port}",
                            "unrelated published port via default Docker bridge",
                        ),
                    ]
                )

                for url, label in blocked_urls:
                    await _assert_primary_cannot_reach(client, sid, url, label)
                    _assert_companion_cannot_reach(companion_container, url, label)
                return sid

        _run_network_scenario(tmp_path, sandbox_id, scenario)
    finally:
        stop_event.set()
        srv_socket.close()
        _stop_port_mapped_container(dnat_container)


def test_sandbox_primary_can_reach_companion_under_host_isolation(
    tmp_path: Path,
) -> None:
    sandbox_id = "net-companion-access"

    async def scenario(socket_path: Path, workspace: Path, sandbox_id: str) -> str:
        client = await _wait_for_client(socket_path)
        async with client:
            sid, _, _ = await _create_network_sandbox(
                client=client,
                workspace=workspace,
                sandbox_id=sandbox_id,
            )
            await _assert_primary_can_reach_companion(client, sid)
            return sid

    _run_network_scenario(tmp_path, sandbox_id, scenario)
