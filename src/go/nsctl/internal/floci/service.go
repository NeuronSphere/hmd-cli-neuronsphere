package floci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tools"
)

// ImagePresence reports whether an image reference is in the local cache.
type ImagePresence interface {
	ImagePresent(ctx context.Context, ref string) bool
}

// ServiceImage finds the image to run a foundation service from.
//
// Local only, deliberately. Floci spawns Lambda containers through the mounted
// host docker.sock, so any image in the host's cache is usable directly, and a
// registry round trip would make a start depend on the network.
func ServiceImage(ctx context.Context, d ImagePresence, repoClass, version string, lookup func(string) string) (string, error) {
	var candidates []string
	for _, key := range []string{"HMD_CONTAINER_REGISTRY", "HMD_LOCAL_NS_CONTAINER_REGISTRY"} {
		if prefix := lookup(key); prefix != "" {
			candidates = append(candidates, prefix+"/"+repoClass+":"+version)
		}
	}
	candidates = append(candidates, repoClass+":"+version)

	for _, ref := range candidates {
		if d.ImagePresent(ctx, ref) {
			return ref, nil
		}
	}
	return "", fmt.Errorf(
		"no local image for %s@%s (looked for %v). Build it with `hmd build` in that repo, or set %s to a version you have",
		repoClass, version, candidates, repoclass.VersionEnvVar(repoClass))
}

// ServiceConfig is the SERVICE_CONFIG a foundation service runs with.
//
// Read from the RepoClass, which is where it is declared: manifest.json's
// deploy.default_configuration.service_config, overlaid with
// meta-data/config_local.json's service_config. That is the same pair
// LocalPluginLoader merges, and it is why nsctl needs no table of its own --
// hmd-ms-naming, hmd-ms-artifact-lib and hmd-ms-deployment each declare their
// loader_config, operations_modules and database engines in their own
// manifest.
//
// Note this is only config_local.json's `service_config` key, not the file's
// top level. Reading the top level yields a configuration with no
// operations_modules, and the service then starts and fails every request with
// a KeyError.
//
// Absent yields nil, and the caller decides what that means.
func ServiceConfig(repoDir string) map[string]any {
	if repoDir == "" {
		return nil
	}
	config := map[string]any{}
	for _, source := range []struct{ path, key string }{
		{filepath.Join(repoDir, "meta-data", "manifest.json"), "deploy.default_configuration.service_config"},
		{filepath.Join(repoDir, "meta-data", "config_local.json"), "service_config"},
	} {
		for k, v := range serviceConfigFrom(source.path, source.key) {
			config[k] = v
		}
	}
	if len(config) == 0 {
		return nil
	}
	return config
}

// copyEngineConfig copies an engine's engine_config so a declared value can be
// kept while the rest is filled in.
func copyEngineConfig(engine map[string]any) map[string]any {
	out := map[string]any{}
	if existing, ok := engine["engine_config"].(map[string]any); ok {
		for k, v := range existing {
			out[k] = v
		}
	}
	return out
}

// setDefault fills a key only when the declaration left it out, so a repo that
// states a value keeps it.
func setDefault(m map[string]any, key string, value any) {
	if _, present := m[key]; !present {
		m[key] = value
	}
}

// serviceConfigFrom reads a dotted key path out of a JSON file. A missing
// file, a malformed one, or an absent key all yield nothing: a repo that
// declares no service configuration is the ordinary case.
func serviceConfigFrom(path, keyPath string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	current := doc
	for _, key := range strings.Split(keyPath, ".") {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

// PostgresMajor is the PostgreSQL major version of the image Floci is
// configured to spawn database backends from.
//
// Read rather than hardcoded so the version an instance declares and the
// binary that initialises its data directory cannot drift apart -- the drift
// that leaves a data directory a newer server refuses to start against.
//
// Returns "" when it cannot be determined, which a caller should treat as "do
// not declare one" rather than as a default.
func PostgresMajor(ctx context.Context, d ImageChecker, container string) string {
	return MajorFromImageRef(d.ContainerEnv(ctx, container)[rdsImageEnv])
}

// MajorFromImageRef reads the major version out of an image tag.
//
// hmd-postgres-base tags look like `<registry>/hmd-postgres-base:0.2.11`, and
// the leading component of the version is not the PostgreSQL major -- so a tag
// is only usable when it carries one explicitly, as `pg16` or `postgres-16`.
// Anything else yields "", because guessing here produces an engine_version
// that disagrees with the running binary.
func MajorFromImageRef(ref string) string {
	if ref == "" {
		return ""
	}
	tag := ref
	if at := strings.LastIndex(ref, ":"); at > strings.LastIndex(ref, "/") {
		tag = ref[at+1:]
	}
	for _, prefix := range []string{"pg", "postgres-", "postgresql-"} {
		if rest, found := strings.CutPrefix(tag, prefix); found {
			if major := leadingDigits(rest); major != "" {
				return major
			}
		}
	}
	return ""
}

func leadingDigits(s string) string {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	if _, err := strconv.Atoi(s[:end]); err != nil {
		return ""
	}
	return s[:end]
}

// LocalizeServiceConfig resolves a service_config's cloud references to what
// the local platform actually provides.
//
// A RepoClass declares its database engine the way the cloud wires it: the
// credentials come from a Secrets Manager secret named by a dependency
// (`dependency:db-credentials`) and connections go through a pgbouncer proxy
// (`dependency:pgbouncer`). Locally there is neither. The database is reached
// directly on the Docker network, and the local convention is that a service's
// database, user and password are all its own name -- see the local database
// password note in the deployer.
//
// Left unresolved, the service starts and then fails every request with
// "Secret dependency:db-credentials not found in PS or SM".
//
// This is the same resolution LocalPluginLoader performs, done here so the
// declaration stays in the RepoClass rather than being duplicated into a table
// of hardcoded configurations.
func LocalizeServiceConfig(config map[string]any, dbHost, dbName, graphHost string,
	repoClass string, names Names) map[string]any {
	if config == nil {
		return nil
	}
	engines, ok := config["hmd_db_engines"].(map[string]any)
	if !ok {
		return config
	}

	// Copied rather than mutated: the caller's map may be the resolver's
	// cached read of the manifest, and localizing it in place would leak one
	// service's connection details into the next.
	out := make(map[string]any, len(config))
	for k, v := range config {
		out[k] = v
	}
	localized := make(map[string]any, len(engines))
	for name, raw := range engines {
		engine, ok := raw.(map[string]any)
		if !ok {
			localized[name] = raw
			continue
		}
		copied := make(map[string]any, len(engine))
		for k, v := range engine {
			copied[k] = v
		}
		switch engine["engine_type"] {
		case "postgres":
			copied["engine_config"] = map[string]any{
				"host":     dbHost,
				"user":     dbName,
				"password": dbName,
				"db_name":  dbName,
			}
		case "gremlin":
			engineConfig := copyEngineConfig(engine)
			if host, _ := engineConfig["db_host"].(string); graphHost != "" && strings.HasPrefix(host, "dependency:") {
				engineConfig["db_host"] = graphHost
			}
			// The cloud reaches Neptune over wss with query strategies; the
			// local gremlin-server does neither.
			setDefault(engineConfig, "db_protocol", "ws")
			setDefault(engineConfig, "with_strategies", false)
			copied["engine_config"] = engineConfig
		case "dynamo":
			// The cloud takes the table name from the table CDKTF creates,
			// which is the service's standard name. Locally the name is the
			// same and Floci's DynamoDB is initialised against it.
			engineConfig := copyEngineConfig(engine)
			setDefault(engineConfig, "dynamo_table", tools.MakeStandardName(
				LambdaName(repoClass), repoClass, names.DeploymentID, "local",
				names.Region, names.CustomerCode))
			copied["engine_config"] = engineConfig
		}
		localized[name] = copied
	}
	out["hmd_db_engines"] = localized
	return out
}

// ServiceParameters are the extra environment variables a RepoClass's
// deploy.default_configuration declares, beyond its service_config.
//
// hmd-lib-cdktf-factories' LibrarianBase exports these at cloud deploy time,
// so a librarian-style service reads them with get_service_parameter and
// raises "Environment variable, X, not populated" without them. Both are
// declared in the repo's own manifest, so reading them keeps the declaration
// with the RepoClass.
func ServiceParameters(repoDir string) map[string]string {
	if repoDir == "" {
		return nil
	}
	manifest := filepath.Join(repoDir, "meta-data", "manifest.json")
	local := filepath.Join(repoDir, "meta-data", "config_local.json")

	out := map[string]string{}
	for _, p := range []struct{ key, envVar string }{
		{"content_path_configs", "CONTENT_PATH_CONFIGS"},
		{"graph_queries", "GRAPH_QUERY_CONFIG"},
	} {
		value := jsonValueAt(local, p.key)
		if value == nil {
			value = jsonValueAt(manifest, "deploy.default_configuration."+p.key)
		}
		if value == nil {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		out[p.envVar] = string(encoded)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LibrarianStyle reports whether a service reads librarian parameters, which
// is what makes a missing bucket declaration fatal to it rather than merely
// absent.
func LibrarianStyle(repoDir string) bool {
	return jsonValueAt(filepath.Join(repoDir, "meta-data", "manifest.json"),
		"deploy.default_configuration.content_path_configs") != nil
}

// LibrarianBucketRepoClass is what a librarian's bucket dependency resolves to.
const LibrarianBucketRepoClass = "hmd-inf-s3bucket"

// LibrarianBucketDependency is the key the cloud reads that dependency under.
//
// hmd_lib_cdktf_factories.librarian_base composes the bucket name from
// "lib-repo.instance_name", "lib-repo.repo_name" and "lib-repo.deployment_id",
// so the key is part of the contract, not a local convention.
const LibrarianBucketDependency = "lib-repo"

// LibrarianBucketName is the bucket a librarian's BUCKET_NAME must point at,
// derived exactly as the cloud derives it.
//
// SPEC014 recorded this gap and prescribed the wrong fix: a literal BUCKET_NAME
// under the repo's deploy.default_configuration, in another repository, which
// "nothing in this one can close". Both halves were wrong. In the cloud the
// name is never declared -- it is *derived*, by
// hmd_lib_cdktf_factories.librarian_base.get_full_bucket_name:
//
//	f"{lib-repo.instance_name}-{lib-repo.repo_name}-{lib-repo.deployment_id}-"
//	f"{environment}-{hmd_region}-{customer_code}"
//
// from the librarian's own required `lib-repo` dependency on hmd-inf-s3bucket.
// A literal in default_configuration would have pinned a name the cloud
// computes, and drifted from the bucket hmd-inf-s3bucket actually creates --
// whose local stack names it self.base_name.replace("_", "-"), i.e.
// make_standard_name of the same six parts.
//
// Derived through tools.ResourceIdentifier, which is make_standard_name, so
// this matches the *producer*. The two formulas agree below 64 characters and
// diverge above it, where make_standard_name shortens and get_full_bucket_name
// does not -- a pre-existing inconsistency in the cloud. Matching the repo that
// creates the bucket is the side worth being on.
//
// The instance name is the dependency's own key. In the cloud a BOM entry names
// the s3bucket instance; the control plane's librarian is deployed before
// ms-deployment exists, so there is no BOM entry and no instance to read. Using
// the dependency's key invents the least: it is the name the RepoClass already
// gives that dependency.
//
// Returns "" when the repo is not a librarian, or declares no such dependency --
// in which case nothing has been guessed and the caller still warns.
func LibrarianBucketName(repoDir, deploymentID string, names Names) string {
	if !LibrarianStyle(repoDir) {
		return ""
	}
	deps, _ := jsonValueAt(filepath.Join(repoDir, "meta-data", "manifest.json"),
		"deploy.dependencies").(map[string]any)
	instance := librarianBucketInstance(deps)
	if instance == "" {
		return ""
	}
	return tools.ResourceIdentifier(instance, LibrarianBucketRepoClass, deploymentID,
		"local", names.Region, names.CustomerCode)
}

// librarianBucketInstance is the instance name to derive the bucket from, or ""
// when the repo declares no bucket dependency.
//
// The declared key wins; failing that, any dependency resolving to
// hmd-inf-s3bucket. The fallback is not defensive padding -- a repo free to name
// its dependency something other than `lib-repo` would otherwise silently get
// no bucket, which is the failure mode this whole function exists to end.
func librarianBucketInstance(deps map[string]any) string {
	if deps == nil {
		return ""
	}
	if dep, ok := deps[LibrarianBucketDependency].(map[string]any); ok {
		return librarianInstanceFor(dep, LibrarianBucketDependency)
	}
	for _, key := range sortedDepKeys(deps) {
		dep, ok := deps[key].(map[string]any)
		if !ok {
			continue
		}
		if name, _ := dep["repo_class_name"].(string); name == LibrarianBucketRepoClass {
			return librarianInstanceFor(dep, key)
		}
	}
	return ""
}

// librarianInstanceFor prefers an explicitly declared instance_name, which is
// what a cloud BOM supplies, and falls back to the dependency's key.
func librarianInstanceFor(dep map[string]any, key string) string {
	if name, _ := dep["instance_name"].(string); name != "" {
		return name
	}
	return key
}

// sortedDepKeys keeps the fallback deterministic: two dependencies on
// hmd-inf-s3bucket must not name a different bucket on different runs.
func sortedDepKeys(deps map[string]any) []string {
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonValueAt reads a dotted key path out of a JSON file, returning nil when
// the file, the path or the JSON itself is absent.
func jsonValueAt(path, keyPath string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	current := doc
	for _, key := range strings.Split(keyPath, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		value, present := m[key]
		if !present {
			return nil
		}
		current = value
	}
	return current
}
