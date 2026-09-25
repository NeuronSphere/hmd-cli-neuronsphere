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
    [Documentation]    Runs the binary under test with no HMD_HOME, no
    ...                control plane and no cloud librarian credential, so
    ...                nothing here depends on the machine -- a CI runner
    ...                exports HMD_AUTH_TOKEN for the release job, and a
    ...                tier that quietly answers turns a refusal test green.
    [Arguments]    @{args}
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-cli` does.
    ${result}=    Run Process    ${BINARY}    @{args}
    ...    env:HMD_HOME=${EMPTY}
    ...    env:HMD_LOCAL_MS_DEPLOYMENT_URL=http://127.0.0.1:1
    ...    env:HMD_AUTH_TOKEN=${EMPTY}
    ...    env:HMD_ARTIFACT_LIBRARIAN_URL=${EMPTY}
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

Run nsctl In Home Without An Engine
    [Documentation]    As Run nsctl In Home, but pointed at a container engine
    ...                that is not there. A contract about what happens when
    ...                Docker cannot be reached must not depend on whether the
    ...                machine running the suite happens to have it.
    [Arguments]    ${home}    @{args}
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-cli` does.
    ${result}=    Run Process    ${BINARY}    @{args}
    ...    env:HMD_HOME=${home}
    ...    env:DOCKER_HOST=unix:///nonexistent/docker.sock
    ...    env:DOCKER_CONTEXT=${EMPTY}
    ...    env:HMD_LOCAL_MS_DEPLOYMENT_URL=http://127.0.0.1:1
    ...    env:HMD_AUTH_TOKEN=${EMPTY}
    ...    env:HMD_ARTIFACT_LIBRARIAN_URL=${EMPTY}
    ...    stdin=${None}
    RETURN    ${result}

Run nsctl In Home
    [Documentation]    Runs the binary against a scratch HMD_HOME, with stdin
    ...                closed so nothing can block on a prompt.
    [Arguments]    ${home}    @{args}
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-cli` does.
    ${result}=    Run Process    ${BINARY}    @{args}
    ...    env:HMD_HOME=${home}
    ...    env:HMD_LOCAL_MS_DEPLOYMENT_URL=http://127.0.0.1:1
    ...    env:HMD_AUTH_TOKEN=${EMPTY}
    ...    env:HMD_ARTIFACT_LIBRARIAN_URL=${EMPTY}
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

Repoclass License Declares And Validates
    [Documentation]    NERD017 SPEC011: the author declares the licence of what
    ...                is published and the paths kept out of it; the verb
    ...                prints one line, validate accepts the shape, and a bad
    ...                exclude is a usage error. Nothing is inferred.
    [Tags]    contract    nerd017
    ${dir}=       Create Scratch Repo
    Run nsctl    repoclass    --path    ${dir}    init    acme-api    --description    The Acme API
    ${w}=         Run nsctl    repoclass    --path    ${dir}    license    set    Apache-2.0    --exclude    src/python/
    Should Be Equal As Integers    ${w.rc}    0
    Should Be Equal    ${w.stdout.strip()}    wrote meta-data/manifest.json license
    ${manifest}=    Get File    ${dir}${/}meta-data${/}manifest.json
    Should Contain    ${manifest}    "spdx": "Apache-2.0"
    Should Contain    ${manifest}    "src/python"
    ${v}=         Run nsctl    repoclass    --path    ${dir}    validate
    Should Be Equal As Integers    ${v.rc}    0
    ${bad}=       Run nsctl    repoclass    --path    ${dir}    license    set    MIT    --exclude    ../secrets
    Should Be Equal As Integers    ${bad.rc}    2
    Should Contain    ${bad.stderr}    license.exclude

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

Access Round-Trips Through Describe
    [Documentation]    NERD023 SPEC004: the access declaration is authored,
    ...                listed and described, and its placeholders stand in a
    ...                repository -- there is no instance or environment to
    ...                resolve them against yet.
    [Tags]    contract    nerd023
    ${dir}=       Create Scratch Repo
    Run nsctl    repoclass    --path    ${dir}    init    acme-ui    --description    An Acme UI
    ${a}=         Run nsctl    repoclass    --path    ${dir}    access    add    ui
    ...    --url    http://{ingress_host}/    --username    admin
    ...    --secret-store    secrets-manager
    ...    --secret-key    {instance_name}-{deployment_id}-{environment}-admin-credentials
    ...    --secret-property    password
    Should Be Equal As Integers    ${a.rc}    0
    Should Contain    ${a.stdout}    access

    ${l}=         Run nsctl    repoclass    --path    ${dir}    access    list
    Should Be Equal As Integers    ${l.rc}    0
    Should Contain    ${l.stdout}    secrets-manager
    Should Contain    ${l.stdout}    {ingress_host}

    ${desc}=      Run nsctl    repoclass    --path    ${dir}    describe    --json
    Should Be Equal As Integers    ${desc.rc}    0
    Should Contain    ${desc.stdout}    "access"
    Should Contain    ${desc.stdout}    "store": "secrets-manager"

    ${v}=         Run nsctl    repoclass    --path    ${dir}    validate
    Should Be Equal As Integers    ${v.rc}    0
    Should Contain    ${v.stdout}    validate: ok

    # Keyed and idempotent: a second add with the same name replaces it.
    Run nsctl    repoclass    --path    ${dir}    access    add    ui    --url    http://{ingress_host}/x/
    ${one}=       Run nsctl    repoclass    --path    ${dir}    access    list
    ${count}=     Get Line Count    ${one.stdout}
    Should Be Equal As Integers    ${count}    2

    ${rm}=        Run nsctl    repoclass    --path    ${dir}    access    remove    ui
    Should Be Equal As Integers    ${rm.rc}    0
    ${gone}=      Run nsctl    repoclass    --path    ${dir}    access    remove    ui
    Should Be Equal As Integers    ${gone.rc}    2

Access Refuses An Unknown Placeholder
    [Documentation]    NERD023 SPEC004: an unknown substitution is refused
    ...                rather than emptied, because the alternative is a URL
    ...                with a hole in it or a truncated secret name.
    [Tags]    contract    nerd023
    ${dir}=       Create Scratch Repo
    Run nsctl    repoclass    --path    ${dir}    init    acme-ui    --description    An Acme UI
    ${r}=         Run nsctl    repoclass    --path    ${dir}    access    add    ui    --url    http://{nope}/
    Should Be Equal As Integers    ${r.rc}    2
    Should Contain    ${r.stderr}    unknown placeholder
    # A refused entry is not written.
    ${l}=         Run nsctl    repoclass    --path    ${dir}    access    list
    Should Contain    ${l.stdout}    No access declared

Access Refuses A Secret With Nothing To Look Up
    [Documentation]    NERD023 SPEC004: the store cannot be inferred, and a
    ...                secret names either a key or a resource output.
    [Tags]    contract    nerd023
    ${dir}=       Create Scratch Repo
    Run nsctl    repoclass    --path    ${dir}    init    acme-ui    --description    An Acme UI
    ${nostore}=   Run nsctl    repoclass    --path    ${dir}    access    add    ui
    ...    --url    http://x/    --secret-key    k
    Should Be Equal As Integers    ${nostore.rc}    2
    Should Contain    ${nostore.stderr}    --secret-store is required
    ${nokey}=     Run nsctl    repoclass    --path    ${dir}    access    add    ui
    ...    --url    http://x/    --secret-store    parameter-store
    Should Be Equal As Integers    ${nokey.rc}    2
    Should Contain    ${nokey.stderr}    names either a key or the resource output

Validate Refuses A Literal Credential In A Manifest
    [Documentation]    NERD023 SPEC005: a manifest names where a credential
    ...                lives and never holds one, because a manifest is a file
    ...                a developer edits and may commit. The authoring verbs
    ...                cannot produce this, so validate is what catches it.
    [Tags]    contract    nerd023
    ${dir}=       Create Scratch Repo
    ${manifest}=  Set Variable    ${dir}${/}meta-data${/}manifest.json
    Create Directory    ${dir}${/}meta-data
    Create File    ${manifest}    {"name":"acme-ui","description":"d","build":{},"access":[{"name":"ui","url":"http://x/","password":"hunter2"}]}
    Create File    ${dir}${/}meta-data${/}VERSION    0.1
    ${v}=         Run nsctl    repoclass    --path    ${dir}    validate
    Should Be Equal As Integers    ${v.rc}    1
    Should Contain    ${v.stdout}    is a literal credential
    Should Contain    ${v.stdout}    never holds one

Env Credentials Needs An HMD_HOME And Says So
    [Documentation]    NERD023 SPEC006. Reading where a credential lives is a
    ...                read of local state, so the only prerequisite is a home.
    [Tags]    contract    nerd023
    ${result}=    Run nsctl    env    credentials
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    HMD_HOME is not set

Env Credentials Reports Nothing Declared Without Failing
    [Documentation]    An environment whose classes declare no access is a
    ...                normal state, not an error, and the answer names the verb
    ...                that would change it.
    [Tags]    contract    nerd023
    ${home}=      Create Scratch Home
    Run nsctl In Home    ${home}    env    add    dev
    ${r}=         Run nsctl In Home    ${home}    env    credentials    dev
    Should Be Equal As Integers    ${r.rc}    0
    Should Contain    ${r.stdout}    declares how to reach it
    Should Contain    ${r.stdout}    nsctl repoclass access add

Env Credentials Refuses An Instance The Environment Does Not Declare
    [Documentation]    A typo in --instance is a typo, not an absence of
    ...                declarations: answering it with "nothing declares access"
    ...                sends the reader looking for the wrong thing.
    [Tags]    contract    nerd023
    ${home}=      Create Scratch Home
    Run nsctl In Home    ${home}    env    add    dev
    ${r}=         Run nsctl In Home    ${home}    env    credentials    dev    --instance    nope
    Should Be Equal As Integers    ${r.rc}    2
    Should Contain    ${r.stderr}    declares no instance named

Help Leads With What A First Run Needs
    [Documentation]    NERD023 SPEC002: sixteen commands in one alphabetical
    ...                block named no first command and put authd -- a mock
    ...                identity provider -- beside env as an equal.
    [Tags]    contract    nerd023
    ${result}=    Run nsctl    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    Start here:
    Should Contain    ${result.stdout}    nsctl quickstart
    # quickstart is first in its group, not third as the alphabet would have it.
    ${start}=     Get Line    ${result.stdout}    ${{ $result.stdout.splitlines().index('Start here:') + 1 }}
    Should Contain    ${start}    quickstart
    # Grouping is presentation only: every command is still listed.
    FOR    ${command}    IN    env    repo    repoclass    control-plane    authd    login    logout    whoami    version    stack    plugin    doctor    db    agent    artifact    lock    bom
        Should Contain    ${result.stdout}    ${command}
    END

Missing HMD_HOME Names Quickstart
    [Documentation]    NERD023 SPEC002: a refusal that states a precondition
    ...                without naming the command that satisfies it is
    ...                incomplete.
    [Tags]    contract    nerd023
    ${result}=    Run nsctl    env    list
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    nsctl quickstart

Quickstart Without A Terminal Prints The Sequence And Runs Nothing
    [Documentation]    NERD023 SPEC001: a wizard is a conversation and there is
    ...                nobody to have it with, so the useful answer is the
    ...                ordered list of commands -- which is also what CI wants.
    [Tags]    contract    nerd023
    ${result}=    Run nsctl    quickstart
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stdout}    stdin is not a terminal
    Should Contain    ${result.stdout}    nsctl doctor
    Should Contain    ${result.stdout}    nsctl env start local
    Should Contain    ${result.stdout}    nsctl repoclass detect
    Should Contain    ${result.stderr}    nothing was run and nothing was written

Detect Classifies A Repository And Writes Nothing
    [Documentation]    NERD009 SPEC009: the classification carries the file and
    ...                line each conclusion came from, and without --apply it
    ...                writes nothing.
    [Tags]    contract    nerd009    nerd023
    ${dir}=       Create Scratch Repo
    Create File    ${dir}${/}Makefile    deploy:\n\t./scripts/ship.sh\n
    Create File    ${dir}${/}README.md    Routes public traffic.\n
    ${d}=         Run nsctl    repoclass    detect    --path    ${dir}    --json
    Should Be Equal As Integers    ${d.rc}    0
    Should Contain    ${d.stdout}    "mechanism": "external"
    Should Contain    ${d.stdout}    "value": "make deploy"
    Should Contain    ${d.stdout}    Makefile:1
    Should Not Exist    ${dir}${/}meta-data${/}manifest.json

Detect Refuses To Infer Dependencies Or Resources
    [Documentation]    NERD009 SPEC010, and the assertions that matter most: a
    ...                wrong required role fails the entire ChangeSet naming only
    ...                the role, so the refusals are reported explicitly and
    ...                --apply writes neither.
    [Tags]    contract    nerd009    nerd023
    ${dir}=       Create Scratch Repo
    Create File    ${dir}${/}Makefile    deploy:\n\t./ship\n
    Create File    ${dir}${/}README.md    Routes public traffic.\n
    Create File    ${dir}${/}docker-compose.yml    services:\n${SPACE}${SPACE}web:\n${SPACE}${SPACE}${SPACE}${SPACE}image: nginx\n
    ${d}=         Run nsctl    repoclass    detect    --path    ${dir}    --json
    Should Contain    ${d.stdout}    "field": "deploy.dependencies"
    Should Contain    ${d.stdout}    "confidence": "refused"

    ${a}=         Run nsctl    repoclass    detect    --path    ${dir}    --apply
    Should Be Equal As Integers    ${a.rc}    0
    ${manifest}=  Get File    ${dir}${/}meta-data${/}manifest.json
    Should Contain    ${manifest}    "mechanism": "external"
    Should Not Contain    ${manifest}    dependencies
    Should Not Contain    ${manifest}    resources
    Should Not Contain    ${manifest}    discovery
    Should Not Exist    ${dir}${/}meta-data${/}resources
    # What it wrote validates, which is the point of writing only what is
    # unambiguous.
    ${v}=         Run nsctl    repoclass    validate    --path    ${dir}
    Should Be Equal As Integers    ${v.rc}    0

Detect Refuses To Apply Without A Description
    [Documentation]    BACON requires a non-empty description, so writing a
    ...                manifest without one produces a file that cannot
    ...                validate. Refusing and naming the flag is better.
    [Tags]    contract    nerd009    nerd023
    ${dir}=       Create Scratch Repo
    Create File    ${dir}${/}Procfile    web: node server.js\n
    ${a}=         Run nsctl    repoclass    detect    --path    ${dir}    --apply
    Should Be Equal As Integers    ${a.rc}    2
    Should Contain    ${a.stderr}    --description
    Should Not Exist    ${dir}${/}meta-data${/}manifest.json
    ${ok}=        Run nsctl    repoclass    detect    --path    ${dir}    --apply    --description    An edge service
    Should Be Equal As Integers    ${ok.rc}    0
    ${v}=         Run nsctl    repoclass    validate    --path    ${dir}
    Should Be Equal As Integers    ${v.rc}    0

Detect Reports A PaaS Deploy And Stops
    [Documentation]    NERD009 SPEC009: there is no local equivalent of a
    ...                platform deploy, and saying so is the honest answer.
    [Tags]    contract    nerd009    nerd023
    ${dir}=       Create Scratch Repo
    Create File    ${dir}${/}fly.toml    app = "edge"\n
    Create File    ${dir}${/}README.md    Routes public traffic.\n
    ${d}=         Run nsctl    repoclass    detect    --path    ${dir}
    Should Be Equal As Integers    ${d.rc}    0
    Should Contain    ${d.stdout}    No deploy mechanism could be decided
    Should Contain    ${d.stdout}    no local equivalent

Detect On An Existing Repo Class Names Describe
    [Tags]    contract    nerd009    nerd023
    ${dir}=       Create Scratch Repo
    Run nsctl    repoclass    --path    ${dir}    init    acme-x    --description    A thing
    ${d}=         Run nsctl    repoclass    detect    --path    ${dir}
    Should Be Equal As Integers    ${d.rc}    0
    Should Contain    ${d.stdout}    already a repo class
    ${a}=         Run nsctl    repoclass    detect    --path    ${dir}    --apply
    Should Be Equal As Integers    ${a.rc}    2

Adopt Skill Is Bundled Now That Detect Exists
    [Documentation]    NERD015 SPEC001 withheld nsctl-repoclass-adopt until
    ...                repoclass detect existed; NERD023 SPEC008 builds it.
    [Tags]    contract    nerd015    nerd023
    ${dir}=       Create Scratch Repo
    ${l}=         Run nsctl    agent    skills    list    --path    ${dir}
    Should Be Equal As Integers    ${l.rc}    0
    Should Contain    ${l.stdout}    nsctl-repoclass-adopt
    ${i}=         Run nsctl    agent    skills    install    nsctl-repoclass-adopt    --host    all    --path    ${dir}
    Should Be Equal As Integers    ${i.rc}    0
    Should Exist    ${dir}${/}.claude${/}skills${/}nsctl-repoclass-adopt${/}SKILL.md
    Should Exist    ${dir}${/}.agents${/}skills${/}nsctl-repoclass-adopt${/}SKILL.md
    ${body}=      Get File    ${dir}${/}.claude${/}skills${/}nsctl-repoclass-adopt${/}SKILL.md
    Should Contain    ${body}    repoclass detect
    Should Contain    ${body}    --json
    Should Contain    ${body}    explicit confirmation

Dns Install Prints A Sudo Line And Runs Nothing
    [Documentation]    nsctl prints the one privileged step and does not take
    ...                it. It needs root, and it changes a file that belongs to
    ...                the user -- the same posture NERD023 takes toward a shell
    ...                profile. NERD026 SPEC002.
    ${before}=    Run Keyword And Return Status    Directory Should Exist    /etc/resolver
    ${result}=    Run nsctl    dns    install
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    sudo
    Should Contain    ${result.stdout}    does not run it for you
    # Whatever the platform's step is, nsctl must not have taken it.
    ${after}=    Run Keyword And Return Status    Directory Should Exist    /etc/resolver
    Should Be Equal    ${before}    ${after}    msg=dns install must not create /etc/resolver itself

Dns Install Scopes The Resolver To The Suffix Itself
    [Documentation]    A resolver file captures its whole subtree, so scoping it
    ...                to the parent would capture something that is not ours --
    ...                every mDNS name on the machine, under `local`. The narrow
    ...                suffix is a correctness requirement, not tidiness.
    ...                NERD026 SPEC002.
    ...
    ...                The privileged step itself is platform-specific -- a
    ...                resolver file on macOS, a systemd-resolved routing domain
    ...                on Linux -- so this asserts the scoping it states rather
    ...                than the shape of one platform's command. Asserting
    ...                /etc/resolver here is what failed this suite on Linux.
    ${result}=    Run nsctl    dns    install
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    It is scoped to
    Should Contain    ${result.stdout}    not to
    # Named for the suffix, never for the subtree above it.
    Should Contain    ${result.stdout}    ns.local
    Should Not Contain    ${result.stdout}    scoped to local,

Dns Install Prints The Port This Home Serves
    [Documentation]    The resolver's port is chosen when the default is taken
    ...                (NERD025 SPEC008), and a resolver file pointing at a port
    ...                nothing listens on fails silently and adds latency to
    ...                every lookup in the suffix.
    ${home}=    Create Scratch Home
    ${registry}=    Join Path    ${home}    .cache    neuronsphere    environments.json
    Create File    ${registry}    {"control_plane": {"ports": {"dns": 19154}}, "environments": {}, "version": 1}
    ${result}=    Run nsctl In Home    ${home}    dns    install
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    19154
    Should Not Contain    ${result.stdout}    19153

Dns Status Tells The Two Failures Apart
    [Documentation]    A resolver that is not running and a machine that is not
    ...                pointed at one need opposite fixes. One message naming
    ...                `dns install` for both sends a user whose control plane is
    ...                down to edit a file that was already correct.
    ...                NERD026 SPEC003.
    ${home}=    Create Scratch Home
    ${registry}=    Join Path    ${home}    .cache    neuronsphere    environments.json
    Create File    ${registry}    {"control_plane": {"ports": {"dns": 19154}}, "environments": {}, "version": 1}
    ${result}=    Run nsctl In Home    ${home}    dns    status
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    control-plane start
    Should Contain    ${result.stdout}    19154

Dns Status Probes A Name Nothing Has Deployed
    [Documentation]    Resolving a name that was never configured anywhere is the
    ...                property a hosts file cannot have, and therefore the one
    ...                worth asserting. NERD026 SPEC003.
    ${home}=    Create Scratch Home
    ${result}=    Run nsctl In Home    ${home}    dns    status
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    wildcard-probe

Db Upgrade Is Listed And Described
    [Documentation]    The repair for what the start pre-flight refuses has to
    ...                be findable from `nsctl db --help`, which is where
    ...                someone arrives after reading that refusal.
    [Tags]    contract    pgupgrade
    ${result}=    Run nsctl    db    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    upgrade
    ${upgrade}=    Run nsctl    db    upgrade    --help
    Should Be Equal As Integers    ${upgrade.rc}    0
    Should Contain    ${upgrade.stdout}    --dry-run
    Should Contain    ${upgrade.stdout}    --yes
    Should Contain    ${upgrade.stdout}    --volume
    # --force must say what it cannot do, because that limit is the design.
    Should Contain    ${upgrade.stdout}    Cannot bypass

Db Upgrade Without HMD_HOME Exits Two
    [Documentation]    A command that rewrites databases must not guess which
    ...                home's databases it meant.
    [Tags]    contract    pgupgrade
    ${result}=    Run nsctl    db    upgrade    --dry-run
    Should Be Equal As Integers    ${result.rc}    2
    Should Contain    ${result.stderr}    HMD_HOME

Db Upgrade Without An Engine Fails Cleanly And Writes Nothing
    [Documentation]    The failure names Docker rather than surfacing as an
    ...                empty result, and a run that could not reach an engine
    ...                leaves no state behind in the home it was given.
    [Tags]    contract    pgupgrade
    ${home}=    Create Scratch Home
    ${result}=    Run nsctl In Home Without An Engine    ${home}    db    upgrade    --dry-run
    Should Be Equal As Integers    ${result.rc}    1
    Should Contain    ${result.stderr}    docker
    Directory Should Not Exist    ${home}${/}floci
