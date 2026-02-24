"""Display helpers for NeuronSphere local startup/shutdown output."""


def print_header(text):
    """Print 'NeuronSphere Local - {text}' with separator line."""
    print(f"\nNeuronSphere Local \u2014 {text}\n")


def print_step(text):
    """Print an indented step message."""
    print(f"  {text}")


def print_service_table(resources):
    """Print formatted table of accessible service URLs.

    Parses resources["endpoints"] ('name:host:port' -> http://host:port)
    and resources["services"] (gateway URLs transformed to localhost).
    """
    endpoints = resources.get("endpoints", [])
    services = resources.get("services", [])

    # Parse endpoints into (name, url) pairs
    direct_services = []
    for ep in endpoints:
        parts = ep.split(":")
        if len(parts) == 3:
            name, host, port = parts
            direct_services.append((name, f"http://{host}:{port}"))

    # Collect gateway-routed services (internal Docker URLs rewritten to localhost)
    gateway_services = []
    for svc in services:
        if not isinstance(svc, dict):
            continue
        name = svc.get("name", "")
        url = svc.get("url", "")
        if not url:
            continue
        # Skip services already covered by endpoints
        if any(name == ds[0] for ds in direct_services):
            continue
        # Rewrite internal gateway URLs to localhost
        if "hmd_gateway" in url:
            path = url.split("hmd_gateway")[-1]
            gateway_services.append((name, f"http://localhost{path}"))
        elif url.startswith("http://localhost"):
            # Already a localhost URL but not in endpoints — skip to avoid duplication
            continue

    if direct_services:
        print("  Accessible Services:")
        # Calculate column width for alignment
        max_name = max(len(name) for name, _ in direct_services)
        for name, url in direct_services:
            print(f"    {name:<{max_name + 2}} {url}")
        print()

    if gateway_services:
        print("  Services (via Gateway at http://localhost):")
        max_name = max(len(name) for name, _ in gateway_services)
        for name, url in gateway_services:
            print(f"    {name:<{max_name + 2}} {url}")
        print()


def print_database_list(resources):
    """Print list of configured databases."""
    databases = resources.get("databases", [])
    if not databases:
        return

    db_names = []
    for db in databases:
        if isinstance(db, dict) and "database" in db:
            db_names.append(db["database"])

    if db_names:
        print(f"  Databases:")
        print(f"    {', '.join(db_names)}")
        print()


def print_startup_summary(resources):
    """Print the full post-startup summary: header, service table, database list."""
    print_header("Ready")
    print_service_table(resources)
    print_database_list(resources)


def print_shutdown_summary():
    """Print clean 'Stopped' message."""
    print_header("Stopped")
