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

Push Artifact CLI Help
    [Tags]    integration    artifact-lib    cli
    [Documentation]    The new ``hmd neuronsphere push-artifact`` subcommand
    ...                is registered and prints help.
    ${result}=    Run Process    hmd    neuronsphere    push-artifact    --help
    ...    timeout=30s    stdout=PIPE    stderr=PIPE
    Should Be Equal As Integers    ${result.rc}    0
    ...    msg=push-artifact help failed: ${result.stderr}
    Should Contain    ${result.stdout}    --name
    Should Contain    ${result.stdout}    --version
    Should Contain    ${result.stdout}    --build-path
    Should Contain    ${result.stdout}    --local-url

Push Artifact Round Trip
    [Tags]    integration    artifact-lib    cli
    [Documentation]    A pre-built directory pushed via ``push-artifact``
    ...                can be retrieved from the local artifact librarian
    ...                under the user-specified version.
    Wait Until S3 Is Ready
    Wait Until Keyword Succeeds    2 min    10 sec
    ...    Bucket Should Exist    ${BUCKET_NAME}
    ${tmp}=    Set Variable    %{TMPDIR=/tmp}/ns-push-artifact-test
    Remove Directory    ${tmp}    recursive=True
    Create Directory    ${tmp}/build
    Create File    ${tmp}/build/marker.txt    push-artifact-roundtrip
    ${result}=    Run Process    hmd    neuronsphere    push-artifact
    ...    --name    demo-tf    --version    9.9.9
    ...    --build-path    ${tmp}/build
    ...    timeout=120s    stdout=PIPE    stderr=PIPE
    Should Be Equal As Integers    ${result.rc}    0
    ...    msg=push-artifact failed: rc=${result.rc} stdout=${result.stdout} stderr=${result.stderr}
    Should Contain    ${result.stdout}    Registered demo-tf:9.9.9
    Create Directory    ${tmp}/out
    Set Environment Variable    HMD_ARTIFACT_LIBRARIAN_URL    http://localhost/hmd_ms_artifact_lib/
    Set Environment Variable    HMD_ARTIFACT_LIBRARIAN_API_KEY    local-dummy
    ${pull}=    Run Process    python    -c
    ...    from hmd_lib_librarian_client.artifact_tools import retrieve_and_unzip; retrieve_and_unzip('local','reg1','repository:/demo-tf/9.9.9/demo-tf_9.9.9_build.zip','${tmp}/out')
    ...    timeout=60s    stdout=PIPE    stderr=PIPE
    Should Be Equal As Integers    ${pull.rc}    0
    ...    msg=retrieve_and_unzip failed: ${pull.stderr}
    File Should Exist    ${tmp}/out/marker.txt
    ${marker}=    Get File    ${tmp}/out/marker.txt
    Should Be Equal As Strings    ${marker.strip()}    push-artifact-roundtrip
