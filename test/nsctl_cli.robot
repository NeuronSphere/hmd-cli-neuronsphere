*** Settings ***
Documentation     Contract tests for the nsctl binary: the things that must hold
...               on a machine with nothing running.
...
...               No Docker, no control plane, no HMD_HOME. Every other suite in
...               this directory needs a platform; this one is what CI and a
...               release build can run, and it is the suite `make test-cli`
...               invokes with BINARY pointed at the freshly built binary.
Library           Process
Library           OperatingSystem
Library           String

*** Variables ***
${BINARY}         ${EMPTY}

*** Keywords ***
Run nsctl
    [Documentation]    Runs the binary under test with no HMD_HOME and no
    ...                control plane, so nothing here depends on the machine.
    [Arguments]    @{args}
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-cli` does.
    ${result}=    Run Process    ${BINARY}    @{args}
    ...    env:HMD_HOME=${EMPTY}
    ...    env:HMD_LOCAL_MS_DEPLOYMENT_URL=http://127.0.0.1:1
    RETURN    ${result}

*** Test Cases ***
Version Prints The Injected Version
    [Documentation]    SPEC013: the binary reports the version -ldflags put in
    ...                it. "dev" would mean the build lost its version stamp.
    [Tags]    contract
    ${result}=    Run nsctl    version
    Should Be Equal As Integers    ${result.rc}    0
    Should Match Regexp    ${result.stdout}    (?m)^nsctl \\S+$
    Should Not Contain    ${result.stdout}    nsctl dev

Version Omits ms-deployment When No Control Plane Answers
    [Documentation]    SPEC013 asks version to also report the ms-deployment
    ...                version when a control plane is reachable. With none, it
    ...                must still answer -- this is the command you run to check
    ...                an install.
    [Tags]    contract
    ${result}=    Run nsctl    version
    Should Be Equal As Integers    ${result.rc}    0
    Should Not Contain    ${result.stdout}    hmd-ms-deployment

Version Flag Matches The Version Command's First Line
    [Tags]    contract
    ${flag}=      Run nsctl    --version
    ${command}=   Run nsctl    version
    ${first}=     Get Line    ${command.stdout}    0
    Should Be Equal    ${flag.stdout.strip()}    ${first.strip()}

Help Lists Every Top-Level Command
    [Tags]    contract
    ${result}=    Run nsctl    --help
    Should Be Equal As Integers    ${result.rc}    0
    FOR    ${command}    IN    env    repo    control-plane    version
        Should Contain    ${result.stdout}    ${command}
    END

Unknown Command Is A Usage Error
    [Documentation]    nserr.Usage is 2, and a mistyped command must not look
    ...                like nsctl itself failing (1).
    [Tags]    contract
    ${result}=    Run nsctl    definitely-not-a-command
    Should Be Equal As Integers    ${result.rc}    2

Unknown Flag Is A Usage Error
    [Tags]    contract
    ${result}=    Run nsctl    --definitely-not-a-flag
    Should Be Equal As Integers    ${result.rc}    2

Missing HMD_HOME Names Both Ways To Supply It
    [Documentation]    HMD_HOME is never defaulted -- guessing would name
    ...                containers and volumes no other HMD tool computes -- so
    ...                the refusal has to say how to set it.
    [Tags]    contract
    ${result}=    Run nsctl    env    list
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    HMD_HOME is not set
    Should Contain    ${result.stderr}    --home
