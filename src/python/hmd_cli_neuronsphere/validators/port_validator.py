"""Port conflict detection for local NeuronSphere startup.

Checks for:
- Host ports claimed by multiple Docker Compose services
- Services using ports reserved by non-Docker CLI tools (e.g. hmd login)
- Host ports already bound by other processes

All checks are warn-only and never block startup.
"""

import os
import re
import socket
import subprocess
from collections import defaultdict
from pathlib import Path
from typing import Dict, List, Optional, Set, Tuple

import yaml
from cement import minimal_logger

from ..floci_deployer import COMPOSE_PROJECT_NAME

logger = minimal_logger("port_validator")

# The one container allowed to publish host ports. Everything else is reached
# through it (see nginx_router); that invariant is what lets multiple named
# environments coexist on one machine.
PROXY_SERVICE_NAMES = frozenset({"proxy"})

# Ports reserved by non-Docker CLI tools that should not be mapped in compose files.
RESERVED_PORTS: Dict[int, str] = {
    8082: "hmd login (Okta OAuth2 callback)",
    80: "hmd_proxy (NeuronSphere HTTP routes)",
    4566: "hmd_proxy (control-plane Floci stream)",
}


def _env_port_range() -> Tuple[int, int]:
    """The contiguous host range hmd_proxy publishes for per-environment streams."""
    try:
        from ..env_registry import env_port_range

        lo, hi = env_port_range().split("-")
        return int(lo), int(hi)
    except Exception:
        return (19000, 19063)


# Host port *ranges* reserved by the proxy, as (low, high, owner).
def reserved_port_ranges() -> List[Tuple[int, int, str]]:
    lo, hi = _env_port_range()
    return [(lo, hi, "hmd_proxy (NeuronSphere per-environment routing)")]


def parse_host_port(port_spec) -> Optional[int]:
    """Extract the host port from a Docker Compose port mapping.

    Handles formats:
        8080            -> 8080
        "8082:8080"     -> 8082
        "6831:6831/udp" -> 6831
        "127.0.0.1:8080:8080" -> 8080

    Returns None for port ranges (e.g. "5000-5010:5000-5010").
    """
    raw = str(port_spec).strip()

    # Strip protocol suffix (/tcp, /udp)
    if "/" in raw:
        raw = raw.split("/")[0]

    parts = raw.split(":")

    if len(parts) == 1:
        # Just a port number — same port on host and container
        host = parts[0]
    elif len(parts) == 2:
        # host:container
        host = parts[0]
    elif len(parts) == 3:
        # ip:host:container
        host = parts[1]
    else:
        return None

    # Skip port ranges
    if "-" in host:
        return None

    try:
        return int(host)
    except ValueError:
        return None


def extract_host_ports(
    compose_files: List[str],
) -> Dict[int, List[Tuple[str, str]]]:
    """Parse compose files and collect host port mappings.

    Returns a dict mapping host port -> list of (service_name, file_path) tuples.
    """
    port_map: Dict[int, List[Tuple[str, str]]] = defaultdict(list)

    for filepath in compose_files:
        try:
            with open(filepath, "r") as f:
                doc = yaml.safe_load(f)
        except Exception as exc:
            logger.debug(f"Skipping {filepath}: {exc}")
            continue

        if not isinstance(doc, dict):
            continue

        services = doc.get("services", {})
        if not isinstance(services, dict):
            continue

        basename = os.path.basename(filepath)
        for svc_name, svc_cfg in services.items():
            if not isinstance(svc_cfg, dict):
                continue
            for port_entry in svc_cfg.get("ports", []):
                host_port = parse_host_port(port_entry)
                if host_port is not None:
                    port_map[host_port].append((svc_name, basename))

    return dict(port_map)


def find_port_conflicts(
    port_map: Dict[int, List[Tuple[str, str]]],
) -> Dict[int, List[Tuple[str, str]]]:
    """Return host ports claimed by more than one service."""
    return {port: owners for port, owners in port_map.items() if len(owners) > 1}


def check_reserved_port_conflicts(
    port_map: Dict[int, List[Tuple[str, str]]],
) -> Dict[int, Tuple[str, List[Tuple[str, str]]]]:
    """Return services that map to a reserved port or reserved range.

    A service owned by the proxy itself is not a conflict -- the proxy is who
    the reservation is *for*.

    Returns dict of port -> (reserved_reason, [(service, file), ...]).
    """
    conflicts: Dict[int, Tuple[str, List[Tuple[str, str]]]] = {}

    def _offenders(port: int) -> List[Tuple[str, str]]:
        return [
            (svc, f)
            for svc, f in port_map.get(port, [])
            if svc not in PROXY_SERVICE_NAMES
        ]

    for port, reason in RESERVED_PORTS.items():
        offenders = _offenders(port)
        if offenders:
            conflicts[port] = (reason, offenders)

    for low, high, owner in reserved_port_ranges():
        for port in port_map:
            if low <= port <= high and port not in conflicts:
                offenders = _offenders(port)
                if offenders:
                    conflicts[port] = (f"{owner} (range {low}-{high})", offenders)

    return conflicts


def find_published_non_proxy_ports(
    port_map: Dict[int, List[Tuple[str, str]]],
) -> Dict[int, List[Tuple[str, str]]]:
    """Return every published host port owned by a service other than the proxy.

    In the control-plane/environment layout that set must be empty: databases
    are unreachable from the host and every service is addressed through
    ``hmd_proxy``.
    """
    offenders: Dict[int, List[Tuple[str, str]]] = {}
    for port, owners in port_map.items():
        non_proxy = [(svc, f) for svc, f in owners if svc not in PROXY_SERVICE_NAMES]
        if non_proxy:
            offenders[port] = non_proxy
    return offenders


def check_ports_in_use(ports: Set[int]) -> Dict[int, bool]:
    """Probe which host ports are already bound.

    Uses a non-blocking TCP connect — works cross-platform with no external deps.
    Returns dict of port -> True if port is already in use.
    """
    in_use: Dict[int, bool] = {}
    for port in sorted(ports):
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(0.2)
        try:
            result = sock.connect_ex(("127.0.0.1", port))
            in_use[port] = result == 0
        except OSError:
            in_use[port] = False
        finally:
            sock.close()
    return in_use


def _known_project_names() -> List[str]:
    """The control-plane project plus every registered environment's project.

    A port bound by one of our own running projects is not a conflict, so the
    "already in use" check must know about all of them -- otherwise every
    running environment reports its own ports as taken.
    """
    names = [COMPOSE_PROJECT_NAME]
    try:
        from ..env_registry import list_envs

        names.extend(
            e.compose_project for e in list_envs() if e.compose_project not in names
        )
    except Exception as exc:
        logger.debug(f"Could not enumerate environment compose projects: {exc}")
    return names


def get_neuronsphere_container_ports(
    project_name: str = None,
    project_names: List[str] = None,
) -> Set[int]:
    """Return host ports currently bound by containers in our compose projects.

    Queries ``docker ps`` for running containers with each compose project label
    and parses the Ports column to extract host port numbers.

    Returns an empty set on any error (Docker not running, timeout, etc.).
    """
    if project_names is None:
        project_names = [project_name] if project_name else _known_project_names()

    ports: Set[int] = set()
    for name in project_names:
        try:
            result = subprocess.run(
                [
                    "docker",
                    "ps",
                    "--filter",
                    f"label=com.docker.compose.project={name}",
                    "--format",
                    "{{.Ports}}",
                ],
                capture_output=True,
                text=True,
                timeout=5,
            )
            if result.returncode != 0:
                continue
            # Each line contains mappings like "0.0.0.0:8080->8080/tcp, :::8080->8080/tcp"
            for line in result.stdout.strip().splitlines():
                for match in re.finditer(r"(?:\d+\.){3}\d+:(\d+)->", line):
                    ports.add(int(match.group(1)))
        except Exception:
            continue
    return ports


def validate_ports(compose_files: List[str], strict: bool = False) -> None:
    """Run all port checks and print actionable warnings.

    This is the main entry point called from start_neuronsphere().

    In the default (warn-only) mode it never raises or blocks startup, which is
    what platform/legacy mode wants. With ``strict=True`` -- used by the
    control-plane and environment paths -- a published host port on any service
    other than the proxy is an error, because that layout guarantees
    ``hmd_proxy`` is the sole publisher.
    """
    try:
        port_map = extract_host_ports(compose_files)
        if not port_map:
            return

        if strict:
            offenders = find_published_non_proxy_ports(port_map)
            if offenders:
                lines = [
                    f"    {port}: " + ", ".join(f"{svc} ({f})" for svc, f in owners)
                    for port, owners in sorted(offenders.items())
                ]
                raise SystemExit(
                    "\n  ERROR: only hmd_proxy may publish host ports in this "
                    "layout, but these services do:\n"
                    + "\n".join(lines)
                    + "\n\n  Remove their `ports:` entries and route them through "
                    "hmd_proxy instead -- HTTP services at "
                    "http://localhost/<env>/<service>/, other protocols via an "
                    "nginx stream listener.\n"
                )

        warnings: List[str] = []

        # 1. Duplicate port mappings across services
        dupes = find_port_conflicts(port_map)
        for port, owners in sorted(dupes.items()):
            svc_list = ", ".join(f"{svc} ({f})" for svc, f in owners)
            warnings.append(f"Port {port} is mapped by multiple services: {svc_list}")

        # 2. Reserved port conflicts
        reserved = check_reserved_port_conflicts(port_map)
        for port, (reason, owners) in sorted(reserved.items()):
            svc_list = ", ".join(f"{svc} ({f})" for svc, f in owners)
            warnings.append(
                f"Port {port} is reserved for {reason} but mapped by: {svc_list}"
            )

        # 3. Ports already in use on the host
        in_use = check_ports_in_use(set(port_map.keys()))
        ns_ports = get_neuronsphere_container_ports()
        for port, busy in sorted(in_use.items()):
            if busy and port not in ns_ports:
                svc_list = ", ".join(f"{svc} ({f})" for svc, f in port_map[port])
                warnings.append(
                    f"Port {port} is already in use on this host "
                    f"(needed by: {svc_list})"
                )

        if warnings:
            print("\n[Port Validation Warnings]")
            for w in warnings:
                print(f"  WARNING: {w}")
            print()

    except Exception as exc:
        logger.debug(f"Port validation skipped due to error: {exc}")
