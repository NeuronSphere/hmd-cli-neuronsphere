package floci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apitypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

func writeStore(t *testing.T, dir, name string, body any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readStore(t *testing.T, dir, name string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A ghost is a record Floci rehydrates with every field null: unusable, and
// undeletable through the API because it has no id.
func TestPruneAPIGatewayGhostsDropsGhostsAndKeepsRealGateways(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeStore(t, dir, apigwAPIStore, map[string]any{
		"000000000000/us-west-2::a8b73e0e83": map[string]any{"id": nil, "name": nil},
		"000000000000/us-west-2::real1":      map[string]any{"id": "real1", "name": "neuronsphere-hmd_ms_naming"},
		"000000000000/us-west-2::noname":     map[string]any{"id": "noname", "name": ""},
	})

	removed := PruneAPIGatewayGhosts(dir)
	if removed != 2 {
		t.Errorf("removed %d records, want the two ghosts", removed)
	}

	kept := readStore(t, dir, apigwAPIStore)
	if len(kept) != 1 {
		t.Fatalf("kept %d gateways, want the real one: %v", len(kept), kept)
	}
	if _, ok := kept["000000000000/us-west-2::real1"]; !ok {
		t.Errorf("the real gateway was dropped: %v", kept)
	}
}

// A service deployed through the DAG gets a CDKTF-managed gateway that a warm
// restart never recreates. Wiping the store destroyed it with nothing left to
// rebuild it, and every /<env>/<service>/ request fell through to the
// catch-all.
func TestPruneAPIGatewayGhostsKeepsAGatewayWithNoGhosts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeStore(t, dir, apigwAPIStore, map[string]any{
		"acct::cdktf1": map[string]any{"id": "cdktf1", "name": "transform-rest-api"},
	})
	writeStore(t, dir, "apigateway-resources.json", map[string]any{
		"acct::cdktf1::res1": map[string]any{"id": "res1"},
	})

	if removed := PruneAPIGatewayGhosts(dir); removed != 0 {
		t.Errorf("removed %d records from a clean store", removed)
	}
	if len(readStore(t, dir, apigwAPIStore)) != 1 {
		t.Error("the CDKTF-managed gateway was dropped")
	}
	if len(readStore(t, dir, "apigateway-resources.json")) != 1 {
		t.Error("a child record of a live gateway was dropped")
	}
}

// Records hanging off a dropped gateway go with it, so nothing is orphaned.
func TestPruneAPIGatewayGhostsDropsOrphanedChildren(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeStore(t, dir, apigwAPIStore, map[string]any{
		"acct::live":  map[string]any{"id": "live", "name": "neuronsphere-svc"},
		"acct::ghost": map[string]any{"id": nil, "name": nil},
	})
	writeStore(t, dir, "apigateway-stages.json", map[string]any{
		"acct::live::local":  map[string]any{"stageName": "local"},
		"acct::dead::local":  map[string]any{"stageName": "local"},
		"acct::ghost::local": map[string]any{"stageName": "local"},
	})

	removed := PruneAPIGatewayGhosts(dir)
	// One ghost API plus two stages whose API is gone.
	if removed != 3 {
		t.Errorf("removed %d records, want 3", removed)
	}
	stages := readStore(t, dir, "apigateway-stages.json")
	if len(stages) != 1 {
		t.Errorf("kept %d stages, want only the live one: %v", len(stages), stages)
	}
	if _, ok := stages["acct::live::local"]; !ok {
		t.Error("the live gateway's stage was dropped")
	}
}

// Best-effort: an unreadable, missing or malformed store must never fail a
// start.
func TestPruneAPIGatewayGhostsIsBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(dir string)
	}{
		{"no data directory", func(string) {}},
		{"no stores", func(dir string) { _ = os.WriteFile(filepath.Join(dir, "other.json"), []byte("{}"), 0o644) }},
		{"malformed api store", func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, apigwAPIStore), []byte("{not json"), 0o644)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			tt.setup(dir)
			if got := PruneAPIGatewayGhosts(dir); got != 0 {
				t.Errorf("removed %d records, want 0", got)
			}
		})
	}
}

// Without an authoritative API list every child record would look orphaned.
func TestPruneAPIGatewayGhostsLeavesChildrenAloneWithNoAPIStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeStore(t, dir, "apigateway-stages.json", map[string]any{
		"acct::something::local": map[string]any{"stageName": "local"},
	})

	if got := PruneAPIGatewayGhosts(dir); got != 0 {
		t.Errorf("removed %d records with no API store", got)
	}
	if len(readStore(t, dir, "apigateway-stages.json")) != 1 {
		t.Error("a child record was dropped with no API store to judge it against")
	}
}

// fakeGateways records what was asked of the API Gateway client.
type fakeGateways struct {
	apis        []apitypes.RestApi
	stages      map[string][]apitypes.Stage
	deployments []string
	deleted     []string
	created     []string
	deleteErr   error
}

func (f *fakeGateways) GetRestApis(context.Context, *apigateway.GetRestApisInput, ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error) {
	return &apigateway.GetRestApisOutput{Items: f.apis}, nil
}

func (f *fakeGateways) GetStages(_ context.Context, in *apigateway.GetStagesInput, _ ...func(*apigateway.Options)) (*apigateway.GetStagesOutput, error) {
	return &apigateway.GetStagesOutput{Item: f.stages[aws.ToString(in.RestApiId)]}, nil
}

func (f *fakeGateways) CreateDeployment(_ context.Context, in *apigateway.CreateDeploymentInput, _ ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error) {
	f.deployments = append(f.deployments, aws.ToString(in.RestApiId))
	return &apigateway.CreateDeploymentOutput{Id: aws.String("dep-" + aws.ToString(in.RestApiId))}, nil
}

func (f *fakeGateways) DeleteStage(_ context.Context, in *apigateway.DeleteStageInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteStageOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.StageName))
	return nil, f.deleteErr
}

func (f *fakeGateways) CreateStage(_ context.Context, in *apigateway.CreateStageInput, _ ...func(*apigateway.Options)) (*apigateway.CreateStageOutput, error) {
	f.created = append(f.created, aws.ToString(in.StageName)+"@"+aws.ToString(in.DeploymentId))
	return nil, nil
}

// This is how a warm restart recovers the gateway ids the bootstrap DAG would
// otherwise hand it.
func TestServiceGatewaysMapsNamesBackToServices(t *testing.T) {
	t.Parallel()

	g := &Gateways{API: &fakeGateways{apis: []apitypes.RestApi{
		{Id: aws.String("id1"), Name: aws.String("neuronsphere-hmd_ms_naming")},
		{Id: aws.String("id2"), Name: aws.String("neuronsphere-hmd_ms_deployment")},
		// A CDKTF-managed gateway, not one setup_service named.
		{Id: aws.String("id3"), Name: aws.String("transform-rest-api")},
		// A ghost.
		{Id: nil, Name: nil},
	}}}

	got, err := g.ServiceGateways(context.Background())
	if err != nil {
		t.Fatalf("ServiceGateways: %v", err)
	}
	want := map[string]string{"hmd_ms_naming": "id1", "hmd_ms_deployment": "id2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// Floci does not support update_stage patch operations reliably and does not
// auto-create a stage from create_deployment, so the stage is deleted and
// recreated.
func TestDeployStageRecreatesTheStage(t *testing.T) {
	t.Parallel()

	f := &fakeGateways{}
	g := &Gateways{API: f}

	if err := g.DeployStage(context.Background(), "api1", ""); err != nil {
		t.Fatalf("DeployStage: %v", err)
	}
	if len(f.deployments) != 1 || f.deployments[0] != "api1" {
		t.Errorf("deployments = %v", f.deployments)
	}
	if len(f.deleted) != 1 || f.deleted[0] != DefaultStage {
		t.Errorf("deleted = %v, want the local stage", f.deleted)
	}
	if len(f.created) != 1 || f.created[0] != "local@dep-api1" {
		t.Errorf("created = %v, want the stage linked to the new deployment", f.created)
	}
}

// A missing stage is the normal first-time case.
func TestDeployStageToleratesAMissingStage(t *testing.T) {
	t.Parallel()

	g := &Gateways{API: &fakeGateways{deleteErr: context.DeadlineExceeded}}
	if err := g.DeployStage(context.Background(), "api1", "local"); err != nil {
		t.Errorf("DeployStage failed on a missing stage: %v", err)
	}
}
