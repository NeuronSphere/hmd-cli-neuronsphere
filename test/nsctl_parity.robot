*** Settings ***
Documentation     SPEC012's parity harness: the acceptance test the SPEC states,
...               which is "perform the operation with one front end, verify with
...               the other".
...
...               Parity was checked by hand through Phases 1-7. This makes the
...               checks repeatable for the four things both front ends are
...               expected to agree about -- `env start`, `env stop`, `status`
...               and the registry -- and no more than that. Full parity across
...               every verb is a much larger suite, and SPEC012 records that
...               only this core was built.
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
