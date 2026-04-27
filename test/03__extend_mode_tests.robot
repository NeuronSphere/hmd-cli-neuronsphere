*** Settings ***
Documentation     Extend mode tests for NeuronSphere admin control plane.
...               All tests run with HMD_LOCAL_NEURONSPHERE_MODE=extend.
...               ms-deployment and ms-naming run as Lambda functions in Floci,
...               verified via HTTP response rather than container status.
...               All tests are in a single suite so Pabot does not parallelize
...               them -- they share Docker ports and the neuronsphere_default network.
Library           OperatingSystem
Library           resources/NeuronSphereLib.py
Library           resources/FlociLib.py
Variables         variables/plugin_containers.py

Suite Setup       Setup Extend Mode
Suite Teardown    Teardown Extend Mode

*** Keywords ***
Setup Extend Mode
    [Documentation]    Set extend mode env var, ensure clean state.
    Set Environment Variable    HMD_LOCAL_NEURONSPHERE_MODE    extend
    Ensure HMD Environment
    Stop Local NeuronSphere
    Clean Floci State

Teardown Extend Mode
    [Documentation]    Stop extend mode and remove env var.
    Stop Local NeuronSphere
    Remove Environment Variable    HMD_LOCAL_NEURONSPHERE_MODE

Verify Extend Containers Running
    [Arguments]    ${container_list}
    Wait Until Keyword Succeeds    5 min    30 sec
    ...    All Containers Should Be Running    ${container_list}

Verify Extend Containers Stopped
    [Arguments]    ${container_list}
    All Containers Should Not Be Running    ${container_list}

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# Admin Control Plane Start
# ═══════════════════════════════════════════════════════════════

Extend Mode Starts Admin Control Plane
    [Tags]    integration    extend-mode
    [Documentation]    Floci (admin + workload), PostgreSQL, and nginx containers start in extend mode.
    Start Local NeuronSphere
    Verify Extend Containers Running    ${EXTEND_ADMIN_RUNNING_CONTAINERS}

Extend Mode ms-deployment Responds
    [Tags]    integration    extend-mode
    [Documentation]    ms-deployment Lambda function responds via API Gateway.
    ...    Verifies the Lambda was deployed to Floci and API Gateway routes work.
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Service Should Respond    http://host.docker.internal/hmd_ms_deployment/

Extend Mode Starts Compose Substitutes
    [Tags]    integration    extend-mode
    [Documentation]    JanusGraph starts as compose_substitute for Neptune.
    Verify Extend Containers Running    ${EXTEND_SUBSTITUTE_CONTAINERS}

# ═══════════════════════════════════════════════════════════════
# Workload Floci
# ═══════════════════════════════════════════════════════════════

Extend Mode Workload Floci Running
    [Tags]    integration    extend-mode    phase2
    [Documentation]    Workload Floci instance runs on port 4567 with separate account.
    Container Should Be Running    floci-workload
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Service Should Respond    http://localhost:4567/_floci/health

# ═══════════════════════════════════════════════════════════════
# BOM File Loading
# ═══════════════════════════════════════════════════════════════

Extend Mode Loads BOM From File
    [Tags]    integration    extend-mode    phase2
    [Documentation]    When HMD_LOCAL_BOM_FILE is set, BOM is loaded from the snapshot file.
    ...    The dev snapshot has 103 entries (minus image_only entries).
    ${count}=    Get Deployment BOM Count    local
    Should Be True    ${count} > 50    BOM should have >50 entries from snapshot file

Extend Mode Uses Snapshot Versions
    [Tags]    integration    extend-mode    phase2
    [Documentation]    Repo versions come from the BOM snapshot, not fallback "0.1.0".
    ${bom}=    Get Deployment BOM    local
    ${vpc_entry}=    Find BOM Entry By Instance Name    ${bom}    base-vpc
    Should Not Be Equal    ${vpc_entry}[repo_class_version]    0.1.0
    ...    msg=base-vpc version should come from snapshot, not fallback

# ═══════════════════════════════════════════════════════════════
# Deployment Graph & Local Workflow
# ═══════════════════════════════════════════════════════════════

Extend Mode Seeds Deployment BOM
    [Tags]    integration    extend-mode    phase2
    [Documentation]    BOM seeding creates entries in the deployment graph
    ...    via ms-deployment CRUD and apiop endpoints.
    ${bom}=    Get Deployment BOM    local
    Should Not Be Empty    ${bom}

Extend Mode VPC Marked Deployed
    [Tags]    integration    extend-mode    phase2
    [Documentation]    hmd-vpc should be skipped (strategy=skip) and marked DEPLOYED.
    ${status}=    Get Instance Deployment Status    base-vpc    local
    Should Be Equal    ${status}    DEPLOYED

Extend Mode S3Bucket Deployed
    [Tags]    integration    extend-mode    phase2
    [Documentation]    hmd-inf-s3bucket deploys via CDKTF against workload Floci.
    ${status}=    Get Instance Deployment Status    ch-storage    local
    Should Be Equal    ${status}    DEPLOYED

Extend Mode All Entries Have Status
    [Tags]    integration    extend-mode    phase2
    [Documentation]    Every entry in the BOM should have been processed by the
    ...    LocalWorkflowRunner and have a final status (not stuck at DEPLOY_NEXT).
    ${bom}=    Get Deployment BOM    local
    All BOM Entries Should Have Final Status    ${bom}

# ═══════════════════════════════════════════════════════════════
# Admin Control Plane Stop
# ═══════════════════════════════════════════════════════════════

Extend Mode Stop Tears Down Admin
    [Tags]    integration    extend-mode
    [Documentation]    All admin and substitute containers stopped after down.
    Stop Local NeuronSphere
    Verify Extend Containers Stopped    ${EXTEND_ADMIN_RUNNING_CONTAINERS}
    Verify Extend Containers Stopped    ${EXTEND_SUBSTITUTE_CONTAINERS}
