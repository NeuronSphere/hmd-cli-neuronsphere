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

Create Scratch Repo
    [Documentation]    A throwaway directory for a repo class, so the repoclass
    ...                verbs -- which need no HMD_HOME at all -- have somewhere
    ...                to write.
    ${dir}=    Join Path    ${TEMPDIR}    nsctl-repoclass-${{ __import__('uuid').uuid4().hex[:8] }}
    Create Directory    ${dir}
    RETURN    ${dir}

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
    FOR    ${command}    IN    env    repo    repoclass    control-plane    authd    login    logout    whoami    version    stack    plugin
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

Repoclass Init Writes The Minimum Without HMD_HOME
    [Documentation]    NERD009 SPEC007: init writes name, description and an
    ...                empty build section plus VERSION 0.1, needs no HMD_HOME,
    ...                and refuses a second time with exit 2 naming describe.
    [Tags]    contract    nerd009
    ${dir}=       Create Scratch Repo
    ${result}=    Run nsctl    repoclass    --path    ${dir}    init    acme-api    --description    The Acme API
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    wrote meta-data/manifest.json
    File Should Exist    ${dir}${/}meta-data${/}manifest.json
    ${version}=    Get File    ${dir}${/}meta-data${/}VERSION
    Should Be Equal    ${version.strip()}    0.1

    ${again}=     Run nsctl    repoclass    --path    ${dir}    init    acme-api
    Should Be Equal As Integers    ${again.rc}    2
    Should Contain    ${again.stderr}    nsctl repoclass describe

Repoclass Verbs Author And Validate A Foreign Toolset Class
    [Documentation]    NERD009 SPEC008/SPEC011: the write verbs each print one
    ...                line naming the file and key, describe --json reads it
    ...                back, and validate is clean but for the inert exec note.
    [Tags]    contract    nerd009
    ${dir}=       Create Scratch Repo
    Run nsctl    repoclass    --path    ${dir}    init    acme-dp    --description    A data product
    ${w}=         Run nsctl    repoclass    --path    ${dir}    build    set-mechanism    external
    Should Be Equal    ${w.stdout.strip()}    wrote meta-data/manifest.json build.mechanism
    Run nsctl    repoclass    --path    ${dir}    deploy    set-command    exec    acme-deploy
    Run nsctl    repoclass    --path    ${dir}    deploy    set-image    acme/ci-tools:1
    ${d}=         Run nsctl    repoclass    --path    ${dir}    deploy    add-dependency    warehouse
    ...    --resource-namespace    acme.com    --resource-definition-name    sql-warehouse    --resource-version    0.1.0
    Should Be Equal    ${d.stdout.strip()}    wrote meta-data/manifest.json deploy.dependencies.warehouse

    ${desc}=      Run nsctl    repoclass    --path    ${dir}    describe    --json
    Should Be Equal As Integers    ${desc.rc}    0
    Should Contain    ${desc.stdout}    "resource_definition_name": "sql-warehouse"
    Should Contain    ${desc.stdout}    "required": true

    ${v}=         Run nsctl    repoclass    --path    ${dir}    validate
    Should Be Equal As Integers    ${v.rc}    0
    Should Contain    ${v.stdout}    validate: ok -- 0 errors, 0 warnings, 1 note

Repoclass Validate Fails On An Error
    [Documentation]    An exec with nothing to run is an error, and errors are
    ...                exit 1 -- the file does not validate.
    [Tags]    contract    nerd009
    ${dir}=       Create Scratch Repo
    Create Directory    ${dir}${/}meta-data
    Create File    ${dir}${/}meta-data${/}manifest.json    {"name":"x","description":"d","build":{},"deploy":{"commands":[["exec"]]}}
    ${v}=         Run nsctl    repoclass    --path    ${dir}    validate
    Should Be Equal As Integers    ${v.rc}    1
    Should Contain    ${v.stdout}    validate: failed

Plugin List With No Config Reports None
    [Documentation]    NERD018 SPEC001: no nsctl.toml means no plugins, and
    ...                saying so is exit 0, not an error.
    [Tags]    contract    nerd018
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    plugin    list
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    No plugins declared

Declared Path Plugin Runs With Argv And Exit Code
    [Documentation]    NERD018 SPEC005: everything after the noun reaches the
    ...                plugin verbatim, the SPEC005 variables are set, the exit
    ...                status comes back unchanged, and nsctl adds no Error line.
    [Tags]    contract    nerd018
    ${home}=      Create Scratch Home
    ${script}=    Join Path    ${home}    nsctl-hello
    Create File    ${script}    \#!/bin/sh\necho "argv: $*"\necho "name=$NSCTL_PLUGIN_NAME home=$NSCTL_HOME"\nexit 7\n
    Run Process    chmod    755    ${script}
    Create File    ${home}${/}.config${/}nsctl.toml    [plugin.hello]\npath = "${script}"\n
    ${result}=    Run nsctl In Home    ${home}    hello    a    --b    --help
    Should Be Equal As Integers    ${result.rc}    7
    Should Contain    ${result.stdout}    argv: a --b --help
    Should Contain    ${result.stdout}    name=hello home=${home}
    Should Not Contain    ${result.stderr}    Error:

Declared Plugin Without Binary Names The Install Command
    [Documentation]    NERD018 SPEC004: declared but not installed is a usage
    ...                error whose remedy is the install verb.
    [Tags]    contract    nerd018
    ${home}=      Create Scratch Home
    Create File    ${home}${/}.config${/}nsctl.toml    [plugin.hello]\nsource = "oci://ghcr.io/hmdlabs/plugins/hello"\nversion = "1.0"\ndigest = "sha256:0"\n
    ${result}=    Run nsctl In Home    ${home}    hello
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    nsctl plugin install hello

Reserved Plugin Name Is Refused
    [Documentation]    NERD018 SPEC004: a declaration named after a built-in
    ...                warns and the built-in wins.
    [Tags]    contract    nerd018
    ${home}=      Create Scratch Home
    Create File    ${home}${/}.config${/}nsctl.toml    [plugin.env]\npath = "/x"\n
    ${result}=    Run nsctl In Home    ${home}    env    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    Usage:
    Should Contain    ${result.stderr}    warning: plugins not loaded

Stack Add Without HMD_HOME Names Both Ways
    [Tags]    contract    nerd017
    ${result}=    Run nsctl    stack    add    observability
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    HMD_HOME
    Should Contain    ${result.stderr}    --home

Stack Add Refuses The Deferred GitHub Scheme
    [Documentation]    NERD016 SPEC008: a github.com reference is recognised
    ...                and refused naming the deferred spec, not a parse error.
    [Tags]    contract    nerd017
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    stack    add    github.com/acme/stack@1.0
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    NERD016

Stack Versions Against A Closed Port Fails Cleanly
    [Documentation]    A registry that is not there is exit 1 with the host
    ...                in the message, never a panic.
    [Tags]    contract    nerd017
    ${home}=      Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    stack    versions    127.0.0.1:1/x/y
    Should Be Equal As Integers    ${result.rc}    1
    Should Contain    ${result.stderr}    127.0.0.1:1
    Should Not Contain    ${result.stderr}    panic

Stack Init Scaffolds A Stack That Validates
    [Documentation]    NERD019 SPEC004/SPEC006/SPEC007: the scaffold is a
    ...                RepoClass that validates, with the CI workflow beside it.
    [Tags]    contract    nerd019
    ${dir}=       Create Scratch Repo
    ${r}=         Run nsctl    stack    init    hmd-stack-x    --path    ${dir}${/}hmd-stack-x    --description    x
    Should Be Equal As Integers    ${r.rc}    0
    File Should Exist    ${dir}${/}hmd-stack-x${/}.github${/}workflows${/}stack.yml
    File Should Exist    ${dir}${/}hmd-stack-x${/}meta-data${/}manifest.json
    ${wf}=        Get File    ${dir}${/}hmd-stack-x${/}.github${/}workflows${/}stack.yml
    Should Contain    ${wf}    nsctl stack push
    Should Contain    ${wf}    secrets.GITHUB_TOKEN
    ${a}=         Run nsctl    repoclass    --path    ${dir}${/}hmd-stack-x    local    add    hmd-inf-redis    --spec    == 0.1.47
    Should Be Equal As Integers    ${a.rc}    0
    ${l}=         Run nsctl    lock    ${dir}${/}hmd-stack-x
    Should Be Equal As Integers    ${l.rc}    0
    ${v}=         Run nsctl    repoclass    --path    ${dir}${/}hmd-stack-x    validate
    Should Be Equal As Integers    ${v.rc}    0
    Should Contain    ${v.stdout}    validate: ok

Stack Build Refuses With Every Tier Named
    [Documentation]    NERD019 SPEC002: a pin nothing can serve is refused
    ...                naming the tiers and the artifact push remedy.
    [Tags]    contract    nerd019
    ${home}=      Create Scratch Home
    ${dir}=       Create Scratch Repo
    Run nsctl    stack    init    hmd-stack-y    --path    ${dir}${/}hmd-stack-y
    Run nsctl    repoclass    --path    ${dir}${/}hmd-stack-y    local    add    hmd-inf-redis    --spec    == 0.1.47
    Run nsctl    lock    ${dir}${/}hmd-stack-y
    ${b}=         Run nsctl In Home    ${home}    stack    build    ${dir}${/}hmd-stack-y
    Should Be Equal As Integers    ${b.rc}    2
    Should Contain    ${b.stderr}    artifact cache (not cached)
    Should Contain    ${b.stderr}    nsctl artifact push

Stack Push Refuses Anonymous
    [Tags]    contract    nerd019
    ${home}=      Create Scratch Home
    ${r}=         Run nsctl In Home    ${home}    stack    push    ghcr.io/acme/stacks/x    --from    ${home}
    Should Be Equal As Integers    ${r.rc}    2
    Should Contain    ${r.stderr}    --token
