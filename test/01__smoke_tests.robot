*** Settings ***
Documentation     Smoke tests for hmd neuronsphere CLI subcommands.
...               Verifies CLI is installed and all subcommand help outputs work.
Library           Process
Library           OperatingSystem
Library           resources/NeuronSphereLib.py

Suite Setup       Check NS CLI Available

*** Keywords ***
Check NS CLI Available
    ${result}=    Run Process    hmd    neuronsphere    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    neuronsphere

*** Test Cases ***
Help Output
    [Tags]    smoke
    ${result}=    Run NS Command    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    up
    Should Contain    ${result.stdout}    down
    Should Contain    ${result.stdout}    restart
    Should Contain    ${result.stdout}    configure
    Should Contain    ${result.stdout}    validate-plugin
    Should Contain    ${result.stdout}    init-plugin
    Should Contain    ${result.stdout}    list-local-plugins

Version Output
    [Tags]    smoke
    ${result}=    Run NS Command    --version
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    version

Up Help
    [Tags]    smoke
    ${result}=    Run NS Command    up    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    verbose

Down Help
    [Tags]    smoke
    ${result}=    Run NS Command    down    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    verbose

Restart Help
    [Tags]    smoke
    ${result}=    Run NS Command    restart    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    service_name

Run Help
    [Tags]    smoke
    ${result}=    Run NS Command    run    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    instance_name

Update Images Help
    [Tags]    smoke
    ${result}=    Run NS Command    update-images    --help
    Should Be Equal As Integers    ${result.rc}    0

Validate Plugin Help
    [Tags]    smoke
    ${result}=    Run NS Command    validate-plugin    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    no-file-check

Init Plugin Help
    [Tags]    smoke
    ${result}=    Run NS Command    init-plugin    --help
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    plugin-name

List Local Plugins
    [Tags]    smoke
    ${result}=    Run NS Command    list-local-plugins
    Should Be Equal As Integers    ${result.rc}    0
    Should Contain    ${result.stdout}    Local Plugin Configuration
