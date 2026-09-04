package floci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
)

// Floci's persisted API Gateway v1 stores. The glob deliberately does not match
// apigatewayv2-*, which persists correctly.
const (
	apigwStoreGlob = "apigateway-*.json"
	apigwAPIStore  = "apigateway-apis.json"
)

// apigwChildKey extracts the REST API id from a child record's key.
var apigwChildKey = regexp.MustCompile(`^.*?::([^:]+)::`)

// GatewayNamePrefix is what setup_service names a gateway: neuronsphere-<service>.
const GatewayNamePrefix = "neuronsphere-"

// DefaultStage is the API Gateway stage every local service is deployed to.
const DefaultStage = "local"

// PruneAPIGatewayGhosts drops the unusable persisted API Gateway v1 records
// before Floci starts, returning how many it removed.
//
// Floci runs with FLOCI_STORAGE_MODE: persistent and has historically written
// v1 entities with every field null -- ids, names, resource paths and stage
// names all lost. It rehydrates those records on start and serves them from
// GET /restapis, so a restart over an existing data dir comes back with
// unusable, *undeletable* (id: null) gateways that accumulate one per gateway
// per start.
//
// This prunes rather than wipes. Deleting every apigateway-*.json would also be
// correct if the only gateways were the ones this CLI creates, but a service
// deployed through the deployment DAG gets a CDKTF-managed gateway that a warm
// restart never recreates -- so wiping the store destroyed it with nothing left
// to rebuild it. The Lambda survived, the gateway did not, and every
// /<env>/<service>/ request fell through to the catch-all.
//
// Best-effort by design: an unreadable, unwritable or missing file must never
// fail a start.
func PruneAPIGatewayGhosts(dataDir string) int {
	stores, err := filepath.Glob(filepath.Join(dataDir, apigwStoreGlob))
	if err != nil || len(stores) == 0 {
		return 0
	}
	sort.Strings(stores)

	apiStore := filepath.Join(dataDir, apigwAPIStore)
	apis := loadJSONDict(apiStore)
	if apis == nil {
		// Without an authoritative API list every child record would look
		// orphaned. Leave the store alone rather than empty it.
		return 0
	}

	live := map[string]bool{}
	kept := map[string]json.RawMessage{}
	for key, record := range apis {
		id, ok := gatewayID(record)
		if !ok {
			continue
		}
		kept[key] = record
		live[id] = true
	}

	removed := len(apis) - len(kept)
	if removed > 0 && !rewriteJSONDict(apiStore, kept) {
		return 0
	}

	// Anything keyed by a REST API that is gone -- whether this call dropped it
	// or a previous run did.
	for _, path := range stores {
		if filepath.Base(path) == apigwAPIStore {
			continue
		}
		records := loadJSONDict(path)
		if len(records) == 0 {
			continue
		}
		keptChildren := map[string]json.RawMessage{}
		for key, value := range records {
			if m := apigwChildKey.FindStringSubmatch(key); m != nil && !live[m[1]] {
				continue
			}
			keptChildren[key] = value
		}
		if dropped := len(records) - len(keptChildren); dropped > 0 && rewriteJSONDict(path, keptChildren) {
			removed += dropped
		}
	}
	return removed
}

// gatewayID reports a persisted record's REST API id, or false when the record
// is a ghost.
//
// Mirrors create_api_gateway's own check: a record with no id or no name can
// neither be reused nor deleted through the API.
func gatewayID(record json.RawMessage) (string, bool) {
	var decoded struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(record, &decoded); err != nil {
		return "", false
	}
	if decoded.ID == "" || decoded.Name == "" {
		return "", false
	}
	return decoded.ID, true
}

func loadJSONDict(path string) map[string]json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func rewriteJSONDict(path string, data map[string]json.RawMessage) bool {
	encoded, err := json.Marshal(data)
	if err != nil {
		return false
	}
	return os.WriteFile(path, encoded, 0o644) == nil
}

// apigatewayAPI narrows the client to the calls used here.
type apigatewayAPI interface {
	GetRestApis(ctx context.Context, in *apigateway.GetRestApisInput, opts ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error)
	CreateDeployment(ctx context.Context, in *apigateway.CreateDeploymentInput, opts ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	GetStages(ctx context.Context, in *apigateway.GetStagesInput, opts ...func(*apigateway.Options)) (*apigateway.GetStagesOutput, error)
	DeleteStage(ctx context.Context, in *apigateway.DeleteStageInput, opts ...func(*apigateway.Options)) (*apigateway.DeleteStageOutput, error)
	CreateStage(ctx context.Context, in *apigateway.CreateStageInput, opts ...func(*apigateway.Options)) (*apigateway.CreateStageOutput, error)
}

// Gateways is the API Gateway half of a Floci account.
type Gateways struct {
	API apigatewayAPI
}

// NewGateways builds a Gateways from a target's AWS config.
func NewGateways(ctx context.Context, t Target) (*Gateways, error) {
	cfg, err := t.Config(ctx)
	if err != nil {
		return nil, err
	}
	return &Gateways{API: apigateway.NewFromConfig(cfg)}, nil
}

// ServiceGateways lists the REST APIs this account owns, keyed by the service
// name they front.
//
// This is how a warm restart recovers the ids the bootstrap DAG would otherwise
// hand it. setup_service names each gateway neuronsphere-<service>, and Floci
// persists real API Gateway records across a restart, so the mapping survives.
// A ghost -- a record with a null id or name -- is skipped, matching what
// create_api_gateway does with one it cannot reuse.
func (g *Gateways) ServiceGateways(ctx context.Context) (map[string]string, error) {
	out, err := g.API.GetRestApis(ctx, &apigateway.GetRestApisInput{Limit: aws.Int32(500)})
	if err != nil {
		return nil, fmt.Errorf("listing API Gateways: %w", err)
	}
	services := map[string]string{}
	for _, api := range out.Items {
		id, name := aws.ToString(api.Id), aws.ToString(api.Name)
		if id == "" || name == "" {
			continue
		}
		if !strings.HasPrefix(name, GatewayNamePrefix) {
			continue
		}
		services[strings.TrimPrefix(name, GatewayNamePrefix)] = id
	}
	return services, nil
}

// DeployStage deploys an API Gateway to a stage.
//
// The stage is deleted and recreated rather than updated: Floci does not
// support update_stage patch operations reliably, and it does not auto-create a
// stage from create_deployment's stageName either.
func (g *Gateways) DeployStage(ctx context.Context, apiID, stage string) error {
	if stage == "" {
		stage = DefaultStage
	}
	deployment, err := g.API.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: aws.String(apiID),
	})
	if err != nil {
		return fmt.Errorf("creating a deployment for %s: %w", apiID, err)
	}
	// A missing stage is the normal first-time case, so the delete is allowed
	// to fail.
	_, _ = g.API.DeleteStage(ctx, &apigateway.DeleteStageInput{
		RestApiId: aws.String(apiID), StageName: aws.String(stage),
	})
	if _, err := g.API.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId:    aws.String(apiID),
		StageName:    aws.String(stage),
		DeploymentId: deployment.Id,
	}); err != nil {
		return fmt.Errorf("creating the %s stage for %s: %w", stage, apiID, err)
	}
	return nil
}

// Gateways this CLI creates itself are named neuronsphere-<service> and are
// already routed by the bulk writers, so discovery skips them rather than
// writing a second, redundant route.
const cliGatewayPrefix = GatewayNamePrefix

// CDKTF names its REST API <instance>_<repo_class>_<did>_<env>_<region>_<customer>-rest-api.
// The leading segment is the repo *instance* name, which is what the service is
// addressed as: transform_hmd-ms-transform_local_..._-rest-api -> /<env>/transform/.
const restAPISuffix = "-rest-api"

// RoutePathForGateway is the route segment a CDKTF gateway should be served
// under, or "" when the gateway is not one.
func RoutePathForGateway(name string) string {
	if name == "" || strings.HasPrefix(name, cliGatewayPrefix) {
		return ""
	}
	if !strings.HasSuffix(name, restAPISuffix) {
		return ""
	}
	// Strip the suffix before splitting, so a name carrying no underscore at
	// all still yields a usable route rather than one ending in "-rest-api".
	stem := strings.TrimSuffix(name, restAPISuffix)
	instance, _, _ := strings.Cut(stem, "_")
	return strings.TrimSpace(instance)
}

// DeployedRoute is one DAG-deployed service's gateway and its live stage.
type DeployedRoute struct {
	RestAPIID string
	StageName string
}

// DeployedServiceRoutes maps a route path to the gateway serving it, for every
// service deployed through the DAG into this account.
//
// The stage has to be read rather than assumed: CDKTF names its stage after the
// stack, not "local", so a route built with the default stage answers
// {"message": "Stage not found"}. An API with no deployed stage is skipped --
// it would 404 anyway, and it is usually a gateway mid-deploy.
func (g *Gateways) DeployedServiceRoutes(ctx context.Context) (map[string]DeployedRoute, error) {
	out, err := g.API.GetRestApis(ctx, &apigateway.GetRestApisInput{Limit: aws.Int32(500)})
	if err != nil {
		return nil, fmt.Errorf("listing API Gateways: %w", err)
	}
	routes := map[string]DeployedRoute{}
	for _, api := range out.Items {
		id, name := aws.ToString(api.Id), aws.ToString(api.Name)
		path := RoutePathForGateway(name)
		if id == "" || path == "" {
			continue
		}
		if _, seen := routes[path]; seen {
			continue
		}
		stages, err := g.API.GetStages(ctx, &apigateway.GetStagesInput{RestApiId: aws.String(id)})
		if err != nil || len(stages.Item) == 0 {
			continue
		}
		routes[path] = DeployedRoute{RestAPIID: id, StageName: aws.ToString(stages.Item[0].StageName)}
	}
	return routes, nil
}
