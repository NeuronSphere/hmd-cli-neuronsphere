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

Create Scratch Home
    [Documentation]    A throwaway HMD_HOME. Never the real one: container names
    ...                and volumes are global, and these tests write config and
    ...                token files.
    ${home}=    Join Path    ${TEMPDIR}    nsctl-cli-${{ __import__('uuid').uuid4().hex[:8] }}
    Create Directory    ${home}${/}.config
    RETURN    ${home}

Run nsctl In Home
    [Documentation]    Runs the binary against a scratch HMD_HOME, with stdin
    ...                closed so nothing can block on a prompt.
    [Arguments]    ${home}    @{args}
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-cli` does.
    ${result}=    Run Process    ${BINARY}    @{args}
    ...    env:HMD_HOME=${home}
    ...    env:HMD_LOCAL_MS_DEPLOYMENT_URL=http://127.0.0.1:1
    ...    stdin=${None}
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
    FOR    ${command}    IN    env    repo    control-plane    authd    login    logout    whoami    version
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

Login Without Configuration Refuses And Shows What To Write
    [Documentation]    NERD008 SPEC002. With no nsctl.toml and no terminal to
    ...                answer -- which is every CI run -- login must refuse with
    ...                exit 2 rather than prompt into a pipe, and must name the
    ...                path and the key so the refusal is actionable.
    [Tags]    contract    nerd008
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    login
    Should Be Equal As Integers    ${result.rc}    2
    ${output}=    Set Variable    ${result.stdout}${result.stderr}
    Should Contain    ${output}    nsctl.toml
    Should Contain    ${output}    auth_url
    # The prompt belongs to a terminal. /dev/null is a character device, so a
    # naive isatty check prints it here and looks like a hang in a CI log.
    Should Not Contain    ${output}    Endpoint for profile

Login Rejects A Bare Hostname As auth_url
    [Documentation]    NERD008 SPEC001: auth_url is the issuer, not a host --
    ...                discovery is appended to it.
    [Tags]    contract    nerd008
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    login    --auth-url    auth.example.test
    Should Be Equal As Integers    ${result.rc}    2

Login Never Overwrites A Config It Cannot Parse
    [Documentation]    NERD008 SPEC002: absent is prompted for, invalid never
    ...                is. A typo must not become data loss.
    [Tags]    contract    nerd008
    ${home}=      Create Scratch Home
    ${config}=    Set Variable    ${home}/.config/nsctl.toml
    Create File    ${config}    [profile.local]\nauth_uri = "https://typo.test/oauth2/ns"\n
    ${result}=    Run nsctl In Home    ${home}    login
    Should Be Equal As Integers    ${result.rc}    2
    ${after}=     Get File    ${config}
    Should Contain    ${after}    auth_uri

Whoami Without A Credential Refuses
    [Documentation]    Not signed in is a precondition the caller can fix, so
    ...                exit 2 rather than 1.
    [Tags]    contract    nerd008
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    whoami
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    nsctl login

Logout Without A Credential Succeeds
    [Documentation]    Logging out when not logged in is a no-op, not a failure;
    ...                a teardown script must be able to call it unconditionally.
    [Tags]    contract    nerd008
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    logout
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    Not signed in
