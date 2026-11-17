*** Settings ***
Documentation     `nsctl data storage` and `land` against a real local
...               environment (NERD039 SPEC009). Opt-in, like the parity
...               suite: it needs a running environment named by STORAGE_ENV,
...               in the HMD_HOME the suite is given, and creates one bucket
...               there with the AWS CLI. `make test-data-storage` runs it.
...
...               With STORAGE_INSTANCE set (an hmd-inf-s3bucket instance the
...               environment has applied, such as the storage bundle's
...               `storage`), the last case lands into its bucket too.
Library           Process
Library           OperatingSystem
Library           String
Library           Collections
Suite Setup       Prepare
Suite Teardown    Clean Up

*** Variables ***
${BINARY}            ${EMPTY}
${DUCKDB_IMAGE}      ${EMPTY}
${STORAGE_ENV}       ${EMPTY}
${STORAGE_INSTANCE}  ${EMPTY}
${HMD_HOME_DIR}      %{HMD_HOME=}
${CORPUS}            ${CURDIR}${/}..${/}src${/}go${/}nsctl${/}internal${/}data${/}testdata${/}corpus
${WORKSPACE}         robot-storage-${{ __import__('uuid').uuid4().hex[:8] }}
${BUCKET}            robot-data-${{ __import__('uuid').uuid4().hex[:8] }}

*** Keywords ***
Prepare
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-data-storage` does.
    Skip If    '${STORAGE_ENV}' == ''    set STORAGE_ENV to a running local environment
    Should Not Be Empty    ${HMD_HOME_DIR}    msg=HMD_HOME must name the home STORAGE_ENV lives in
    Data    init    ${WORKSPACE}    ${CORPUS}
    Data    scan    --apply
    Data    register

Clean Up
    Run Keyword If    '${STORAGE_ENV}' != ''    Run Process    ${BINARY}    data    delete    ${WORKSPACE}    --yes
    ...    env:HMD_HOME=${HMD_HOME_DIR}

Data
    [Arguments]    @{args}    ${rc}=0
    ${env_image}=    Set Variable If    '${DUCKDB_IMAGE}' != ''    ${DUCKDB_IMAGE}    ${EMPTY}
    ${result}=    Run Process    ${BINARY}    data    @{args}    --workspace    ${WORKSPACE}
    ...    env:HMD_HOME=${HMD_HOME_DIR}
    ...    env:HMD_DUCKDB_IMAGE=${env_image}
    Should Be Equal As Integers    ${result.rc}    ${rc}    msg=${result.stdout}\n${result.stderr}
    RETURN    ${result}

Data JSON
    [Arguments]    @{args}
    ${result}=    Data    @{args}    --json
    ${value}=     Evaluate    json.loads($result.stdout)    modules=json
    RETURN    ${value}

*** Test Cases ***
Storage Shows The Environment And Withholds The Secret
    [Tags]    nerd039    storage
    ${info}=    Data JSON    storage    --env    ${STORAGE_ENV}
    Should Be Equal    ${info}[internal_endpoint]    http://neuronsphere:4566
    Should Not Be Empty    ${info}[access_key_id]
    Dictionary Should Not Contain Key    ${info}    secret_access_key
    Should Contain    ${info}[aws_cli]    s3 mb
    Set Suite Variable    ${ENDPOINT}    ${info}[endpoint]
    Set Suite Variable    ${KEY}    ${info}[access_key_id]
    Set Suite Variable    ${REGION}    ${info}[region]

Land Into A Bucket You Created
    [Documentation]    The AWS CLI creates the bucket, as a person would from
    ...                the snippet storage printed; land writes every table,
    ...                the views read it back, and a second land writes nothing.
    [Tags]    nerd039    storage
    ${which}=    Run Process    which    aws
    Skip If    ${which.rc} != 0    no AWS CLI on this host
    ${mb}=    Run Process    aws    --endpoint-url    ${ENDPOINT}    s3    mb    s3://${BUCKET}
    ...    env:AWS_ACCESS_KEY_ID=${KEY}    env:AWS_SECRET_ACCESS_KEY=dummykey    env:AWS_REGION=${REGION}
    Should Be Equal As Integers    ${mb.rc}    0    msg=${mb.stderr}
    ${early}=    Data    land    rc=2
    Should Contain    ${early.stderr}    no storage yet
    ${land}=     Data JSON    land    --env    ${STORAGE_ENV}    --to    s3://${BUCKET}/shop
    ${rows}=     Evaluate    {r['table']: r['rows'] for r in $land['landed']}
    Should Be Equal As Integers    ${rows}[orders]    5
    Should Be Equal As Integers    ${rows}[regional_report]    21
    ${answer}=   Data JSON    sql    SELECT c.name, sum(o.amount) AS total FROM orders o JOIN customers c USING (customer_id) GROUP BY ALL ORDER BY total DESC LIMIT 1
    Should Be Equal    ${answer}[0][name]    Ada Lovelace
    ${again}=    Data JSON    land
    ${changed}=  Evaluate    [r['table'] for r in $again['landed'] if not r['unchanged']]
    Should Be Empty    ${changed}
    ${status}=   Data JSON    status
    Should Be Equal    ${status}[storage][bucket]    ${BUCKET}
    Should Start With    ${status}[tables][0][landed]    s3://${BUCKET}/shop/raw/

Land Into The Storage Bundle's Bucket
    [Documentation]    instance:<name> reads the bucket from what the
    ...                hmd-inf-s3bucket instance produced; nobody types it.
    [Tags]    nerd039    storage
    Skip If    '${STORAGE_INSTANCE}' == ''    set STORAGE_INSTANCE to an applied hmd-inf-s3bucket instance
    ${land}=     Data JSON    land    --env    ${STORAGE_ENV}    --to    instance:${STORAGE_INSTANCE}    --force
    Should Not Be Equal    ${land}[storage][bucket]    ${BUCKET}
    ${rows}=     Evaluate    {r['table']: r['rows'] for r in $land['landed']}
    Should Be Equal As Integers    ${rows}[orders]    5
