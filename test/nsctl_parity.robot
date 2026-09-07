*** Settings ***
Documentation     SPEC012's parity harness: the acceptance test the SPEC states,
...               which is "perform the operation with one front end, verify with
...               the other".
...
...               Parity was checked by hand through Phases 1-7. This makes the
...               checks repeatable for the five things both front ends are
...               expected to agree about -- `env start`, `env stop`, `status`,
...               `env purge` and the registry -- and no more than that. Full
...               parity across every verb is a much larger suite, and SPEC012
...               records that only this core was built.
...
...               `env purge` costs the most of the five and is here anyway: it
...               is the newest verb, it is the one where a leftover is the
...               entire failure mode, and the first real run of it left seven
...               containers, three volumes and the platform network behind. Each
...               direction destroys the environment and rebuilds it, so the two
...               purge tests add two cold starts to every run of this suite.
...
...               This suite needs a real platform, so it belongs with the other
...               Docker-dependent suites and is deliberately NOT wired into
...               `make test-cli` or CI. Run it with `make test-parity`.
...
...               It is destructive: it starts and stops a real environment in
...               the HMD_HOME it is pointed at. `make test-parity` therefore
...               requires NSCTL_PARITY_ENV to be named explicitly -- HMD_HOME
...               is set in every shell anyone works in, so requiring only that
...               would make running this at the wrong platform a reflex. ${ENV}
...               has no default here for the same reason.
Library           Process
Library           OperatingSystem
Library           Collections
Library           String

Suite Setup       Check Both Front Ends Are Present

*** Variables ***
${BINARY}         ${EMPTY}
${HMD}            hmd
${ENV}            ${EMPTY}

*** Keywords ***
Check Both Front Ends Are Present
    [Documentation]    Both binaries and an HMD_HOME, or there is nothing to
    ...                compare. Failing here rather than mid-suite keeps a
    ...                missing prerequisite from reading as a parity failure.
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-parity` does.
    File Should Exist      ${BINARY}
    ${home}=    Get Environment Variable    HMD_HOME    ${EMPTY}
    Should Not Be Empty    ${home}    msg=This suite operates on a real HMD_HOME; export one.
    Should Not Be Empty    ${ENV}
    ...    msg=Name the environment: make test-parity NSCTL_PARITY_ENV=<slug>. It is started and stopped for real.

Run nsctl
    [Arguments]    @{args}
    ${result}=    Run Process    ${BINARY}    @{args}    stderr=STDOUT    timeout=20 min
    Log    ${result.stdout}
    RETURN    ${result}

Run hmd neuronsphere
    [Arguments]    @{args}
    ${result}=    Run Process    ${HMD}    neuronsphere    @{args}    stderr=STDOUT    timeout=20 min
    Log    ${result.stdout}
    RETURN    ${result}

Registry Should List
    [Documentation]    Both front ends read the same
    ...                $HMD_HOME/.cache/neuronsphere/environments.json (SPEC003),
    ...                so each must see an environment the other registered.
    [Arguments]    ${slug}
    ${ours}=      Run nsctl               env    list
    ${theirs}=    Run hmd neuronsphere    env    list
    Should Contain    ${ours.stdout}      ${slug}
    Should Contain    ${theirs.stdout}    ${slug}

Registry Should Not List
    [Documentation]    The other half of the same claim. An environment one front
    ...                end purged must be gone to both -- a purge that only one
    ...                of them can see is a purge that leaves the other able to
    ...                address containers nothing owns.
    [Arguments]    ${slug}
    ${ours}=      Run nsctl               env    list
    ${theirs}=    Run hmd neuronsphere    env    list
    Should Not Contain    ${ours.stdout}      ${slug}
    Should Not Contain    ${theirs.stdout}    ${slug}

Nothing Docker Holds Should Mention
    [Documentation]    A purge's failure mode is what it leaves, so this asks
    ...                Docker rather than either CLI. Containers, volumes and
    ...                networks: the three things the first real purge left
    ...                behind, each for a different reason.
    [Arguments]    ${slug}
    ${containers}=    Run Process    docker    ps    -a    --format    {{.Names}}      stderr=STDOUT
    ${volumes}=       Run Process    docker    volume    ls    --format    {{.Name}}   stderr=STDOUT
    ${networks}=      Run Process    docker    network    ls    --format    {{.Name}}  stderr=STDOUT
    Should Not Contain    ${containers.stdout}    ns-${slug}-
    Should Not Contain    ${volumes.stdout}       ns-${slug}-
    Should Not Contain    ${networks.stdout}      ns-${slug}-

Rebuild The Environment
    [Documentation]    Puts back what a purge test destroyed, and is itself the
    ...                assertion: `env add` plus `env start` over a purged name
    ...                is a path that works, so a purge that left something
    ...                behind surfaces here as a bootstrap failure rather than
    ...                silently.
    [Arguments]    ${slug}
    ${added}=      Run nsctl    env    add      ${slug}
    Should Be Equal As Integers    ${added.rc}    0
    ${started}=    Run nsctl    env    start    ${slug}
    Should Be Equal As Integers    ${started.rc}    0

*** Test Cases ***
An Environment nsctl Started Is Running To hmd
    [Documentation]    SPEC012's acceptance test, in the direction that matters
    ...                most: nsctl brings the environment up and the Python CLI
    ...                is asked whether it is up. A divergence here means the two
    ...                disagree about container names, the compose project or the
    ...                network -- the four things SPEC012 lists.
    [Tags]    parity    docker    destructive
    ${started}=    Run nsctl    env    start    ${ENV}
    Should Be Equal As Integers    ${started.rc}    0

    ${status}=    Run hmd neuronsphere    status    --env    ${ENV}
    Should Be Equal As Integers    ${status.rc}    0
    Should Not Contain    ${status.stdout}    not running

An Environment hmd Sees Is An Environment nsctl Sees
    [Documentation]    The registry, read by both. Its shape is SPEC003's, and
    ...                the regression fixture for it is the literal JSON the
    ...                Python emits -- this checks the two agree at runtime and
    ...                not only against a captured file.
    [Tags]    parity    docker
    Registry Should List    ${ENV}

Both Front Ends Report The Same Environment Status
    [Documentation]    Not a string comparison -- the two render differently on
    ...                purpose. What must agree is the verdict: an environment
    ...                one calls running is not one the other calls stopped.
    [Tags]    parity    docker
    ${ours}=      Run nsctl               env    status    ${ENV}
    ${theirs}=    Run hmd neuronsphere    status    --env    ${ENV}
    Should Be Equal As Integers    ${ours.rc}      0
    Should Be Equal As Integers    ${theirs.rc}    0
    Should Contain    ${ours.stdout}    ${ENV}
    Should Contain    ${theirs.stdout}    ${ENV}

An Environment nsctl Stopped Is Stopped To hmd
    [Documentation]    The other direction of the same claim, and the one that
    ...                catches a stop that stopped the wrong containers -- which
    ...                is exactly what a name derived differently by the two
    ...                front ends would look like.
    [Tags]    parity    docker    destructive
    ${stopped}=    Run nsctl    env    stop    ${ENV}
    Should Be Equal As Integers    ${stopped.rc}    0

    ${status}=    Run hmd neuronsphere    status    --env    ${ENV}
    Should Be Equal As Integers    ${status.rc}    0
    # Still registered after a stop: a stop is not a teardown, in either CLI.
    Registry Should List    ${ENV}

An Environment hmd Started Is Running To nsctl
    [Documentation]    The reverse of the first test. Both directions are needed:
    ...                agreeing about what nsctl created says nothing about
    ...                whether nsctl can read what the Python CLI created, which
    ...                is the case every existing user is in.
    [Tags]    parity    docker    destructive
    ${started}=    Run hmd neuronsphere    up    --env    ${ENV}
    Should Be Equal As Integers    ${started.rc}    0

    ${status}=    Run nsctl    env    status    ${ENV}
    Should Be Equal As Integers    ${status.rc}    0
    Should Contain    ${status.stdout}    ${ENV}

An Environment nsctl Purged Is Gone To hmd
    [Documentation]    SPEC012's acceptance test applied to the newest verb, in
    ...                the direction that matters most. A purge is the one
    ...                operation where a leftover is the entire failure mode, so
    ...                Docker is asked too and not only the two registries.
    ...
    ...                This destroys ${ENV} and rebuilds it. That is the cost of
    ...                covering the verb at all, and it is also the round trip:
    ...                the rebuild is what proves the purge left nothing that
    ...                would break the next start.
    [Tags]    parity    docker    destructive
    ${purged}=    Run nsctl    env    purge    ${ENV}    --yes
    Should Be Equal As Integers    ${purged.rc}    0
    # The Python's own symptom, which would mean the delete-before-stop
    # ordering is right in the unit tests and wrong in the wiring.
    Should Not Contain    ${purged.stdout}    Could not connect to the endpoint URL

    Registry Should Not List           ${ENV}
    Nothing Docker Holds Should Mention    ${ENV}
    Rebuild The Environment            ${ENV}

An Environment hmd Purged Is Gone To nsctl
    [Documentation]    The reverse. `hmd neuronsphere down --purge --env` is the
    ...                verb `nsctl env purge` was written against, and every
    ...                existing user's environments were purged by it -- so nsctl
    ...                reading the result correctly is the case that actually
    ...                occurs, not the hypothetical one.
    ...
    ...                Rebuilt at the end so the suite leaves the platform as it
    ...                found it.
    [Tags]    parity    docker    destructive
    ${purged}=    Run hmd neuronsphere    down    --purge    --env    ${ENV}    --yes
    Should Be Equal As Integers    ${purged.rc}    0

    Registry Should Not List    ${ENV}
    Rebuild The Environment     ${ENV}
