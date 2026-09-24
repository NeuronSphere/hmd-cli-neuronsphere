package floci

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apitypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// Floci does not support the ANY catch-all method, so every HTTP method is
// registered individually.
var httpMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

// lambdaAPI narrows the Lambda client to the calls used here.
type lambdaAPI interface {
	CreateFunction(ctx context.Context, in *lambda.CreateFunctionInput, opts ...func(*lambda.Options)) (*lambda.CreateFunctionOutput, error)
	UpdateFunctionCode(ctx context.Context, in *lambda.UpdateFunctionCodeInput, opts ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error)
	UpdateFunctionConfiguration(ctx context.Context, in *lambda.UpdateFunctionConfigurationInput, opts ...func(*lambda.Options)) (*lambda.UpdateFunctionConfigurationOutput, error)
	GetFunction(ctx context.Context, in *lambda.GetFunctionInput, opts ...func(*lambda.Options)) (*lambda.GetFunctionOutput, error)
}

// gatewayAdminAPI is the API Gateway surface for wiring a Lambda behind a REST
// API.
type gatewayAdminAPI interface {
	apigatewayAPI
	DeleteRestApi(ctx context.Context, in *apigateway.DeleteRestApiInput, opts ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	CreateRestApi(ctx context.Context, in *apigateway.CreateRestApiInput, opts ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	GetResources(ctx context.Context, in *apigateway.GetResourcesInput, opts ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	CreateResource(ctx context.Context, in *apigateway.CreateResourceInput, opts ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	DeleteMethod(ctx context.Context, in *apigateway.DeleteMethodInput, opts ...func(*apigateway.Options)) (*apigateway.DeleteMethodOutput, error)
	PutMethod(ctx context.Context, in *apigateway.PutMethodInput, opts ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	PutIntegration(ctx context.Context, in *apigateway.PutIntegrationInput, opts ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error)
}

// Services deploys the foundation service Lambdas: the ones nsctl owns
// directly rather than deploying through the graph.
//
// They are bootstrapped before hmd-ms-deployment exists, or in dbaccount's case
// before anything that needs a database can deploy, so their own manifests'
// required dependencies would never resolve. What they provide is represented
// as concrete microservice Resources produced by the core instance instead.
type Services struct {
	Target   Target
	Lambda   lambdaAPI
	Gateways gatewayAdminAPI
}

// NewServices builds a Services from a target's AWS config.
func NewServices(ctx context.Context, t Target) (*Services, error) {
	cfg, err := t.Config(ctx)
	if err != nil {
		return nil, err
	}
	return &Services{
		Target:   t,
		Lambda:   lambda.NewFromConfig(cfg),
		Gateways: apigateway.NewFromConfig(cfg),
	}, nil
}

// LambdaName is the function name for a repo class: dashes become underscores,
// matching what the routes and the gateway are named after.
func LambdaName(repoClass string) string {
	return strings.ReplaceAll(repoClass, "-", "_")
}

// MSDeploymentServiceName is the deployment service as everything addresses
// it -- Lambda hmd_ms_deployment, route /hmd_ms_deployment/, DB user, the
// repo_class tag on its microservice Resource -- and MSDeploymentImageClass is
// the RepoClass whose image and descriptor the local control plane runs for
// it (NERD0015): the registry-and-resolver core, not the orchestrator. The two
// are the same string for every other foundation service.
const (
	MSDeploymentServiceName = "hmd-ms-deployment"
	MSDeploymentImageClass  = "hmd-ms-deployment-core"
)

// ImageClassFor is the RepoClass whose image a foundation service runs.
func ImageClassFor(service string) string {
	if service == MSDeploymentServiceName {
		return MSDeploymentImageClass
	}
	return service
}

// ServiceEnv is the environment a foundation service Lambda runs with.
//
// The database credentials follow the local convention that a service's user,
// password and database are all its repo class with underscores -- the same
// convention ms-dbaccount applies when it provisions one.
func ServiceEnv(repoClass, version string, names Names, dbHost string, serviceConfig map[string]any) map[string]string {
	if serviceConfig == nil {
		serviceConfig = map[string]any{}
	}
	encoded, err := json.Marshal(serviceConfig)
	if err != nil {
		encoded = []byte("{}")
	}
	user := LambdaName(repoClass)
	env := map[string]string{
		"HMD_INSTANCE_NAME":    LambdaName(repoClass),
		"HMD_REPO_NAME":        repoClass,
		"HMD_REPO_VERSION":     version,
		"HMD_ENVIRONMENT":      "local",
		"HMD_REGION":           names.Region,
		"HMD_CUSTOMER_CODE":    names.CustomerCode,
		"HMD_DID":              names.DeploymentID,
		"HMD_DB_HOST":          dbHost,
		"HMD_DB_USER":          user,
		"HMD_DB_PASSWORD":      user,
		"HMD_DB_NAME":          user,
		"HMD_USE_FASTAPI":      "true",
		"AWS_XRAY_SDK_ENABLED": "false",
		"SERVICE_CONFIG":       string(encoded),
	}
	return env
}

// DeployLambda creates or updates a Lambda from a container image.
func (s *Services) DeployLambda(ctx context.Context, functionName, imageURI string, env map[string]string) (string, error) {
	role := fmt.Sprintf("arn:aws:iam::%s:role/lambda-role", s.Target.AccountID)
	out, err := s.Lambda.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(imageURI)},
		Role:         aws.String(role),
		Timeout:      aws.Int32(300),
		MemorySize:   aws.Int32(512),
		Environment:  &lambdatypes.Environment{Variables: env},
	})
	if err == nil {
		return aws.ToString(out.FunctionArn), nil
	}
	if !isAPIErrorCode(err, "ResourceConflictException") {
		return "", fmt.Errorf("creating the %s function: %w", functionName, err)
	}

	if _, err := s.Lambda.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
		FunctionName: aws.String(functionName), ImageUri: aws.String(imageURI),
	}); err != nil {
		return "", fmt.Errorf("updating the %s function's code: %w", functionName, err)
	}
	if _, err := s.Lambda.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(functionName),
		Timeout:      aws.Int32(300),
		MemorySize:   aws.Int32(512),
		Environment:  &lambdatypes.Environment{Variables: env},
	}); err != nil {
		return "", fmt.Errorf("updating the %s function's configuration: %w", functionName, err)
	}
	got, err := s.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(functionName)})
	if err != nil {
		return "", fmt.Errorf("reading the %s function back: %w", functionName, err)
	}
	return aws.ToString(got.Configuration.FunctionArn), nil
}

// FunctionEnv is a deployed function's environment variables, or an error when
// there is no such function.
//
// Used to read back what a Lambda was deployed with -- HMD_REPO_VERSION above
// all, which ServiceEnv writes -- so currency can be decided from the
// deployment itself rather than from a record kept beside it that could
// disagree with it (NERD024 SPEC002).
func (s *Services) FunctionEnv(ctx context.Context, functionName string) (map[string]string, error) {
	got, err := s.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(functionName)})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", functionName, err)
	}
	if got.Configuration == nil || got.Configuration.Environment == nil {
		return map[string]string{}, nil
	}
	return got.Configuration.Environment.Variables, nil
}

// EnsureRestAPI creates or reuses a REST API by name.
//
// recreate deletes an existing one first, which matters because Floci's
// resource matcher prefers the earliest-created {proxy+} on a tie -- so
// accumulated stale routes from previous runs hijack traffic for a
// newly-registered service.
//
// A record with no id or name is a ghost, skipped rather than reused: Floci
// persists API Gateway v1 entities with all-null fields, and a ghost carries no
// id, so it can neither be reused nor deleted through the API.
func (s *Services) EnsureRestAPI(ctx context.Context, apiName string, recreate bool) (string, error) {
	list, err := s.Gateways.GetRestApis(ctx, &apigateway.GetRestApisInput{Limit: aws.Int32(500)})
	if err != nil {
		return "", fmt.Errorf("listing API Gateways: %w", err)
	}
	for _, api := range list.Items {
		id, name := aws.ToString(api.Id), aws.ToString(api.Name)
		if id == "" || name == "" || name != apiName {
			continue
		}
		if !recreate {
			return id, nil
		}
		if _, err := s.Gateways.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: api.Id}); err != nil {
			return "", fmt.Errorf("deleting the existing %s gateway: %w", apiName, err)
		}
	}
	created, err := s.Gateways.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String(apiName)})
	if err != nil {
		return "", fmt.Errorf("creating the %s gateway: %w", apiName, err)
	}
	return aws.ToString(created.Id), nil
}

// AddLambdaRoutes wires a gateway's root and {proxy+} to a Lambda.
//
// One Lambda per gateway, with no service-name prefix in the path, so nginx
// strips the prefix before proxying and the Lambda receives clean /api/...
// paths -- which FastAPI routes natively and would 404 otherwise.
func (s *Services) AddLambdaRoutes(ctx context.Context, apiID, functionName string) error {
	resources, err := s.Gateways.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: aws.String(apiID)})
	if err != nil {
		return fmt.Errorf("reading the %s gateway's resources: %w", apiID, err)
	}
	var rootID, proxyID string
	for _, r := range resources.Items {
		// Skip pathless records: Floci persists resources with null fields too,
		// so a rehydrated store serves ghosts alongside the real tree.
		path := aws.ToString(r.Path)
		if path == "" {
			continue
		}
		if path == "/" {
			rootID = aws.ToString(r.Id)
		}
		if path == "/{proxy+}" {
			proxyID = aws.ToString(r.Id)
		}
	}
	if rootID == "" {
		return fmt.Errorf("the %s gateway has no root resource", apiID)
	}
	if proxyID == "" {
		created, err := s.Gateways.CreateResource(ctx, &apigateway.CreateResourceInput{
			RestApiId: aws.String(apiID), ParentId: aws.String(rootID), PathPart: aws.String("{proxy+}"),
		})
		if err != nil {
			return fmt.Errorf("creating the {proxy+} resource: %w", err)
		}
		proxyID = aws.ToString(created.Id)
	}

	fn, err := s.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(functionName)})
	if err != nil {
		return fmt.Errorf("reading %s: %w", functionName, err)
	}
	uri := fmt.Sprintf("arn:aws:apigateway:%s:lambda:path/2015-03-31/functions/%s/invocations",
		s.Target.Region, aws.ToString(fn.Configuration.FunctionArn))

	for _, resourceID := range []string{rootID, proxyID} {
		for _, method := range httpMethods {
			// Delete first, so a partial prior registration cannot leave gaps
			// that make Floci's matcher fall through to a sibling {proxy+}.
			if _, err := s.Gateways.DeleteMethod(ctx, &apigateway.DeleteMethodInput{
				RestApiId: aws.String(apiID), ResourceId: aws.String(resourceID), HttpMethod: aws.String(method),
			}); err != nil && !isAPIErrorCode(err, "NotFoundException") {
				return fmt.Errorf("clearing the %s method: %w", method, err)
			}
			if _, err := s.Gateways.PutMethod(ctx, &apigateway.PutMethodInput{
				RestApiId: aws.String(apiID), ResourceId: aws.String(resourceID),
				HttpMethod: aws.String(method), AuthorizationType: aws.String("NONE"),
			}); err != nil {
				return fmt.Errorf("adding the %s method: %w", method, err)
			}
			if _, err := s.Gateways.PutIntegration(ctx, &apigateway.PutIntegrationInput{
				RestApiId: aws.String(apiID), ResourceId: aws.String(resourceID),
				HttpMethod: aws.String(method), Type: apitypes.IntegrationTypeAwsProxy,
				IntegrationHttpMethod: aws.String("POST"), Uri: aws.String(uri),
			}); err != nil {
				return fmt.Errorf("integrating the %s method: %w", method, err)
			}
		}
	}
	return nil
}

// SetupService deploys a Lambda, fronts it with a gateway and deploys the
// stage. It returns the REST API id so the caller can route it.
func (s *Services) SetupService(ctx context.Context, repoClass, imageURI string, env map[string]string) (string, error) {
	functionName := LambdaName(repoClass)
	if _, err := s.DeployLambda(ctx, functionName, imageURI, env); err != nil {
		return "", err
	}
	apiID, err := s.EnsureRestAPI(ctx, GatewayNamePrefix+functionName, true)
	if err != nil {
		return "", err
	}
	if err := s.AddLambdaRoutes(ctx, apiID, functionName); err != nil {
		return "", err
	}
	gw := &Gateways{API: s.Gateways}
	if err := gw.DeployStage(ctx, apiID, DefaultStage); err != nil {
		return "", err
	}
	return apiID, nil
}
