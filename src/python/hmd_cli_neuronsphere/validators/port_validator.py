"""Port conflict detection for local NeuronSphere startup.

Checks for:
- Host ports claimed by multiple Docker Compose services
- Services using ports reserved by non-Docker CLI tools (e.g. hmd login)
- Host ports already bound by other processes

All checks are warn-only and never block startup.
"""

import os
import socket
from collections import defaultdict
from pathlib import Path
from typing import Dict, List, Optional, Set, Tuple

import yaml
from cement import minimal_logger

logger = minimal_logger("port_validator")

# Ports reserved by non-Docker CLI tools that should not be mapped in compose files.
RESERVED_PORTS: Dict[int, str] = {
    8082: "hmd login (Okta OAuth2 callback)",
}


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
    """Return services that map to a reserved port.

    Returns dict of port -> (reserved_reason, [(service, file), ...]).
    """
    conflicts: Dict[int, Tuple[str, List[Tuple[str, str]]]] = {}
    for port, reason in RESERVED_PORTS.items():
        if port in port_map:
            conflicts[port] = (reason, port_map[port])
    return conflicts


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


def validate_ports(compose_files: List[str]) -> None:
    """Run all port checks and print actionable warnings.

    This is the main entry point called from start_neuronsphere().
    It never raises or blocks startup.
    """
    try:
        port_map = extract_host_ports(compose_files)
        if not port_map:
            return

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
        for port, busy in sorted(in_use.items()):
            if busy:
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
