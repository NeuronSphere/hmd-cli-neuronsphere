*** Settings ***
Documentation     HMDMS service plugin tests for local NeuronSphere.
...               A "fake librarian" plugin under test/resources/fake-librarian/
...               registers via src/local/nsplugin.json with an `hmdms_service`
...               block. The CLI should: (a) provision its S3 bucket on Floci,
...               (b) deploy a Lambda from <repo_name>:<version>, (c) wire the
...               <ENV_VAR>=s3://<bucket> into the transform plugin's environment,
...               and (d) seed ms-deployment with the librarian as a RepoClass +
...               RepoInstance + DEPLOYED RepoInstanceDeployment, mocking
...               unmet dependencies (e.g. hmd-vpc) as SKIPPED.
...               All tests in a single suite (Pabot does not parallelize them).
Library           OperatingSystem
Library           Process
Library           resources/NeuronSphereLib.py
Library           resources/FlociLib.py

Suite Setup       Setup HMDMS Service Suite
Suite Teardown    Teardown HMDMS Service Suite

*** Variables ***
${FIXTURE_PATH}       ${CURDIR}/resources/fake-librarian
${IMAGE_TAG}          hmd-ms-fake-librarian:0.1
${BUCKET_NAME}        fake-librarian
${LAMBDA_NAME}        hmd_ms_fake_librarian
${BUCKET_ENV_VAR}     FAKE_BUCKET
${MS_DEPLOYMENT_URL}  http://host.docker.internal/hmd_ms_deployment

*** Keywords ***
Setup HMDMS Service Suite
    [Documentation]    Build the fake librarian image, ensure clean state.
    Ensure HMD Environment
    Stop Local NeuronSphere
    Clean Floci State
    Build Fake Librarian Image
    Set Environment Variable    HMD_LOCAL_PLUGINS    ${FIXTURE_PATH}
    Set Environment Variable    HMD_LOCAL_NEURONSPHERE_ENABLE_FAKE_LIBRARIAN    true

Teardown HMDMS Service Suite
    [Documentation]    Stop NeuronSphere and unset env.
    Stop Local NeuronSphere
    Remove Environment Variable    HMD_LOCAL_PLUGINS
    Remove Environment Variable    HMD_LOCAL_NEURONSPHERE_ENABLE_FAKE_LIBRARIAN

Build Fake Librarian Image
    [Documentation]    Build a stub Lambda image and tag as <repo_name>:<version>.
    ${result}=    Run Process    docker    build    -t    ${IMAGE_TAG}    ${FIXTURE_PATH}
    ...    timeout=300s    stdout=PIPE    stderr=PIPE
    Should Be Equal As Integers    ${result.rc}    0    msg=Failed to build fake librarian image: ${result.stderr}

*** Test Cases ***
HMDMS Service Plugin Provisions S3 Bucket
    [Tags]    integration    hmdms-service
    [Documentation]    The fake librarian's bucket is created in Floci on `up`.
    Start Local NeuronSphere
    Wait Until S3 Is Ready
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Bucket Should Exist    ${BUCKET_NAME}

HMDMS Service Plugin Deploys Lambda
    [Tags]    integration    hmdms-service
    [Documentation]    A Lambda function tagged as <repo>:<version> is registered in Floci.
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Lambda Function Should Exist    ${LAMBDA_NAME}    ${IMAGE_TAG}

HMDMS Service Plugin Routes Lambda Through API Gateway
    [Tags]    integration    hmdms-service
    [Documentation]    The Lambda is reachable via nginx proxy at /<lambda_name>/.
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Service Should Respond    http://host.docker.internal/${LAMBDA_NAME}/

Transform Receives HMDMS Service Bucket Env Var
    [Tags]    integration    hmdms-service
    [Documentation]    Transform plugin's compose env contains the librarian's bucket env var.
    ...    Skipped if transform plugin is not enabled in this run.
    ${transform_running}=    Run Keyword And Return Status
    ...    Container Should Be Running    transform
    Skip If    not ${transform_running}    Transform plugin not enabled
    ${env}=    Get Container Env    transform
    Should Contain    ${env}    ${BUCKET_ENV_VAR}=s3://${BUCKET_NAME}

ms-deployment Has RepoClass For HMDMS Service
    [Tags]    integration    hmdms-service
    [Documentation]    A hmd_lang_deployment.repo_class entity exists for the librarian.
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    RepoClass Should Exist    hmd-ms-fake-librarian    ${MS_DEPLOYMENT_URL}

ms-deployment Has DEPLOYED Status For HMDMS Service
    [Tags]    integration    hmdms-service
    [Documentation]    The librarian's repo_instance_deployment status is DEPLOYED.
    ${status}=    Get HMDMS Deployment Status    hmd-ms-fake-librarian    ${MS_DEPLOYMENT_URL}
    Should Be Equal    ${status}    DEPLOYED

ms-deployment Mocks Unmet Dep As SKIPPED
    [Tags]    integration    hmdms-service
    [Documentation]    hmd-vpc is declared as a dependency but not deployed locally —
    ...    it should be present in ms-deployment with status SKIPPED.
    ${status}=    Get HMDMS Deployment Status    hmd-vpc    ${MS_DEPLOYMENT_URL}
    Should Be Equal    ${status}    SKIPPED

Status Command Lists HMDMS Service
    [Tags]    integration    hmdms-service
    [Documentation]    `hmd neuronsphere status --json` includes the librarian.
    ${result}=    Run NS Command    status    --json
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    hmd-ms-fake-librarian
    Should Contain    ${result.stdout}    ${BUCKET_NAME}

HMDMS Service Up Is Idempotent
    [Tags]    integration    hmdms-service
    [Documentation]    Running `up` twice does not error or duplicate entities.
    Stop Local NeuronSphere
    Start Local NeuronSphere
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Bucket Should Exist    ${BUCKET_NAME}
    ${status}=    Get HMDMS Deployment Status    hmd-ms-fake-librarian    ${MS_DEPLOYMENT_URL}
    Should Be Equal    ${status}    DEPLOYED
