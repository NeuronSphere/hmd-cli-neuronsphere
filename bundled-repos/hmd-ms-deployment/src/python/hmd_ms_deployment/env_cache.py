"""Redis cache for the per-environment dependency graph (NERD0005).

``EnvironmentInformation.load_environment()`` is the single hot path for every
dependency lookup, DAG build, and BOM/diagram render: it issues two large native
SQL queries against Postgres and populates the in-memory ``nouns`` /
``relationships`` dictionaries that all downstream code reads. This module caches
that graph in Redis so repeated loads reuse a cached graph instead of re-hitting
Postgres.

The graph is split into two layers whose membership maps directly to the CTEs in
``environment_query.py``:

* **topology** (long TTL) -- ``repo_class`` / ``repo_class_version`` /
  ``repo_instance`` / ``environment`` nouns and the class/version/instance
  dependency edges. This is what the DAG builder traverses and it changes only on
  structural edits (new versions / instances / dependency wiring).
* **status** (short TTL) -- the current-or-NEXT ``repo_instance_deployment`` nouns
  and the ``repo_instance_has_repo_instance_deployment`` (``current`` pointer) and
  ``repo_instance_deployment_has_repo_class_version`` edges. This is what a
  deployment's status callbacks flip on every step.

Caching the layers separately is the load-bearing idea: a busy deployment updates
only the small ``status`` key (write-through, in place) while the expensive
``topology`` key survives untouched, so the cache never churns during a deployment.

Everything is **fail-open**: any Redis error, timeout, or miss falls back to the
Postgres path, so caching can never break or block a deployment. The feature is
gated by ``HMD_ENV_CACHE_ENABLED`` and defaults off.

Connection handling mirrors ``hmd_ms_transform.redis_cache`` (external neuronsphere
domain host + credentials from Secrets Manager), which is the proven path for
reaching the shared ``hmd-inf-redis`` deployment from a Lambda runtime.
"""

import os
from collections import defaultdict
from json import dumps, loads
from logging import getLogger
from typing import Dict, List, Optional, Tuple

import boto3
import redis
from boto3 import Session
from hmd_cli_tools.hmd_cli_tools import get_cloud_region, get_service_parameter
from hmd_meta_types import Entity, Noun, Relationship
from hmd_lang_deployment.repo_instance_deployment import RepoInstanceDeployment
from hmd_lang_deployment.repo_instance_deployment_has_repo_class_version import (
    RepoInstanceDeploymentHasRepoClassVersion,
)
from hmd_lang_deployment.repo_instance_has_repo_instance_deployment import (
    RepoInstanceHasRepoInstanceDeployment,
)

logger = getLogger(f"HMD.{__name__}")

# Namespace names of the entities that make up the volatile "status" layer. A
# noun/relationship is topology iff it is NOT one of these.
_RID_NS = "hmd_lang_deployment.repo_instance_deployment"
_RIHRID_NS = "hmd_lang_deployment.repo_instance_has_repo_instance_deployment"
_RIDHRCV_NS = "hmd_lang_deployment.repo_instance_deployment_has_repo_class_version"

VOLATILE_NOUN_TYPES = (RepoInstanceDeployment,)
VOLATILE_REL_TYPES = (
    RepoInstanceHasRepoInstanceDeployment,
    RepoInstanceDeploymentHasRepoClassVersion,
)


def is_volatile(entity) -> bool:
    """True if ``entity`` belongs to the volatile status layer, else topology."""
    return isinstance(entity, VOLATILE_NOUN_TYPES + VOLATILE_REL_TYPES)


def _namespace_name(entity) -> str:
    return Entity.get_namespace_name(type(entity).entity_definition())


def _dump(entities: List) -> str:
    """Serialize a layer to a JSON string of ``{ns, data}`` records.

    ``ns`` carries the hmd namespace name so the loader can rehydrate the exact
    class; ``data`` is the standard hmd serialize form (with ref_to/ref_from for
    relationships). We do not rely on ``serialize(include_schema=True)`` because
    ``Entity.get_namespace_name`` is a staticmethod that ignores the instance.
    """
    return dumps(
        [{"ns": _namespace_name(e), "data": e.serialize(encode_blobs=True)} for e in entities]
    )


def _load(blob: str, loader) -> List:
    """Rehydrate a layer produced by :func:`_dump` into hmd entities."""
    result = []
    for item in loads(blob):
        cls = loader.get_class(item["ns"])
        result.append(Entity.deserialize(cls, item["data"]))
    return result


class EnvCache:
    """Fail-open, store-agnostic Redis cache for the environment graph."""

    KEY_PREFIX = "deploy:cache"
    TOPOLOGY_TTL_S = 14400  # 4 hours -- topology changes rarely
    STATUS_TTL_S = 300  # kept warm by write-through during active deployments

    def __init__(self):
        self._client: Optional[redis.Redis] = None
        self._initialized = False
        self.stats: Dict[str, int] = defaultdict(int)

    # ------------------------------------------------------------------
    # Connection / enablement
    # ------------------------------------------------------------------
    @staticmethod
    def _feature_enabled() -> bool:
        val = get_service_parameter(
            "HMD_ENV_CACHE_ENABLED", default_value="false", throw=False
        )
        return str(val).lower() == "true"

    def _ensure_initialized(self):
        if self._initialized:
            return
        self._initialized = True
        if not self._feature_enabled():
            logger.info("Environment graph cache disabled (HMD_ENV_CACHE_ENABLED).")
            return
        self._initialize_connection()

    def _initialize_connection(self):
        """Mirror hmd_ms_transform.redis_cache: lazy connect, short timeouts,
        credentials from Secrets Manager, disable on any error."""
        try:
            redis_host = get_service_parameter("REDIS_HOST", throw=False)
            redis_port = get_service_parameter("REDIS_PORT", throw=False) or "6379"
            redis_db = int(os.environ.get("REDIS_DB", "0"))
            redis_secret_name = get_service_parameter("REDIS_SECRET", throw=False)
            redis_ssl = os.environ.get("REDIS_SSL", "false").lower() == "true"

            if not redis_host:
                logger.warning("REDIS_HOST not configured. Env graph cache disabled.")
                return

            redis_credentials = {}
            if redis_secret_name:
                session = Session(
                    region_name=get_cloud_region(get_service_parameter("HMD_REGION"))
                )
                sm_client = session.client("secretsmanager")
                try:
                    secret = sm_client.get_secret_value(SecretId=redis_secret_name)[
                        "SecretString"
                    ]
                    redis_credentials = loads(secret)
                except boto3.exceptions.botocore.exceptions.ClientError as ex:
                    if ex.response["Error"]["Code"] == "ResourceNotFoundException":
                        raise Exception(
                            f"Unable to read secret name: {redis_secret_name}"
                        ) from ex
                    raise

            connection_params = {
                "host": redis_host,
                "port": int(redis_port),
                "db": redis_db,
                "decode_responses": True,
                "socket_connect_timeout": 5,
                "socket_timeout": 5,
                "retry_on_timeout": False,
            }
            if redis_credentials.get("username"):
                connection_params["username"] = redis_credentials["username"]
            if redis_credentials.get("password"):
                connection_params["password"] = redis_credentials["password"]
            if redis_ssl:
                connection_params["ssl"] = True

            self._client = redis.Redis(**connection_params)
            self._client.ping()
            logger.info(
                f"Environment graph cache initialized: {redis_host}:{redis_port}/{redis_db}"
            )
        except Exception as e:  # fail open on anything
            logger.error(f"Env graph cache init failed: {e}. Caching disabled.")
            self._client = None

    @property
    def is_enabled(self) -> bool:
        self._ensure_initialized()
        return self._client is not None

    # ------------------------------------------------------------------
    # Keys
    # ------------------------------------------------------------------
    def _key(self, environment_type: str, layer: str) -> str:
        did = os.environ.get("HMD_DID", "")
        region = os.environ.get("HMD_REGION", "")
        return f"{self.KEY_PREFIX}:{did}:{region}:{environment_type}:{layer}"

    def _topology_key(self, environment_type: str) -> str:
        return self._key(environment_type, "topology")

    def _status_key(self, environment_type: str) -> str:
        return self._key(environment_type, "status")

    # ------------------------------------------------------------------
    # Read / write both layers
    # ------------------------------------------------------------------
    def load_layers(
        self, environment_type: str, loader
    ) -> Tuple[Optional[List], Optional[List]]:
        """Return ``(topology_entities, status_entities)`` or ``(None, None)`` on
        any miss/error. Both layers must be present for a hit -- a missing status
        key (short TTL) forces a full reload so the graph is never partially stale.
        """
        if not self.is_enabled:
            return None, None
        try:
            topo_blob = self._client.get(self._topology_key(environment_type))
            status_blob = self._client.get(self._status_key(environment_type))
            if topo_blob is None or status_blob is None:
                self.stats["miss"] += 1
                logger.debug("Env graph cache miss for %s", environment_type)
                return None, None
            topology = _load(topo_blob, loader)
            status = _load(status_blob, loader)
            self.stats["hit"] += 1
            logger.debug(
                "Env graph cache hit for %s (%d topology, %d status)",
                environment_type,
                len(topology),
                len(status),
            )
            return topology, status
        except Exception as e:  # fail open
            self.stats["fallback"] += 1
            logger.warning(f"Env graph cache read error ({e}); falling back to db.")
            return None, None

    def store_layers(
        self,
        environment_type: str,
        topology_entities: List,
        status_entities: List,
    ) -> None:
        """Populate both layers after a Postgres load. No-op / fail-open on error."""
        if not self.is_enabled:
            return
        try:
            self._client.set(
                self._topology_key(environment_type),
                _dump(topology_entities),
                ex=self.TOPOLOGY_TTL_S,
            )
            self._client.set(
                self._status_key(environment_type),
                _dump(status_entities),
                ex=self.STATUS_TTL_S,
            )
            logger.debug(
                "Env graph cache stored for %s (%d topology, %d status)",
                environment_type,
                len(topology_entities),
                len(status_entities),
            )
        except Exception as e:  # fail open
            logger.warning(f"Env graph cache write error ({e}); leaving cache as-is.")

    # ------------------------------------------------------------------
    # Volatile write-through (status callbacks) -- NEVER touches topology
    # ------------------------------------------------------------------
    def splice_status(
        self, environment_type: str, ri_identifier: str, volatile_entities: List
    ) -> None:
        """Replace one RepoInstance's slice of the status layer in place.

        ``volatile_entities`` is the post-update current-or-NEXT set for the
        instance: its ``RepoInstanceDeployment`` nouns plus the
        ``repo_instance_has_repo_instance_deployment`` and
        ``repo_instance_deployment_has_repo_class_version`` relationships that
        anchor them. Only the status key is touched, so a deployment -- no matter
        how many status updates it emits -- never invalidates the topology layer.

        If the status key is absent or the update fails, falls back to
        :meth:`invalidate_status` (the next load rebuilds it), which likewise
        never touches topology.
        """
        if not self.is_enabled:
            return
        try:
            blob = self._client.get(self._status_key(environment_type))
            if blob is None:
                # Nothing to keep warm; next full load will repopulate.
                return
            items = loads(blob)

            # Identify this RI's existing slice: its current-pointer rels give the
            # set of RID ids to drop along with their version links.
            old_rid_ids = {
                it["data"].get("ref_to")
                for it in items
                if it["ns"] == _RIHRID_NS
                and it["data"].get("ref_from") == ri_identifier
            }

            def _keep(it) -> bool:
                ns, data = it["ns"], it["data"]
                if ns == _RIHRID_NS and data.get("ref_from") == ri_identifier:
                    return False
                if ns == _RID_NS and data.get("identifier") in old_rid_ids:
                    return False
                if ns == _RIDHRCV_NS and data.get("ref_from") in old_rid_ids:
                    return False
                return True

            items = [it for it in items if _keep(it)]
            items.extend(
                {"ns": _namespace_name(e), "data": e.serialize(encode_blobs=True)}
                for e in volatile_entities
            )
            self._client.set(
                self._status_key(environment_type),
                dumps(items),
                ex=self.STATUS_TTL_S,
            )
            self.stats["writethrough"] += 1
            logger.debug(
                "Env graph cache status write-through for RI %s (%s)",
                ri_identifier,
                environment_type,
            )
        except Exception as e:  # fail open -- drop volatile, keep topology
            logger.warning(
                f"Env graph cache write-through failed ({e}); invalidating status."
            )
            self.invalidate_status(environment_type)

    # ------------------------------------------------------------------
    # Targeted invalidation (structural changes) -- topology + status only
    # ------------------------------------------------------------------
    def invalidate_topology(self, environment_type: str) -> None:
        self._delete(self._topology_key(environment_type))

    def invalidate_status(self, environment_type: str) -> None:
        self._delete(self._status_key(environment_type))

    def invalidate_environment(self, environment_type: str) -> None:
        """Drop both layers for one environment (structural change to that env)."""
        self.invalidate_topology(environment_type)
        self.invalidate_status(environment_type)

    def invalidate_all_topology(self) -> None:
        """Drop every topology key for this DID/region.

        Used by ``add_repo_class_version``: a new class version is not tied to a
        single environment, so every environment's topology may now be stale.
        The volatile status layer is unaffected by a version registration.
        """
        self._scan_delete(self._key("*", "topology"))

    def invalidate_all(self) -> None:
        """Drop every cache key (both layers, all environments) for this
        DID/region. Used by ``apply_changeset``, which can introduce new
        instances, dependency edges (topology) and DEPLOY_NEXT/DESTROY_NEXT
        deployments (status) across the environments of a deployment set.
        """
        self._scan_delete(f"{self.KEY_PREFIX}:{os.environ.get('HMD_DID', '')}:{os.environ.get('HMD_REGION', '')}:*")

    def _scan_delete(self, pattern: str) -> None:
        if not self.is_enabled:
            return
        try:
            keys = list(self._client.scan_iter(match=pattern))
            if keys:
                self._client.delete(*keys)
            logger.debug("Env graph cache invalidated %d keys (%s)", len(keys), pattern)
        except Exception as e:  # fail open
            logger.warning(f"Env graph cache invalidation failed for {pattern} ({e}).")

    def _delete(self, key: str) -> None:
        if not self.is_enabled:
            return
        try:
            self._client.delete(key)
            logger.debug("Env graph cache deleted %s", key)
        except Exception as e:  # fail open
            logger.warning(f"Env graph cache delete failed for {key} ({e}).")


# Global cache instance (mirror hmd_ms_transform.redis_cache.get_redis_cache)
_env_cache: Optional[EnvCache] = None


def get_env_cache() -> EnvCache:
    """Get or create the process-wide environment graph cache."""
    global _env_cache
    if _env_cache is None:
        _env_cache = EnvCache()
    return _env_cache
