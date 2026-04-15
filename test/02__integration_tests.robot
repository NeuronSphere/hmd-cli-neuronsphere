*** Settings ***
Documentation     Integration tests for NeuronSphere start/stop and plugin combinations.
...               All integration tests are in a single suite so Pabot does not
...               parallelize them -- they share Docker ports and the neuronsphere_default network.
Library           resources/NeuronSphereLib.py
Library           resources/MiniStackLib.py
Variables         variables/plugin_containers.py

Suite Setup       Ensure Clean State
Suite Teardown    Teardown NeuronSphere

*** Keywords ***
Ensure Clean State
    [Documentation]    Stop any running NeuronSphere and clear plugin env before suite.
    Ensure HMD Environment
    Stop Local NeuronSphere
    Clear Plugin Environment

Teardown NeuronSphere
    [Documentation]    Guarantee NeuronSphere is stopped after suite completes.
    Stop Local NeuronSphere
    Clear Plugin Environment

Verify Plugin Running Containers
    [Documentation]    Wait for all running containers of a plugin to be up.
    [Arguments]    ${container_list}
    Wait Until Keyword Succeeds    5 min    30 sec
    ...    All Containers Should Be Running    ${container_list}

Verify Plugin Init Containers
    [Documentation]    Wait for all init containers of a plugin to complete.
    [Arguments]    ${container_list}
    Wait Until Keyword Succeeds    5 min    30 sec
    ...    All Init Containers Should Have Completed    ${container_list}

Verify Plugin Not Running
    [Documentation]    Assert none of the plugin's containers are running.
    [Arguments]    ${container_list}
    All Containers Should Not Be Running    ${container_list}

Start With Plugins
    [Documentation]    Set plugin env, start NeuronSphere, clear env for next test.
    [Arguments]    @{plugins}
    Set Plugin Environment    ${plugins}
    Start Local NeuronSphere

Stop And Clear
    [Documentation]    Stop NeuronSphere and clear plugin env vars.
    Stop Local NeuronSphere
    Clear Plugin Environment

*** Test Cases ***
# ═══════════════════════════════════════════════════════════════
# Default Start / Stop
# ═══════════════════════════════════════════════════════════════

Start Default NeuronSphere
    [Tags]    integration
    [Documentation]    Start NeuronSphere with all default plugins enabled.
    ...               Init containers (db_init) may fail on a fresh HMD_HOME due to
    ...               postgres startup timing. Only assert long-running containers.
    Start Local NeuronSphere
    Verify Plugin Running Containers    ${MAIN_RUNNING_CONTAINERS}

Default Plugin Containers Are Running
    [Tags]    integration
    [Documentation]    Verify containers for each default-enabled plugin group.
    ...               Trino, clickhouse, and hive_metastore depend on external build
    ...               artifacts (from hmd build -pdo) and are tested via the Data
    ...               Pipeline Stack combo test instead.
    ...               MiniStack replaces minio and dynamodb containers.
    Verify Plugin Running Containers    ${TELEMETRY_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${GRAPH_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${MINISTACK_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${JUPYTER_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${APACHE_SUPERSET_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${AIRFLOW_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${TRANSFORM_RUNNING_CONTAINERS}

Stop Default NeuronSphere
    [Tags]    integration
    [Documentation]    Stop NeuronSphere and verify all containers are down.
    Stop Local NeuronSphere
    Verify Plugin Not Running    ${MAIN_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${TELEMETRY_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${GRAPH_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${MINISTACK_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${JUPYTER_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${APACHE_SUPERSET_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${AIRFLOW_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${TRANSFORM_RUNNING_CONTAINERS}

# ═══════════════════════════════════════════════════════════════
# Plugin Combinations
# ═══════════════════════════════════════════════════════════════

Minimal Setup Main Only
    [Tags]    integration    plugin-combo
    [Documentation]    Start with all optional plugins disabled. Only main containers should run.
    [Teardown]    Stop And Clear
    Start With Plugins
    Verify Plugin Running Containers    ${MAIN_RUNNING_CONTAINERS}
    Verify Plugin Init Containers       ${MAIN_INIT_CONTAINERS}
    # Verify optional plugins are NOT running
    Verify Plugin Not Running    ${TELEMETRY_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${MINIO_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${GRAPH_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${AIRFLOW_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${TRINO_RUNNING_CONTAINERS}

Main Plus MiniStack
    [Tags]    integration    plugin-combo
    [Documentation]    Start with only MiniStack enabled alongside main.
    [Teardown]    Stop And Clear
    Start With Plugins    ministack
    Verify Plugin Running Containers    ${MAIN_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${MINISTACK_RUNNING_CONTAINERS}
    # Verify others are NOT running
    Verify Plugin Not Running    ${TELEMETRY_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${GRAPH_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${AIRFLOW_RUNNING_CONTAINERS}

Main Plus Telemetry
    [Tags]    integration    plugin-combo
    [Documentation]    Start with only telemetry enabled alongside main.
    [Teardown]    Stop And Clear
    Start With Plugins    telemetry
    Verify Plugin Running Containers    ${MAIN_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${TELEMETRY_RUNNING_CONTAINERS}
    # Verify others are NOT running
    Verify Plugin Not Running    ${MINIO_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${GRAPH_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${AIRFLOW_RUNNING_CONTAINERS}

Data Pipeline Stack
    [Tags]    integration    plugin-combo
    [Documentation]    Start with the full data pipeline: ministack, graph, airflow, trino,
    ...               and transform. Transform depends on graph and airflow.
    [Teardown]    Stop And Clear
    Start With Plugins    ministack    graph    airflow    trino    transform
    Verify Plugin Running Containers    ${MAIN_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${MINISTACK_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${GRAPH_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${AIRFLOW_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${TRINO_RUNNING_CONTAINERS}
    Verify Plugin Running Containers    ${TRANSFORM_RUNNING_CONTAINERS}
    # Verify disabled plugins are NOT running
    Verify Plugin Not Running    ${TELEMETRY_RUNNING_CONTAINERS}
    Verify Plugin Not Running    ${JUPYTER_RUNNING_CONTAINERS}

# ═══════════════════════════════════════════════════════════════
# MiniStack Persistence
# ═══════════════════════════════════════════════════════════════

MiniStack S3 Data Persists Across Restart
    [Tags]    integration    persistence    ministack
    [Documentation]    Verify files in S3 survive a stop/start cycle.
    [Teardown]    Stop And Clear
    Clean MiniStack State
    Start With Plugins    ministack
    Verify Plugin Running Containers    ${MINISTACK_RUNNING_CONTAINERS}
    Wait Until S3 Is Ready
    Create S3 Bucket    persistence-test
    Upload Test File To S3    persistence-test    test-key    test-content-12345
    Verify S3 Object Exists    persistence-test    test-key
    Stop Local NeuronSphere
    Verify Plugin Not Running    ${MINISTACK_RUNNING_CONTAINERS}
    Start With Plugins    ministack
    Verify Plugin Running Containers    ${MINISTACK_RUNNING_CONTAINERS}
    Wait Until S3 Is Ready
    Verify S3 Object Exists    persistence-test    test-key
    Verify S3 Object Content    persistence-test    test-key    test-content-12345
