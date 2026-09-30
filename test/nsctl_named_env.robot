*** Settings ***
Documentation     A full `env start` and `env apply` in an environment that is not
...               named `local`.
...
...               Every repo-scoped environment has its own name, and three separate
...               defects hid behind "only `local` is ever tested", all found on
...               2026-09-30 in an environment called `scratch`:
...
...               - projectbuilder 0.5.389 shipped hmd-cli-dbaccount 0.1.7, whose
...                 deploy chose its local path by `environment == "local"`, so every
...                 hmd-database-account node died with
...                 `IndexError: list index out of range`;
...               - the ms-dbaccount service named the admin secret from its own
...                 HMD_ENVIRONMENT ("local"), while the substrate created it under
...                 the environment's name, so every account failed with
...                 "Secret ... not found in PS or SM";
...               - a cluster recreated under the same name kept the previous
...                 cluster's kubeconfig, so the first chart failed with
...                 401 Unauthorized.
...
...               Each is a name predicate or a stale per-name artifact that the
...               default environment can never exercise, so this suite creates a
...               fresh environment under a name of its own and deploys through it.
...
...               It needs Docker, a bootstrapped control plane and a real HMD_HOME,
...               and it creates, starts and (by default) purges an environment in
...               that home, so it is not part of `make test-cli` or CI. Run it with
...               `make test-named-env NSCTL_NAMED_ENV=<slug>`. The slug must not be
...               `local` -- that is the name this suite exists not to use -- and
...               must not already be registered, because the first start of a new
...               environment is the path under test.
Library           Process
Library           OperatingSystem
Library           String
Library           resources/FlociLib.py

Suite Setup       Create The Environment
Suite Teardown    Purge The Environment

*** Variables ***
${BINARY}         ${EMPTY}
${ENV}            ${EMPTY}
# A cold start deploys the whole substrate through projectbuilder.
${CLI TIMEOUT}    45 min
# Keep the environment afterwards, for inspecting a failure.
${KEEP}           ${False}
# What the database-account test creates. Pinned to the version the analytics
# stack pins, so the artifact is usually already cached; `env apply --pull`
# fetches it otherwise.
${DB ACCOUNT CLASS}    hmd-database-account@0.1.3
${DB NAME}        nsctl_named_env

*** Keywords ***
Run nsctl
    [Arguments]    @{args}
    ${result}=    Run Process    ${BINARY}    @{args}    stderr=STDOUT    timeout=${CLI TIMEOUT}
    Log    ${result.stdout}
    RETURN    ${result}

Registry Value
    [Documentation]    One value from $HMD_HOME's registry, the file both front
    ...                ends read (SPEC003): `environments.<env>.<key>` or
    ...                `control_plane.<key>...`.
    [Arguments]    @{path}
    ${home}=     Get Environment Variable    HMD_HOME
    ${file}=     Set Variable    ${home}/.cache/neuronsphere/environments.json
    ${value}=    Evaluate    functools.reduce(lambda v, k: v[k], $path, json.load(open($file)))
    ...    modules=json,functools
    RETURN    ${value}

Create The Environment
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-named-env` does.
    File Should Exist      ${BINARY}
    ${home}=    Get Environment Variable    HMD_HOME    ${EMPTY}
    Should Not Be Empty    ${home}    msg=This suite operates on a real HMD_HOME; export one.
    Should Not Be Empty    ${ENV}    msg=Name the environment: make test-named-env NSCTL_NAMED_ENV=<slug>.
    Should Not Be Equal    ${ENV}    local
    ...    msg=The point of this suite is an environment not named `local`; choose another slug.
    ${listed}=    Run nsctl    env    list
    Should Not Match Regexp    ${listed.stdout}    (?m)^${ENV}\\b
    ...    msg=${ENV} is already registered; this suite needs the first start of a new environment.
    ${added}=    Run nsctl    env    add    ${ENV}
    Should Be Equal As Integers    ${added.rc}    0
    Set Suite Variable    ${ADD OUTPUT}    ${added.stdout}
    ${account}=    Registry Value    environments    ${ENV}    account_id
    ${did}=        Registry Value    environments    ${ENV}    deployment_id
    ${port}=       Registry Value    control_plane    ports    floci
    ${state}=      Registry Value    environments    ${ENV}    state_dir
    Set Suite Variable    ${ACCOUNT}     ${account}
    Set Suite Variable    ${DID}         ${did}
    Set Suite Variable    ${FLOCI}       http://localhost:${port}
    Set Suite Variable    ${STATE DIR}   ${state}

Purge The Environment
    IF    ${KEEP}
        Log    Kept ${ENV} (KEEP=True)    WARN
        RETURN
    END
    ${purged}=    Run nsctl    env    purge    ${ENV}    --yes
    Should Be Equal As Integers    ${purged.rc}    0

*** Test Cases ***
Adding It Does Not Warn About Its Name
    [Documentation]    With the default projectbuilder (0.5.390 or later) nothing in a
    ...                deploy decides "local" by the environment's name, so a new
    ...                environment with another name gets no warning about it.
    Should Not Contain    ${ADD OUTPUT}    is not named
    Should Not Contain    ${ADD OUTPUT}    tofu init

A Full Start Deploys The Substrate
    ${started}=    Run nsctl    env    start    ${ENV}
    Should Be Equal As Integers    ${started.rc}    0
    Should Not Contain    ${started.stdout}    Unauthorized

The Kubeconfig Is For This Cluster
    [Documentation]    Written by this start, for this cluster: a kubeconfig left by
    ...                an earlier cluster of the same name authenticates nowhere.
    ${nodes}=    Run Process    kubectl    --kubeconfig    ${STATE DIR}/k3s/kubeconfig
    ...    get    nodes    stderr=STDOUT    timeout=2 min
    Log    ${nodes.stdout}
    Should Be Equal As Integers    ${nodes.rc}    0
    Should Contain    ${nodes.stdout}    Ready

The Substrate's Database Secret Is Named After The Environment
    [Documentation]    ms-deployment passes the environment's own name as
    ...                --environment, so the substrate's database admin secret is
    ...                make_standard_name(environment-db, hmd-postgres-rds, <id>, <env>)
    ...                -- shortened when that reaches 64 characters, which a long
    ...                environment name does -- and nothing is named for `local`.
    ${base}=     Standard Name    environment-db    hmd-postgres-rds    ${DID}    ${ENV}
    ${local}=    Standard Name    environment-db    hmd-postgres-rds    ${DID}    local
    Set Suite Variable    ${DB SECRET BASE}    ${base}
    Floci Account Should Hold Secret        ${ACCOUNT}    ${FLOCI}    ${base}_db-secret
    Floci Account Should Not Hold Secret    ${ACCOUNT}    ${FLOCI}    ${local}_db-secret

A Database Account Deploys In It
    [Documentation]    The whole hmd-database-account path: the deploy node takes
    ...                the local path (hmd-cli-dbaccount >= 0.1.10), and the
    ...                environment's ms-dbaccount service finds the admin secret
    ...                under the environment's name and writes the new user's
    ...                secret beside it.
    ${added}=    Run nsctl    repo    add    ${DB ACCOUNT CLASS}    --env    ${ENV}
    ...    --name    named-env-db-account
    ...    --config    db_name=${DB NAME}    --config    username=${DB NAME}
    ...    --depends    database-instance=environment-db
    ...    --depends    create-service=local-neuronsphere
    Should Be Equal As Integers    ${added.rc}    0
    ${applied}=    Run nsctl    env    apply    ${ENV}    --pull
    Should Be Equal As Integers    ${applied.rc}    0
    Should Not Contain    ${applied.stdout}    IndexError
    Should Not Contain    ${applied.stdout}    not found in PS or SM
    Floci Account Should Hold Secret    ${ACCOUNT}    ${FLOCI}    ${DB SECRET BASE}_${DB NAME}

A Second Apply Changes Nothing
    ${again}=    Run nsctl    env    apply    ${ENV}
    Should Be Equal As Integers    ${again.rc}    0
    Should Match Regexp    ${again.stdout}    \\b0 to deploy, 0 changed\\b
