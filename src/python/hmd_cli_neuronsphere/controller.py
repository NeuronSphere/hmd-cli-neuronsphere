import os
from importlib.metadata import version

from cement import Controller, ex
from hmd_cli_tools import get_version
from hmd_cli_tools.hmd_cli_tools import load_hmd_env, set_hmd_env
from hmd_cli_tools.prompt_tools import prompt_for_values

VERSION_BANNER = """
hmd neuronsphere version: {}
"""

VERSION = version("hmd_cli_neuronsphere")


CONFIG_VALUES = {
    "HMD_LOCAL_NS_CONTAINER_REGISTRY": {
        "hidden": True,
        "default": "ghcr.io/neuronsphere",
    }
}


class LocalController(Controller):
    class Meta:
        label = "neuronsphere"

        stacked_type = "nested"
        stacked_on = "base"

        # text displayed at the top of --help output
        description = "Local NeuronSphere Control CLI"

        arguments = (
            (
                ["-v", "--version"],
                {
                    "help": "Display the version of the  command.",
                    "action": "version",
                    "version": VERSION_BANNER.format(VERSION),
                },
            ),
        )

    def _default(self):
        """Default action if no sub-command is passed."""
        self._parser.print_help()

    @ex(
        help="Start the local NeuronSphere",
        arguments=[
            (
                ["--verbose", "-V"],
                {
                    "help": "Show full Docker Compose output",
                    "action": "store_true",
                    "dest": "verbose",
                },
            ),
        ],
    )
    def up(self):
        from .hmd_cli_neuronsphere import start_neuronsphere

        start_neuronsphere(verbose=self.app.pargs.verbose)

    @ex(
        help="Stop the local NeuronSphere",
        arguments=[
            (
                ["--verbose", "-V"],
                {
                    "help": "Show full Docker Compose output",
                    "action": "store_true",
                    "dest": "verbose",
                },
            ),
        ],
    )
    def down(self):
        from .hmd_cli_neuronsphere import stop_neuronsphere

        stop_neuronsphere(verbose=self.app.pargs.verbose)

    @ex(
        help="Restart the local NeuronSphere",
        arguments=[
            (
                ["-s", "--service_name"],
                {
                    "help": "name of service to restart",
                    "action": "store",
                    "dest": "service_name",
                    "nargs": "*",
                },
            )
        ],
    )
    def restart(self):
        from .hmd_cli_neuronsphere import restart_service

        restart_service(self.app.pargs.service_name)

    @ex(
        help="Run a NeuronSphere microservice locally",
        arguments=[
            (
                ["instance_name"],
                {
                    "help": "name to assign service",
                    "action": "store",
                },
            ),
            (
                ["-mnt", "--mount"],
                {
                    "help": "local Python packages to mount into the container",
                    "action": "store",
                    "dest": "mounts",
                    "required": False,
                    "nargs": "*",
                    "default": [],
                },
            ),
        ],
    )
    def run(self):
        from .hmd_cli_neuronsphere import run_local_service

        run_local_service(
            self.app.pargs.repo_name,
            self.app.pargs.repo_version,
            self.app.pargs.instance_name,
            mount_packages=self.app.pargs.mounts,
        )

    @ex(help="pulls latest versions of required images")
    def update_images(self):
        from .hmd_cli_neuronsphere import update_images

        update_images()

    @ex(
        help="Copy an artifact from a cloud artifact librarian into the local one",
        arguments=[
            (
                ["--repo"],
                {
                    "help": "Repo name (e.g. hmd-config-transform-tests)",
                    "action": "store",
                    "dest": "repo",
                    "required": True,
                },
            ),
            (
                ["--version"],
                {
                    "help": "Repo version (e.g. 0.1.46)",
                    "action": "store",
                    "dest": "version",
                    "required": True,
                },
            ),
            (
                ["--cloud-customer"],
                {
                    "help": "Cloud customer_code (used to resolve the cloud librarian URL)",
                    "action": "store",
                    "dest": "cloud_customer",
                    "required": False,
                },
            ),
            (
                ["--cloud-region"],
                {
                    "help": "Cloud region (used to resolve the cloud librarian URL)",
                    "action": "store",
                    "dest": "cloud_region",
                    "required": False,
                },
            ),
            (
                ["--cloud-url"],
                {
                    "help": "Explicit cloud artifact librarian URL (overrides --cloud-customer/--cloud-region)",
                    "action": "store",
                    "dest": "cloud_url",
                    "required": False,
                },
            ),
            (
                ["--artifact-type"],
                {
                    "help": "Content item type (default: build)",
                    "action": "store",
                    "dest": "artifact_type",
                    "default": "build",
                },
            ),
            (
                ["--local-url"],
                {
                    "help": "Local artifact librarian URL (default: http://localhost/hmd_ms_artifact_lib/)",
                    "action": "store",
                    "dest": "local_url",
                    "default": "http://localhost/hmd_ms_artifact_lib/",
                },
            ),
        ],
    )
    def pull_artifact(self):
        from .hmd_cli_neuronsphere import pull_artifact

        pull_artifact(
            repo_name=self.app.pargs.repo,
            version=self.app.pargs.version,
            cloud_customer=self.app.pargs.cloud_customer,
            cloud_region=self.app.pargs.cloud_region,
            cloud_url=self.app.pargs.cloud_url,
            artifact_type=self.app.pargs.artifact_type,
            local_url=self.app.pargs.local_url,
        )

    @ex(
        help="Register a local repo's build artifact in the local artifact librarian",
        arguments=[
            (
                ["--name"],
                {
                    "help": "Repo name (default: manifest.json `name` from --repo-path)",
                    "action": "store",
                    "dest": "repo",
                    "required": False,
                    "default": None,
                },
            ),
            (
                ["--version"],
                {
                    "help": "Version to register the artifact under (default: meta-data/VERSION)",
                    "action": "store",
                    "dest": "version",
                    "required": False,
                    "default": None,
                },
            ),
            (
                ["--repo-path"],
                {
                    "help": "Path to the repo to build (default: cwd)",
                    "action": "store",
                    "dest": "repo_path",
                    "required": False,
                    "default": None,
                },
            ),
            (
                ["--build-path"],
                {
                    "help": "Path to a pre-built build directory; skips running hmd build",
                    "action": "store",
                    "dest": "build_path",
                    "required": False,
                    "default": None,
                },
            ),
            (
                ["--artifact-type"],
                {
                    "help": "Content item type (default: build)",
                    "action": "store",
                    "dest": "artifact_type",
                    "default": "build",
                },
            ),
            (
                ["--local-url"],
                {
                    "help": "Local artifact librarian URL (default: http://localhost/hmd_ms_artifact_lib/)",
                    "action": "store",
                    "dest": "local_url",
                    "default": "http://localhost/hmd_ms_artifact_lib/",
                },
            ),
        ],
    )
    def push_artifact(self):
        from .hmd_cli_neuronsphere import push_artifact

        push_artifact(
            repo=self.app.pargs.repo,
            version=self.app.pargs.version,
            repo_path=self.app.pargs.repo_path,
            build_path=self.app.pargs.build_path,
            artifact_type=self.app.pargs.artifact_type,
            local_url=self.app.pargs.local_url,
        )

    @ex(
        help="Show registered HMDMS services and mocked dependencies",
        arguments=[
            (
                ["--json"],
                {
                    "help": "Emit JSON instead of text",
                    "action": "store_true",
                    "dest": "json_mode",
                },
            ),
        ],
    )
    def status(self):
        from .hmd_cli_neuronsphere import print_status

        print_status(json_mode=self.app.pargs.json_mode)

    @ex(
        help="Configure local NeuronSphere plugins and settings",
        arguments=[],
    )
    def configure(self):
        from importlib_metadata import entry_points as get_entry_points

        from InquirerPy import inquirer

        from .loaders import LocalPluginLoader

        load_hmd_env()

        # --- Discover all plugins ---
        choices = []
        plugin_env_vars = {}  # plugin_name -> env_var_name

        # Bundled plugins (from entry points, skip "main")
        from .plugins.base import load_nsplugin_config

        bundled_eps = get_entry_points(group="hmd_cli_neuronsphere.enabled")
        for ep in sorted(bundled_eps, key=lambda e: e.name):
            if ep.name == "main":
                continue
            env_var = f"HMD_LOCAL_NEURONSPHERE_ENABLE_{ep.name.upper()}"
            env_val = os.environ.get(env_var)
            if env_val is not None:
                enabled = env_val.lower() == "true"
            else:
                bundled_config = load_nsplugin_config(ep.name)
                enabled = (bundled_config or {}).get("enabled_by_default", True)
            choices.append({"name": ep.name, "value": ep.name, "enabled": enabled})
            plugin_env_vars[ep.name] = env_var

        # Local plugins (from LocalPluginLoader)
        local_loader = LocalPluginLoader()
        discovered = local_loader.discover_plugins()
        for name in sorted(discovered.keys()):
            if name in plugin_env_vars:
                continue  # Already covered by bundled
            config = local_loader.load_raw_plugin_config(name)
            env_var = (config or {}).get(
                "env_var_override",
                f"HMD_LOCAL_NEURONSPHERE_ENABLE_{name.upper().replace('-', '_')}",
            )
            env_val = os.environ.get(env_var)
            if env_val is not None:
                enabled = env_val.lower() == "true"
            else:
                enabled = (config or {}).get("enabled_by_default", False)
            choices.append(
                {
                    "name": f"{name} (local)",
                    "value": name,
                    "enabled": enabled,
                }
            )
            plugin_env_vars[name] = env_var

        # --- Prompt ---
        if choices:
            selected = inquirer.checkbox(
                message="Select plugins to enable:",
                choices=choices,
            ).execute()
            selected_set = set(selected)

            # Persist each plugin's enable/disable state
            for name, env_var in plugin_env_vars.items():
                value = "true" if name in selected_set else "false"
                set_hmd_env(env_var, value)

            # Summary
            enabled_names = sorted(n for n in plugin_env_vars if n in selected_set)
            disabled_names = sorted(n for n in plugin_env_vars if n not in selected_set)
            print(f"\nEnabled:  {', '.join(enabled_names) or '(none)'}")
            print(f"Disabled: {', '.join(disabled_names) or '(none)'}")
        else:
            print("No configurable plugins found.")

        # --- Other config (container registry default) ---
        results = prompt_for_values(CONFIG_VALUES)
        for k, v in results.items():
            set_hmd_env(k, str(v))

        print("\nConfiguration saved to $HMD_HOME/.config/hmd.env")

    @ex(
        help="Validate a local plugin configuration (nsplugin.json)",
        arguments=[
            (
                ["path"],
                {
                    "help": "Path to nsplugin.json file or directory containing it",
                    "action": "store",
                    "nargs": "?",
                    "default": ".",
                },
            ),
            (
                ["--no-file-check"],
                {
                    "help": "Skip checking if referenced files exist",
                    "action": "store_true",
                    "dest": "no_file_check",
                },
            ),
        ],
    )
    def validate_plugin(self):
        from pathlib import Path

        from .validators import validate_nsplugin

        path = Path(self.app.pargs.path)
        check_files = not self.app.pargs.no_file_check

        result = validate_nsplugin(path, check_files=check_files)

        if result.errors:
            print("Errors:")
            for error in result.errors:
                print(f"  - {error}")

        if result.warnings:
            print("Warnings:")
            for warning in result.warnings:
                print(f"  - {warning}")

        if result.valid:
            if not result.warnings:
                print("Plugin configuration is valid.")
            else:
                print("\nPlugin configuration is valid with warnings.")
            return

        print("\nPlugin configuration is INVALID.")
        raise SystemExit(1)

    @ex(
        help="Initialize a local plugin directory structure (src/local/)",
        arguments=[
            (
                ["--path"],
                {
                    "help": "Base path for the repository (defaults to current directory)",
                    "action": "store",
                    "dest": "path",
                    "default": ".",
                },
            ),
            (
                ["--plugin-name"],
                {
                    "help": "Name of the plugin (e.g., 'transform', 'trino')",
                    "action": "store",
                    "dest": "plugin_name",
                    "required": False,
                },
            ),
        ],
    )
    def init_plugin(self):
        import json
        from pathlib import Path

        base_path = Path(self.app.pargs.path)
        plugin_name = self.app.pargs.plugin_name

        # Try to infer plugin name from manifest.json if not provided
        if not plugin_name:
            manifest_path = base_path / "meta-data" / "manifest.json"
            if manifest_path.exists():
                try:
                    with open(manifest_path, "r") as f:
                        manifest = json.load(f)
                    repo_name = manifest.get("name", "")
                    # Extract plugin name from repo name (e.g., hmd-ms-transform -> transform)
                    parts = repo_name.split("-")
                    if len(parts) >= 3:
                        plugin_name = "-".join(parts[2:])
                    else:
                        plugin_name = repo_name
                except (json.JSONDecodeError, IOError):
                    pass

        if not plugin_name:
            print("Error: Could not determine plugin name.")
            print(
                "Please provide --plugin-name or run from a repo with meta-data/manifest.json"
            )
            raise SystemExit(1)

        # Create directory structure
        local_dir = base_path / "src" / "local"
        dirs_to_create = [
            local_dir,
            local_dir / "config",
            local_dir / "templates",
            local_dir / "scripts" / "postgres",
            local_dir / "scripts" / "minio",
            local_dir / "scripts" / "dynamodb",
        ]

        for dir_path in dirs_to_create:
            if not dir_path.exists():
                dir_path.mkdir(parents=True, exist_ok=True)
                print(f"Created: {dir_path}")

        # Create template nsplugin.json
        nsplugin_path = local_dir / "nsplugin.json"
        if nsplugin_path.exists():
            print(f"Warning: {nsplugin_path} already exists, skipping")
        else:
            service_name = f"hmd_ms_{plugin_name.replace('-', '_')}"
            template = {
                "plugin_name": plugin_name,
                "compose_file": f"docker-compose.{plugin_name}.yml",
                "resources": {
                    "services": [],
                    "databases": [],
                    "endpoints": [],
                    "buckets": [],
                },
                "required_dirs": [],
                "config_mappings": [],
                "templates": [],
                "postgres_scripts": [],
                "minio_scripts": [],
                "dynamodb_scripts": [],
                "dependencies": {
                    "requires_plugins": [],
                    "requires_services": [],
                },
                "config": {
                    "SERVICE_CONFIG": {
                        "default": {
                            "operations_modules": [f"{service_name}.{service_name}"],
                        },
                        "env_var": "SERVICE_CONFIG",
                        "type": "json",
                    },
                },
                "env_var_override": f"HMD_LOCAL_NEURONSPHERE_ENABLE_{plugin_name.upper().replace('-', '_')}",
                "enabled_by_default": False,
            }

            with open(nsplugin_path, "w") as f:
                json.dump(template, f, indent=2)
            print(f"Created: {nsplugin_path}")

        # Create empty docker-compose file
        compose_path = local_dir / f"docker-compose.{plugin_name}.yml"
        if compose_path.exists():
            print(f"Warning: {compose_path} already exists, skipping")
        else:
            service_key = plugin_name.replace("-", "_")
            compose_template = f"""services:
  {service_key}:
    image: ${{HMD_LOCAL_NS_CONTAINER_REGISTRY:-ghcr.io/neuronsphere}}/hmd-ms-{plugin_name}:${{HMD_IMG_{plugin_name.upper().replace('-', '_')}_VERSION:-stable}}
    container_name: {plugin_name}
    networks:
      - neuronsphere_default
    environment:
      HMD_CUSTOMER_CODE: ${{HMD_CUSTOMER_CODE}}
      HMD_DID: ${{HMD_DID:-aaa}}
      HMD_ENVIRONMENT: ${{HMD_ENVIRONMENT:-local}}
      HMD_REGION: ${{HMD_REGION:-us-west-2}}
      # Configurable via meta-data/config_local.json
      SERVICE_CONFIG: ${{SERVICE_CONFIG:-'{{}}'}}
    # volumes:
    #   - ${{HMD_HOME}}/data:/data
    # depends_on:
    #   db:
    #     condition: service_healthy

networks:
  neuronsphere_default:
    external: true
"""
            with open(compose_path, "w") as f:
                f.write(compose_template)
            print(f"Created: {compose_path}")

        print(f"\nPlugin '{plugin_name}' initialized successfully!")
        print(f"\nNext steps:")
        print(f"  1. Edit {nsplugin_path} to configure resources and dependencies")
        print(f"  2. Edit {compose_path} to configure your service")
        print(f"  3. Run 'hmd neuronsphere validate-plugin {local_dir}' to validate")

    @ex(
        help="List discovered local plugins",
        arguments=[
            (
                ["--verbose", "-v"],
                {
                    "help": "Show detailed plugin information",
                    "action": "store_true",
                    "dest": "verbose",
                },
            ),
        ],
    )
    def list_local_plugins(self):
        """List local plugins discovered from HMD_LOCAL_PLUGINS or HMD_REPO_HOME."""
        import os

        from .loaders import LocalPluginLoader

        loader = LocalPluginLoader()
        discovered = loader.discover_plugins()

        # Show configuration
        print("Local Plugin Configuration:")
        print(
            f"  HMD_LOCAL_PLUGINS: {os.environ.get('HMD_LOCAL_PLUGINS', '(not set)')}"
        )
        print(
            f"  HMD_LOCAL_PLUGINS_SCAN_REPO_HOME: {os.environ.get('HMD_LOCAL_PLUGINS_SCAN_REPO_HOME', '(not set)')}"
        )
        print(f"  HMD_REPO_HOME: {os.environ.get('HMD_REPO_HOME', '(not set)')}")
        print()

        if not discovered:
            print("No local plugins discovered.")
            print()
            print("To use local plugins, set one of:")
            print("  export HMD_LOCAL_PLUGINS=/path/to/repo1:/path/to/repo2")
            print("  export HMD_LOCAL_PLUGINS_SCAN_REPO_HOME=true")
            return

        print(f"Discovered {len(discovered)} local plugin(s):")
        print()

        for name, info in sorted(discovered.items()):
            enabled = loader.is_plugin_enabled(name)
            explicit = loader._is_explicitly_listed(name)
            status = "enabled" if enabled else "disabled"
            source = "explicit" if explicit else "scanned"

            print(f"  {name}:")
            print(f"    Status: {status} ({source})")
            print(f"    Repo: {info.repo_path}")

            if self.app.pargs.verbose:
                print(f"    Local dir: {info.local_dir}")
                print(f"    Config: {info.config_path}")

                # Show compose file
                compose_path = loader.get_compose_path(name)
                if compose_path:
                    print(f"    Compose: {compose_path}")

                # Show env var for enabling (if scanned)
                if not explicit:
                    env_var = f"HMD_LOCAL_NEURONSPHERE_ENABLE_{name.upper().replace('-', '_')}"
                    print(f"    Enable via: export {env_var}=true")

            print()
