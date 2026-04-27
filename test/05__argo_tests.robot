*** Settings ***
Documentation     Argo Workflows + k3s integration tests for local NeuronSphere.
...               Verifies that ``hmd neuronsphere up`` (platform mode) creates a
...               Floci EKS k3s cluster, installs Argo Workflows on it via the
...               hmd-app-argo plugin, and exposes the Argo UI at /argo/.
...               All tests share the same NeuronSphere stack and run in one file
...               so Pabot serializes them.
Library           OperatingSystem
Library           Process
Library           RequestsLibrary
Library           resources/NeuronSphereLib.py
Library           resources/FlociLib.py
Library           resources/K3sLib.py

Suite Setup       Setup Argo Suite
Suite Teardown    Teardown Argo Suite

*** Variables ***
${ARGO_NAMESPACE}    argo

*** Keywords ***
Setup Argo Suite
    [Documentation]    Start a fresh NeuronSphere stack with k3s + Argo enabled.
    Ensure HMD Environment
    Stop Local NeuronSphere
    Clean Floci State
    Set Environment Variable    HMD_LOCAL_NEURONSPHERE_ENABLE_K3S      true
    Set Environment Variable    HMD_LOCAL_NEURONSPHERE_ENABLE_ARGO     true
    Start Local NeuronSphere

Teardown Argo Suite
    Stop Local NeuronSphere

*** Test Cases ***
Platform Mode Creates k3s Cluster
    [Tags]    integration    argo    k3s
    [Documentation]    The Floci EKS k3s cluster is up and has a Ready node.
    Wait Until Keyword Succeeds    3 min    10 sec    Kubeconfig Should Exist
    Wait Until Keyword Succeeds    3 min    10 sec    K3s Should Have Ready Node

Argo Plugin Installs Argo On k3s
    [Tags]    integration    argo    k3s
    [Documentation]    The hmd-app-argo plugin runs install_argo.sh and brings up
    ...                workflow-controller + argo-server in the argo namespace.
    Wait Until Keyword Succeeds    3 min    10 sec
    ...    Argo Pods Should Be Running    ${ARGO_NAMESPACE}

Argo UI Accessible At /argo/
    [Tags]    integration    argo    networking
    [Documentation]    nginx routes /argo/ to the k3s NodePort exposing argo-server.
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Argo Endpoint Should Respond

Argo Token Stored In Floci Secrets Manager
    [Tags]    integration    argo    secrets
    [Documentation]    The install script emits a service-account bearer token,
    ...                and the argo plugin stashes it under Secrets Manager.
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Floci Secret Should Exist    argo-token

*** Keywords ***
Argo Endpoint Should Respond
    ${response}=    GET    http://localhost/argo/
    Should Be True    ${response.status_code} < 500
