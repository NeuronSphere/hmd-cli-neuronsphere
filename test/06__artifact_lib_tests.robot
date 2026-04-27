*** Settings ***
Documentation     Artifact librarian + pull-artifact CLI tests.
...               Verifies that ``hmd-ms-artifact-lib`` is deployed by default
...               as a Floci Lambda and that the new ``hmd neuronsphere
...               pull-artifact`` command exists and parses arguments correctly.
...               Full cloud → local artifact copying is exercised by the
...               external hmd-intg-transform suite (which runs against the
...               same stack); these tests are smoke tests for the wiring.
Library           OperatingSystem
Library           Process
Library           RequestsLibrary
Library           resources/NeuronSphereLib.py
Library           resources/FlociLib.py

Suite Setup       Setup Artifact Lib Suite
Suite Teardown    Teardown Artifact Lib Suite

*** Variables ***
${LAMBDA_NAME}      hmd_ms_artifact_lib
${BUCKET_NAME}      artifact-librarian

*** Keywords ***
Setup Artifact Lib Suite
    Ensure HMD Environment
    Stop Local NeuronSphere
    Clean Floci State
    Set Environment Variable    HMD_LOCAL_NEURONSPHERE_ENABLE_ARTIFACT_LIB    true
    Start Local NeuronSphere

Teardown Artifact Lib Suite
    Stop Local NeuronSphere

*** Test Cases ***
Artifact Lib Lambda Is Deployed
    [Tags]    integration    artifact-lib
    [Documentation]    The artifact-lib HMDMS plugin deploys the Lambda by default.
    Wait Until Keyword Succeeds    3 min    10 sec
    ...    Lambda Function Should Exist    ${LAMBDA_NAME}

Artifact Lib Bucket Provisioned
    [Tags]    integration    artifact-lib
    [Documentation]    The declared S3 bucket is created in Floci.
    Wait Until S3 Is Ready
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Bucket Should Exist    ${BUCKET_NAME}

Pull Artifact CLI Help
    [Tags]    integration    artifact-lib    cli
    [Documentation]    The new ``hmd neuronsphere pull-artifact`` subcommand
    ...                is registered and prints help.
    ${result}=    Run Process    hmd    neuronsphere    pull-artifact    --help
    ...    timeout=30s    stdout=PIPE    stderr=PIPE
    Should Be Equal As Integers    ${result.rc}    0
    ...    msg=pull-artifact help failed: ${result.stderr}
    Should Contain    ${result.stdout}    --repo
    Should Contain    ${result.stdout}    --version
    Should Contain    ${result.stdout}    --cloud-url
