*** Settings ***
Documentation     SPEC012's parity harness: the acceptance test the SPEC states,
...               which is "perform the operation with one front end, verify with
...               the other".
...
...               Parity was checked by hand through Phases 1-7. This makes the
...               checks repeatable for the five things both front ends are
...               expected to agree about -- `env start`, `env stop`, `status`,
...               `env purge` and the registry -- plus `env add`/`env delete`
...               and the slug rules, which cost a registry write and no
...               provisioning. Still uncovered, and named rather than rounded
...               up: `env apply`, `env attach`, the four `repo` verbs,
...               `control-plane start|stop|status`, and `authd`. Full parity
...               across every verb is a much larger suite, and SPEC012 records
...               that only this much was built.
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
# 20 minutes was not enough, and the first complete run of this suite found out
# by having Robot SIGKILL `hmd neuronsphere up` at rc -9 mid-deploy -- which
# reads as a parity failure and is a stopwatch. A cold `up` from the Python
# front end deploys its own LOCAL_BOM through projectbuilder and is the slowest
# thing either CLI does.
${CLI TIMEOUT}    45 min
# The Python's full-BOM `up` is opt-in; see its test for the measurement.
${DEEP}           ${False}
# A slug the registry tests may create and destroy freely. Deliberately not
# ${ENV}: a test that can unregister the environment the rest of the suite
# starts and stops is a test that can take the suite with it, which is how the
# first complete run failed.
${SCRATCH}        parity-tmp
# Two rule violations in one string -- an uppercase character and an
# underscore -- so a front end enforcing only half of SPEC003's pattern still
# refuses it. Deliberately no leading hyphen: argparse would take one for an
# option and fail before the slug was ever validated, which is a nonzero exit
# for the wrong reason and exactly the kind of pass this suite has been caught
# on before.
${BAD SLUG}       Parity_Bad

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
    ${result}=    Run Process    ${BINARY}    @{args}    stderr=STDOUT    timeout=${CLI TIMEOUT}
    Log    ${result.stdout}
    RETURN    ${result}

Run hmd neuronsphere
    [Arguments]    @{args}
    ${result}=    Run Process    ${HMD}    neuronsphere    @{args}    stderr=STDOUT    timeout=${CLI TIMEOUT}
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
    ...
    ...                Line-anchored, not a substring search. The Python's
    ...                empty-state message is "No local environments yet", so a
    ...                plain `Should Not Contain ... local` reports the default
    ...                environment as still listed *because* the listing is
    ...                empty. That is how the first complete run of this suite
    ...                failed, and it took the failure with it: the rebuild in
    ...                the test body never ran, so the next test's `down --purge`
    ...                correctly refused an environment that no longer existed.
    ...                A listing row starts with the slug; prose about it does
    ...                not.
    [Arguments]    ${slug}
    ${ours}=      Run nsctl               env    list
    ${theirs}=    Run hmd neuronsphere    env    list
    Should Not Match Regexp    ${ours.stdout}      (?im)^\\s*[*-]?\\s*${slug}\\b
    Should Not Match Regexp    ${theirs.stdout}    (?im)^\\s*[*-]?\\s*${slug}\\b

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
    ...
    ...                This test said exactly that and then did the opposite. It
    ...                asserted the environment's name appeared in *both*
    ...                outputs, which is a rendering detail the paragraph above
    ...                disclaims -- and the Python's extend-mode status does not
    ...                print it, rendering `Mode: extend` and the registered
    ...                HMDMS services instead. The first complete run of this
    ...                suite failed here, on the assertion rather than on the
    ...                platform. Written and never run, like the six items whose
    ...                verification produced this suite.
    ...
    ...                So: nsctl's rendering is still checked for the name,
    ...                because nsctl does print it and a status that names the
    ...                wrong environment is a real failure. The Python is
    ...                checked for its verdict, which is the only thing both are
    ...                claimed to agree about.
    [Tags]    parity    docker
    ${ours}=      Run nsctl               env    status    ${ENV}
    ${theirs}=    Run hmd neuronsphere    status    --env    ${ENV}
    Should Be Equal As Integers    ${ours.rc}      0
    Should Be Equal As Integers    ${theirs.rc}    0
    Should Contain        ${ours.stdout}      ${ENV}
    Should Not Contain    ${theirs.stdout}    not running

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
    ...
    ...                Opt-in, because the two start verbs are not the same size.
    ...                `nsctl env start` brings up the substrate and stops --
    ...                every workload above it is a RepoClass a user adds.
    ...                `hmd neuronsphere up` seeds its own plugin-derived BOM and
    ...                deploys the lot: airflow, argo, the transform broker,
    ...                cert-manager, trino, superset. Measured on the machine
    ...                this suite was first run to completion on, that is over
    ...                45 minutes against nsctl's three and a half, and Robot
    ...                SIGKILLed it at both 20 and 45 -- reporting rc -9, which
    ...                reads as a parity failure and is a stopwatch.
    ...
    ...                So it is skipped unless asked for. A test that cannot pass
    ...                is worse than one that says why it did not run, and
    ...                burying half an hour inside a suite people are told to run
    ...                is how a suite stops being run at all. Ask for it with
    ...                `-v DEEP:True`, and give it an hour.
    [Tags]    parity    docker    destructive    slow
    Skip If    not ${DEEP}
    ...    msg=Set -v DEEP:True to run this: `hmd neuronsphere up` deploys its whole default BOM and took over 45 minutes here.
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

    Registry Should Not List               ${ENV}
    Nothing Docker Holds Should Mention    ${ENV}
    [Teardown]    Rebuild The Environment    ${ENV}

An Environment hmd Purged Is Gone To nsctl
    [Documentation]    The reverse, and the two verbs turn out not to be the same
    ...                operation -- which this test asserted before it was run,
    ...                and which is worth having found.
    ...
    ...                `nsctl env purge` destroys an environment AND unregisters
    ...                it: SPEC001 made it "the teardown `env delete` refuses to
    ...                be". `hmd neuronsphere down --purge --env` destroys the
    ...                state and KEEPS the registration, so the environment can
    ...                be brought back with another `up`. Both are defensible and
    ...                they are not interchangeable.
    ...
    ...                What must still agree is everything else: the resources
    ...                are gone from Docker, and both front ends read the same
    ...                registry -- so an environment the Python left registered
    ...                is one nsctl still lists. That is the parity claim; the
    ...                symmetry of the two verbs never was one.
    [Tags]    parity    docker    destructive
    ${purged}=    Run hmd neuronsphere    down    --purge    --env    ${ENV}    --yes
    Should Be Equal As Integers    ${purged.rc}    0

    Nothing Docker Holds Should Mention    ${ENV}
    Registry Should List                   ${ENV}
    [Teardown]    Run nsctl    env    start    ${ENV}

An Environment nsctl Registered Is An Environment hmd Reads
    [Documentation]    `env add` and `env delete` were outside the five states
    ...                this suite covered, and they are the cheapest parity
    ...                claim there is: `nsctl env add` says of itself that it
    ...                "only writes the registry", so both directions cost a
    ...                registry write and no provisioning at all.
    ...
    ...                That is what makes this worth having. The registry is
    ...                SPEC003's whole subject -- every derived value written
    ...                once at create time and never recomputed -- so a slug one
    ...                front end registers and the other cannot see is the
    ...                failure SPEC003 exists to prevent, and until now nothing
    ...                asked.
    ...
    ...                A scratch slug, not ${ENV}: this test must not be able to
    ...                unregister the environment the rest of the suite starts
    ...                and stops.
    ...
    ...                `env delete` takes `--yes`. It refuses without one (exit
    ...                2, naming the account and port slot it would free), which
    ...                is the right default for a verb that frees a slot another
    ...                environment will later be given -- and is the first thing
    ...                running this test found, by leaving ${SCRATCH} registered
    ...                when the teardown omitted the flag too.
    [Tags]    parity    registry
    [Teardown]    Run Keyword And Ignore Error    Run nsctl    env    delete    ${SCRATCH}    --yes
    ${added}=    Run nsctl    env    add    ${SCRATCH}
    Should Be Equal As Integers    ${added.rc}    0

    Registry Should List    ${SCRATCH}

    ${refused}=    Run nsctl    env    delete    ${SCRATCH}
    Should Not Be Equal As Integers    ${refused.rc}    0
    Registry Should List    ${SCRATCH}

    ${deleted}=    Run nsctl    env    delete    ${SCRATCH}    --yes
    Should Be Equal As Integers    ${deleted.rc}    0

    Registry Should Not List    ${SCRATCH}

Both Front Ends Refuse The Same Bad Slug
    [Documentation]    SPEC003 fixes the slug rules -- `^[a-z0-9][a-z0-9-]{0,15}$`
    ...                plus a reserved set -- and says nsctl must preserve them
    ...                exactly, because the registry's names are derived from the
    ...                slug once and never recomputed. A slug one front end
    ...                accepts and the other rejects is how two front ends stop
    ...                agreeing about what exists.
    ...
    ...                Both must refuse; neither is asked to phrase it the same
    ...                way. A nonzero exit and no registration is the whole
    ...                claim.
    [Tags]    parity    registry
    ${ours}=    Run nsctl    env    add    ${BAD SLUG}
    Should Not Be Equal As Integers    ${ours.rc}    0

    ${theirs}=    Run hmd neuronsphere    env    create    ${BAD SLUG}    --no-deploy
    Should Not Be Equal As Integers    ${theirs.rc}    0

    Registry Should Not List    ${BAD SLUG}
