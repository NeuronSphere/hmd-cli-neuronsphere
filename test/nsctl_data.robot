*** Settings ***
Documentation     `nsctl data` end to end over the fixture corpus (NERD039).
...
...               Needs a container engine and the DuckDB image, and nothing
...               else: no environment, no control plane. `make test-data`
...               runs it with BINARY pointed at the freshly built binary and
...               DUCKDB_IMAGE at a local build of hmd-img-duckdb until that
...               image is published.
...
...               The scratch HMD_HOME lives under target/, not the system temp
...               directory, because every container engine shares the user's
...               home with its VM and not all of them share /var/folders.
Library           Process
Library           OperatingSystem
Library           String
Library           Collections
Suite Setup       Create Data Home
Suite Teardown    Remove Directory    ${HOME_DIR}    recursive=True

*** Variables ***
${BINARY}         ${EMPTY}
${DUCKDB_IMAGE}   ${EMPTY}
${CORPUS}         ${CURDIR}${/}..${/}src${/}go${/}nsctl${/}internal${/}data${/}testdata${/}corpus
${HOME_DIR}       ${EMPTY}

*** Keywords ***
Create Data Home
    ${dir}=    Join Path    ${CURDIR}    ..    target    data-robot-${{ __import__('uuid').uuid4().hex[:8] }}
    ${dir}=    Normalize Path    ${dir}
    Create Directory    ${dir}${/}.config
    Set Suite Variable    ${HOME_DIR}    ${dir}

Data
    [Documentation]    Runs `nsctl data` in the suite's HMD_HOME and fails on a
    ...                non-zero exit unless told otherwise.
    [Arguments]    @{args}    ${rc}=0
    Should Not Be Empty    ${BINARY}    msg=Pass --variable BINARY:<path>; `make test-data` does.
    ${env_image}=    Set Variable If    '${DUCKDB_IMAGE}' != ''    ${DUCKDB_IMAGE}    ${EMPTY}
    ${result}=    Run Process    ${BINARY}    data    @{args}
    ...    env:HMD_HOME=${HOME_DIR}
    ...    env:HMD_DUCKDB_IMAGE=${env_image}
    Should Be Equal As Integers    ${result.rc}    ${rc}    msg=${result.stdout}\n${result.stderr}
    RETURN    ${result}

Data JSON
    [Arguments]    @{args}
    ${result}=    Data    @{args}    --json
    ${value}=     Evaluate    json.loads($result.stdout)    modules=json
    RETURN    ${value}

Corpus Fingerprint
    [Documentation]    Every corpus file with its SHA-256, to prove nothing wrote to a source.
    ${all}=    Evaluate    sorted(str(p) for p in __import__('pathlib').Path($CORPUS).rglob('*') if p.is_file())
    ${sums}=    Evaluate    [(p, __import__('hashlib').sha256(open(p,'rb').read()).hexdigest()) for p in $all]
    RETURN    ${sums}

*** Test Cases ***
From A Folder Of Files To An Answer In SQL
    [Documentation]    NERD039: init, scan, profile, register and sql over the
    ...                corpus, with nothing but a container engine.
    [Tags]    nerd039    docker
    ${before}=    Corpus Fingerprint
    Data    init    shop    ${CORPUS}
    ${scan}=      Data JSON    scan    --apply
    Length Should Be    ${scan}[applied]    7
    Should Be Equal As Integers    ${scan}[files_seen]    10
    Length Should Be    ${scan}[unsupported]    3
    ${orders}=    Data JSON    profile    orders
    Should Be Equal As Integers    ${orders}[rows]    5
    ${kinds}=     Evaluate    [i['kind'] for i in $orders['issues']]
    Should Contain    ${kinds}    schema_drift
    ${reg}=       Data JSON    register
    ${rows}=      Evaluate    {t['table']: t['rows'] for t in $reg['tables']}
    Should Be Equal As Integers    ${rows}[regional_report]    21
    Should Be Equal As Integers    ${rows}[budget_actuals]    2
    ${answer}=    Data JSON    sql    SELECT c.name, sum(o.amount) AS total FROM orders o JOIN customers c USING (customer_id) GROUP BY ALL ORDER BY total DESC LIMIT 1
    Should Be Equal    ${answer}[0][name]    Ada Lovelace
    ${after}=     Corpus Fingerprint
    Lists Should Be Equal    ${before}    ${after}    msg=a source file changed; sources are only ever read

The Catalog Opens From The Host
    [Documentation]    NERD039 SPEC002: sources and workspace are mounted at
    ...                their host paths, so the catalog's views resolve from a
    ...                DuckDB on the host. Skipped when the host has none.
    [Tags]    nerd039    docker
    ${which}=     Run Process    which    duckdb
    Skip If    ${which.rc} != 0    no duckdb on this host
    ${cat}=       Set Variable    ${HOME_DIR}${/}data${/}shop${/}catalog.duckdb
    ${res}=       Run Process    duckdb    -readonly    -json    ${cat}    -c    SELECT count(*) AS n FROM orders
    Should Be Equal As Integers    ${res.rc}    0    msg=${res.stderr}
    Should Contain    ${res.stdout}    "n":5

Writes Need --write
    [Documentation]    NERD039 SPEC006: the catalog is read-only to sql.
    [Tags]    nerd039    docker
    ${res}=    Data    sql    CREATE TABLE x AS SELECT 1    rc=1
    Should Contain    ${res.stderr}    read-only
