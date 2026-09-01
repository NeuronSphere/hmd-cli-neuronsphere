"""Display helpers for NeuronSphere local startup/shutdown output."""

import sys

import yaspin


# Block-letter "NEURONSPHERE", built from a small 5-row font (see
# print_banner) rather than hand-typed, so column alignment across letters
# is guaranteed by construction instead of by eye.
_BANNER_FONT = {
    "N": ["#   #", "##  #", "# # #", "#  ##", "#   #"],
    "E": ["#####", "#    ", "###  ", "#    ", "#####"],
    "U": ["#   #", "#   #", "#   #", "#   #", "#####"],
    "R": ["#### ", "#   #", "#### ", "#  # ", "#   #"],
    "O": [" ### ", "#   #", "#   #", "#   #", " ### "],
    "S": [" ####", "#    ", " ### ", "    #", "#### "],
    "P": ["#### ", "#   #", "#### ", "#    ", "#    "],
    "H": ["#   #", "#   #", "#####", "#   #", "#   #"],
}


def print_banner():
    """Print the ASCII 'NEURONSPHERE' banner shown once at the start of `up`."""
    rows = ["" for _ in range(5)]
    for ch in "NEURONSPHERE":
        glyph = _BANNER_FONT[ch]
        for i in range(5):
            rows[i] += glyph[i] + " "
    print()
    for row in rows:
        print(row.rstrip())
    print()


def print_header(text):
    """Print 'NeuronSphere Local - {text}' with separator line."""
    print(f"\nNeuronSphere Local \u2014 {text}\n")


def print_step(text):
    """Print an indented step message."""
    print(f"  {text}")


def print_section(title, width=64):
    """Print a section divider demarcating a major phase of `up`/`down`.

    Used to clearly separate control-plane setup from a named environment's
    own deploy, which otherwise read as one undifferentiated stream of steps.
    """
    label = f" {title} "
    left = "\u2500\u2500"
    right = "\u2500" * max(2, width - len(left) - len(label))
    print(f"\n{left}{label}{right}")


class _SpinnerStep:
    """One terse status line for a step: spinner while it runs, then a
    checkmark or cross. Falls back to plain (unanimated) start/result lines
    when stdout isn't a TTY or the caller passed verbose=True, so piped/CI
    output and `--verbose` both stay readable line-by-line.
    """

    def __init__(self, text, animate):
        self._text = text
        self._animate = animate
        self._settled = False
        self._sp = None

    def __enter__(self):
        if self._animate:
            self._sp = yaspin.yaspin(text=self._text, color="cyan")
            self._sp.start()
        else:
            print(f"  {self._text}")
        return self

    def ok(self, text=None):
        self._settled = True
        message = text or self._text
        if self._sp is not None:
            self._sp.text = message
            self._sp.ok("\u2714")
        else:
            print(f"  \u2714 {message}")

    def fail(self, text=None):
        self._settled = True
        message = text or self._text
        if self._sp is not None:
            self._sp.text = message
            self._sp.fail("\u2717")
        else:
            print(f"  \u2717 {message}")

    def __exit__(self, exc_type, exc_value, traceback):
        if not self._settled:
            # An exception propagated without the caller settling the step
            # explicitly -- never leave a spinner hanging.
            self.fail()
        return False


def spinner_step(text, *, verbose=False):
    """Context manager for one terse, animated step line.

    Usage::

        with spinner_step("Waiting for Floci...") as step:
            wait_for_floci()
            step.ok()

    Animates via yaspin when stdout is a TTY and ``verbose`` is False;
    otherwise prints plain, unanimated start/result lines.
    """
    animate = sys.stdout.isatty() and not verbose
    return _SpinnerStep(text, animate)


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
        # Rewrite internal proxy/gateway URLs to localhost
        for internal_host in ("hmd_proxy", "hmd_gateway"):
            if internal_host in url:
                path = url.split(internal_host)[-1]
                gateway_services.append((name, f"http://localhost{path}"))
                break
        else:
            if url.startswith("http://localhost"):
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
        print("  Services (via API Gateway at http://localhost):")
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
