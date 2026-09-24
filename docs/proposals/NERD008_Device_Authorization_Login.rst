.. NERD008 Device Authorization Login

NERD008 Device Authorization Login
==================================

.. req:: Sign in from the CLI with nothing pre-configured but one URL
    :id: HMD_CLI_NEURONSPHERE_NERD008
    :status: in_progress

    ``nsctl`` shall authenticate a user through the **OAuth 2.0 device
    authorization grant** (RFC 8628), against an authorization server named by
    a single URL in ``$HMD_HOME/.config/nsctl.toml`` and located by OIDC
    discovery.

    The user's machine shall carry **no client secret**. It may carry a public
    ``client_id``, which the device grant makes public by design; it shall never
    carry anything that authenticates the client.

    Where the endpoint points is not the client's concern. ``auth_url`` may name
    a customer's own Okta or Auth0 issuer, or any service presenting the same
    endpoints -- see SPEC007 for which of those was chosen and why.

    ``nsctl`` shall be unable to distinguish one such endpoint from another.
    Pointing ``auth_url`` at a customer's own Okta or Auth0 issuer, or at the
    local mock, shall work unchanged and with no client change.

Motivation
----------

``nsctl`` is self-sufficient for everything local and can authenticate to
nothing remote. The only login in the platform is the Python ``hmd-cli-login``,
and its shape is wrong for the motion this CLI exists to serve.

**It needs a browser on the same host.** ``hmd login`` is authorization-code
plus PKCE through a Flask server bound to ``localhost:8082``; the CLI learns it
worked by watching ``tokens.yaml``'s mtime for sixty seconds. There is no IPC
between the two processes and no path that works over SSH, inside a container,
or on a machine with no browser.

**It requires the user to configure the identity provider before their first
command.** ``helpers.load_config()`` asserts on ``HMD_NS_ISSUER`` and
``HMD_NS_CLIENT_ID`` in ``hmd.env``, and ``hmd configure`` -- the command whose
job is to populate that file -- never prompts for either. They are pasted in by
hand or by an installer. ``hmd login service`` additionally requires a plaintext
``HMD_SERVICE_CLIENT_SECRET`` on disk. For a user who has just installed a
binary, that is not a first step; it is a reason to stop.

**There is no refresh.** The Okta CLI application is provisioned
``grant_types=["authorization_code", "implicit"]``
(``hmd-lib-cdktf-factories/auth.py``), and the flow never requests
``offline_access``, so no refresh token is ever issued. When the access token
expires every ``hmd`` command begins returning 401 until the browser flow is
repeated. ``hmd-cli-login``'s controller still carries ``# TODO: Check token
validity first``; it does not check, so the repeat is unconditional.

The device grant answers all three. The user reads a short code off the
terminal and types it into whatever browser they have, on whatever machine they
have. Nothing binds a port, nothing is watched for an mtime change, and
``offline_access`` is requested by default so the hour-long token is renewable
without a second visit to a browser.

Two things already in the tree make this cheaper than it looks.
``internal/librarian/client.go`` **already reads**
``$HMD_HOME/.cache/tokens.yaml``; only the Python writes it today, so a Go
login makes the binary self-sufficient and authenticated artifact pulls begin
working the same day. And ``nsctl authd`` is already an Okta-shaped mock
authorization server with two authorization servers and a claims-choosing
sign-in form -- adding the device endpoints to it turns it from a policy
fixture into a test double for this flow, so the whole thing is exercised by
``make check`` with no cloud, no credentials and no Docker.

Scope
-----

**In scope.** The client: ``nsctl login``, ``logout`` and ``whoami``; the TOML
configuration and its profiles; the token store; the device endpoints on the
local mock; and the **contract** the admin-account service must satisfy.

**Out of scope.**

- *Any* server-side service. SPEC007 was a broker in the customer's admin
  account and is withdrawn: the client points at the customer's provider
  directly. User management over that provider is ``hmd-ms-identity``
  ``NERD001``.
- Changing ``hmd-cli-login``. The Python surface coexists, as NERD002 SPEC012
  requires; both front ends write the same file and either may be used.
- Authorising anything. This proposal obtains a token and caches it. What reads
  that token, and what any policy decides about its claims, is unchanged.
- Service-account credentials. Agents are not users; a per-install API key is a
  different mechanism with a different lifetime and does not belong in an
  interactive grant.
- Local operation, entirely. Signing in is never a precondition for any local
  verb, and no local path sends a credential: the local deployment-service and
  librarian clients are anonymous by construction, the deploy containers carry
  no token, and ``AUTHORIZATION`` defaults to ``NONE`` on every local Lambda.
  It follows that a failure on a local path is never an authentication failure,
  and must never be reported in terms that invite that reading -- see NERD024
  SPEC007, which makes the property legible rather than merely true.

.. spec:: The configuration file and its profiles
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC001
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    Configuration lives at ``$HMD_HOME/.config/nsctl.toml``, overridable in
    full by ``HMD_LOCAL_NSCTL_CONFIG`` -- the convention
    ``HMD_LOCAL_ENV_MANIFEST`` and ``HMD_LOCAL_CP_MANIFEST`` already set, and
    what lets a test avoid depending on ``$HMD_HOME``'s layout.

    .. code-block:: toml

        default_profile = "neuronsphere"

        [profile.neuronsphere]
        auth_url = "https://auth.neuronsphere.io/oauth2/ns"

        [profile.acme]
        auth_url  = "https://auth-aaa-us-west-2.acme-admin-neuronsphere.io/oauth2/ns"
        audience  = "api://neuronsphere"
        client_id = "0oa1b2c3"
        scopes    = ["openid", "email", "profile", "groups", "offline_access"]

    ``auth_url`` is the only required key.

    **``.config/``, because that is where authored configuration already
    lives** -- ``hmd.env``, ``control-plane.yaml``, ``uv.toml``, ``pip.conf``
    -- and because it is not ``.cache/``. The sentence that keeps environment
    manifests out of ``.cache`` decides this file's location too: ``env purge``
    deletes ``.cache`` subtrees and must never take a user's endpoints with it.

    **TOML rather than the YAML every manifest uses.** This is the one piece of
    configuration a new user must supply before anything works, and it is the
    file most likely to be hand-written from a documentation example. TOML has
    no significant indentation to get wrong. It is also already the format of
    the other hand-authored file in that directory, ``uv.toml``.

    **``auth_url`` is the issuer, not a host.** Discovery is fetched from
    ``{auth_url}/.well-known/...``, and an authorization server is reached at a
    path: ``authd`` serves ``/oauth2/ns`` and ``/oauth2/services``, and Okta's
    org servers are ``/oauth2/<id>``. A bare hostname resolves to no discovery
    document, so the value is validated as an absolute ``http://`` or
    ``https://`` URL and the error says which mistake was made.

    **Profiles, not a single endpoint.** The same person may hold accounts in
    more than one place -- an existing customer with two admin accounts, anyone
    working across tenants. One extra table level costs nothing now and cannot
    be retrofitted later without changing the format under people who have
    already written the file.

    A profile is resolved from ``--profile``, then ``default_profile``, then --
    only when the file defines exactly one -- that one. Beyond that the CLI
    refuses and lists what is defined. Choosing one of three alphabetically
    would authenticate against an endpoint nobody named.

    Unknown keys are **refused**, not ignored. A misspelt ``auth_uri`` that
    parsed silently would produce "auth_url is required" against a file that
    visibly contains a URL, which is the least actionable error available.

.. spec:: Absent configuration prompts or refuses, and never guesses
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC002
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    ``nsctl login`` with no usable configuration does exactly one of two
    things, decided by whether there is a terminal to answer:

    - **On a TTY** -- prompts for the profile's ``auth_url``, writes the file
      at ``0600`` in a directory created ``0700``, and continues into the
      flow. The first profile written becomes ``default_profile``, so a user
      who answered one prompt never names a profile again.
    - **Not on a TTY, or with** ``--no-prompt`` -- refuses with exit code
      ``2`` (``nserr.Usage``), naming the exact path, the missing key, and a
      pasteable ``[profile.<name>]`` block.

    The distinction that makes this safe is between *absent* and *invalid*. A
    missing file is offered a prompt; a file that exists and does not parse is
    an error naming the line, and is never silently overwritten. Prompting to
    recreate a file the user has already written is how a typo becomes data
    loss.

    ``--auth-url`` overrides the file for a single run; with ``--save`` it
    writes the profile. Neither requires the file to exist first, so a scripted
    install can pass the URL once and never author TOML at all.

    *Amended 2026-09-14.* This said "refusing rather than defaulting to a
    NeuronSphere-hosted endpoint is deliberate", on the grounds that an endpoint
    is where credentials are sent. The rule is right and the conclusion drawn
    from it was wrong, in the case that matters most.

    What the rule forbids is **inferring** an endpoint -- from a hostname, an
    email domain, a previous session. A constant compiled into the binary is not
    an inference; it is this build's own identity, and it is what ``gh auth
    login``, ``stripe login`` and ``vercel login`` all carry. Refusing in that
    case does not protect anybody: it means a free-tier user must author TOML
    before their first login, which is friction at exactly the step that exists
    to remove it.

    So a build **may** carry a default endpoint, in
    ``nsconfig.DefaultAuthURL``, set at link time
    (``-X ...nsconfig.DefaultAuthURL=...``) the way ``main.version`` already is.
    It appears as a profile named ``neuronsphere``. The file always wins: a
    ``[profile.neuronsphere]`` table replaces it outright rather than merging,
    so the endpoint someone reads in their own file is the one that is used. It
    answers only for its own name or for no name at all -- a caller who asked
    for ``staging`` and has no file must hear that ``staging`` is undefined, not
    be signed in somewhere else.

    **It is empty unless a build sets it**, and no build sets it yet. A
    development build has no hosted tenant to name, and a constant pointing at a
    host that cannot answer is worse than the refusal above. So today's
    behaviour is unchanged and the mechanism is in place for the day the tenant
    exists. See SPEC009 for which populations this serves.

.. spec:: The device authorization grant, by the RFC
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC003
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    The client implements RFC 8628 against endpoints located by discovery, and
    knows nothing else about the server.

    **Discovery.** ``{auth_url}/.well-known/openid-configuration``, falling
    back to ``/.well-known/oauth-authorization-server``. Both are required
    because the ecosystem is split: Okta publishes the OIDC document, Superset
    and Airflow build the OAuth one, and ``authd.Server.Handler`` already
    serves both at each prefix for precisely this reason. A document carrying
    no ``device_authorization_endpoint`` is refused with its own message --
    that is the "this server does not implement the grant; point at the broker"
    case, and it deserves a sentence rather than a missing-field error.

    **Polling.** The state machine is implemented completely, because every
    state arrives as the same HTTP 400 and is distinguished only by the
    ``error`` field: ``authorization_pending`` waits the interval,
    ``slow_down`` adds five seconds to it per section 3.5, and
    ``access_denied`` and ``expired_token`` are terminal with messages saying
    which happened. The loop stops at the ``expires_in`` deadline even if the
    server never says so, and honours the context throughout, so Ctrl-C returns
    at once rather than at the end of the current sleep.

    The first poll waits one interval before firing. The user cannot have
    approved in the microsecond since the code was issued, and an immediate
    poll only earns a ``slow_down`` from a server that counts them.

    **Presentation.** The user code and the URL are printed **before** any
    attempt to open a browser. The browser is opened best-effort by shelling
    ``open`` or ``xdg-open``; a failure is a ``note:``, never an error. The
    printed URL is the real interface and the only one that works over SSH.
    ``--no-browser`` skips the attempt.

    ``verification_uri_complete`` is used for the browser when the server
    supplies it, so a user whose browser opens has nothing to type; the plain
    ``verification_uri`` and the code are still printed, because the complete
    URL is optional in the RFC and unusable when read aloud.

    **Secrets are never logged.** Not the device code, not the tokens, not the
    ``Authorization`` header -- including under any verbosity flag.

.. spec:: The token store extends tokens.yaml and does not reshape it
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC004
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    The credential is written to ``$HMD_HOME/.cache/tokens.yaml`` -- the file
    ``hmd-cli-login`` already writes and that both
    ``hmd_cli_tools.okta_tools.get_auth_token`` and
    ``internal/librarian/client.go`` already read.

    .. code-block:: yaml

        login:
          access_token: ...
          id_token: ...
          refresh_token: ...       # new
          expires_at: ...          # new, RFC 3339
          issuer: ...              # new
          profile: ...             # new
          token_type: Bearer       # new

    Every new key sits **inside** ``login:``. Both existing readers take
    ``data["login"]["access_token"]`` and ignore the rest, so roughly forty
    Python repositories and one Go package keep working with no change, and
    authenticated artifact pulls begin working the day this lands. Unknown keys
    read from the file are carried through a rewrite, so a future writer's
    additions are not silently dropped.

    The file is written at **0600**, in a directory created ``0700``, through a
    temporary sibling and a rename. The Python writer uses a bare ``open(...,
    "w")`` after a ``Path.touch()`` and sets no mode, so the bearer token is
    typically ``0644`` -- world-readable on a shared machine. This proposal
    does not change the Python, and must not reproduce its behaviour.

    **Why not the OS keychain.** ``internal/keyring`` exists but is read-only:
    it shells ``security find-generic-password`` and has no write path, no
    Linux equivalent for writing, and no answer for a headless CI machine.
    More decisively, nothing else can read it -- the interoperability with the
    Python CLI that makes this file the right answer is exactly what a keychain
    entry would destroy. It remains available later as an opt-in.

.. spec:: Reuse and refresh before asking for a browser
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC005
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    ``nsctl login`` against a profile whose cached token is still valid reports
    that and exits ``0``. ``--force`` runs the flow anyway.

    A token that has expired, or is within a short margin of expiring, is
    **refreshed silently** when a refresh token is held, and the user sees
    nothing but success. Only when there is no refresh token, or the refresh is
    rejected, does a browser become necessary.

    This is the behaviour ``offline_access`` is requested for, and the one the
    Python flow cannot offer. It is also the reason the scope defaults include
    ``offline_access`` rather than leaving it to each profile: a default that
    omits it would reproduce the hourly re-authentication this proposal exists
    to remove.

    A server may rotate the refresh token, return the same one, or return none.
    The stored refresh token is replaced only when the response carries one,
    so the third case does not destroy a working credential.

.. spec:: The local mock becomes the test double
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC006
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    ``nsctl authd`` (NERD002 SPEC016) gains the device grant, so the flow is
    testable with no cloud, no credentials, no network and no Docker.

    - ``device_authorization_endpoint`` is added to both discovery documents,
      and the device grant type to ``grant_types_supported``.
    - ``POST {prefix}/v1/device/authorize`` issues a ``device_code`` and a
      human-readable ``user_code``, with ``verification_uri``,
      ``verification_uri_complete``, ``expires_in`` and ``interval``. The user
      code excludes glyphs that are ambiguous when read off a screen and typed
      into a browser.
    - ``GET {prefix}/v1/device`` renders the **same claims-choosing sign-in
      form** the redirect flow uses, keyed by user code rather than redirect
      URI. Choosing the claims is the whole point of the mock -- the groups
      decide every Rego decision and both applications' role mapping -- so the
      device path must offer the same box, or it tests a different thing.
    - ``POST {prefix}/v1/device`` binds the typed claims to the pending code.
    - ``POST {prefix}/v1/token`` handles the device grant, answering
      ``authorization_pending`` until approval and then issuing once.

    Pending device codes are held beside the existing authorization codes,
    under the same mutex and swept on write the way ``storeCode`` prunes: the
    map only grows when someone signs in, so that is the right moment to prune.

    **The refresh grant, which was advertised and not implemented.** The
    discovery document has listed ``refresh_token`` in
    ``grant_types_supported`` since this server's first version, and the token
    endpoint answered it with ``unsupported_grant_type`` -- a lie a consumer
    could only discover at runtime. It went unnoticed because nothing local
    held a refresh token. SPEC005 makes ``nsctl login`` renew whenever it has
    one, so the mock now issues a refresh token when ``offline_access`` is
    among the requested scopes, and honours the grant: rotated and single use,
    as Okta and Auth0 both do by default. Without this the renewal path could
    be exercised only against a stub, which is the half of SPEC008 this
    document says is not worth having.

    Only when asked for. A service on ``client_credentials`` already holds its
    own credentials, and a refresh token it never requested is a long-lived
    secret nobody agreed to look after.

    What this is not, restated from SPEC016 because it now sits on a login
    path: it signs with a key it generated, mints a token with any claims asked
    of it, registers no clients, and is not reachable from outside the machine.
    It is not an authorization server anyone should trust.

.. spec:: No broker -- auth_url names the customer's own provider
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC007
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: withdrawn

    *Withdrawn 2026-09-14.* This specified a service in the customer's admin
    account presenting the RFC 8628 endpoints and holding the OAuth client
    registration on the user's behalf. The decision is that ``hmd-ms-identity``
    stays **out of the authentication path** and fronts user management only;
    see ``hmd-ms-identity`` ``NERD001``. ``auth_url`` therefore names the
    customer's own Okta or Auth0 issuer, which implements the grant natively.

    **Nothing in the client changes**, which is the property SPEC003 was built
    for: ``nsctl`` speaks the RFC against whatever discovery names and cannot
    tell a provider from a broker. That this withdrawal costs no code is the
    evidence the indirection was drawn in the right place.

    Two things in the original reasoning were wrong, and are recorded because
    both would otherwise be re-derived:

    - **"The broker holds the client secret" was never the real justification.**
      The device grant is a public-client flow: RFC 8628 requires no client
      secret, and ``client_id`` is public by design. A broker's actual value is
      tenant indirection -- one ``auth_url`` for every customer -- and claim
      enrichment, neither of which is needed yet.
    - **The Okta gap was overstated.** This said a device-capable Okta client
      "must be created out of band". ``HmdOktaClient.create_pkce_application``
      already registers an Okta ``native`` application through the dynamic
      client registration endpoint; adding the device grant type to its
      ``grant_types`` is a one-item change. The real gap is narrower and lives
      one layer up: ``Okta.oauth_native_app`` is not implemented in
      ``hmd-lib-cdktf-factories``, so it inherits ``AuthProvider``'s
      ``raise NotImplementedError`` while ``Auth0`` implements it.

    **What this costs, and it is a real cost.** A device-capable application
    must be provisioned in each customer's provider, and each profile must name
    its ``client_id`` -- so the "nothing on the machine but one URL" promise
    holds for the local mock and becomes "one URL and one public client id"
    against a real provider. Closing that is ``Okta.oauth_native_app`` plus the
    device grant type, in the factory.

    Reviving a broker later needs no client change, and would have to mint its
    own tokens rather than relay: a service publishing ``issuer: <broker>`` in
    its discovery document while returning the provider's tokens -- which carry
    ``iss: <provider>`` -- satisfies neither RFC 8414's requirement that the
    document's ``issuer`` equal the URL it was fetched from, nor
    ``hmd_lib_auth.verify_token``. ``NERD001``'s alternatives section carries
    the full reasoning.

.. spec:: Where auth_url comes from, per population
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC009
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: proposed

    With SPEC007 withdrawn, "point ``auth_url`` at your provider" is an answer
    only for someone who has one. There are three populations and they do not
    share an answer; conflating them is what made the broker look necessary.

    **1. Self-serve, no admin account -- the free tier and every trial.** They
    have no Okta tenant, no admin account and no deploy. ``auth_url`` is
    **NeuronSphere's own tenant**: one authorization server we operate,
    multi-tenant by ``hmd_lang_saas.organization``. Not a broker and not
    ``hmd-ms-identity`` -- an ordinary identity provider that happens to be
    ours, reached directly like any other.

    This is the population the SPEC002 default exists for, and it must be the
    default rather than a documented URL to copy: someone installing the binary
    and running ``nsctl login`` has to arrive at a browser having configured
    nothing.

    **Auth0 rather than Okta for that tenant.** ``hmd-ms-mickey`` already runs
    Auth0 with Stripe-billed organizations, Auth0 Organizations map onto
    ``hmd_lang_saas.organization``, and an Okta tenant per free-tier
    organization is not economically possible. Its cost is
    ``hmd-ms-identity`` ``NERD001`` SPEC004: Auth0 has no groups, so the team
    claim needs a Post-Login Action.

    **2. Enterprise, bring-your-own SSO, with an admin account.**
    ``auth_url`` is their own authorization server's issuer
    (``https://acme.okta.com/oauth2/aus1b2c3d4``) and the profile names their
    application's ``client_id``.

    Copying both out of deploy outputs by hand is the friction this document
    exists to remove, so a **static discovery document** should be served at the
    admin-account hostname already in use -- the shape
    ``librarian/client.go`` composes for the artifact librarian:

    .. code-block:: text

        nsctl login --customer acme
          GET https://acme-admin-neuronsphere.io/.well-known/neuronsphere-login
          -> {"auth_url": "...", "client_id": "...", "audience": "..."}
          -> written to [profile.acme], then the ordinary flow

    **This is not the broker SPEC007 withdrew, and the difference is not
    cosmetic.** That was an authentication participant: it received the device
    request and returned tokens, which is what made its ``issuer`` disagree with
    their ``iss``. This is a static JSON file holding configuration that is
    already public -- an issuer URL and a public client id. It holds no secret,
    handles no token, and is not on the login path. *Proposed, not built.*

    **3. Air-gapped, or anyone who prefers to be explicit.** Write the TOML, or
    pass ``--auth-url`` once with ``--save``. Implemented, and the only path
    that works today.

    **The blocker is provisioning, not discovery.** Population 2 cannot be
    served at all until an Okta application can be created with the device
    grant: ``Okta.oauth_native_app`` is unimplemented in
    ``hmd-lib-cdktf-factories`` -- it inherits ``AuthProvider``'s
    ``raise NotImplementedError`` while ``Auth0`` implements it -- and
    ``HmdOktaClient.create_pkce_application`` registers a native application
    with ``grant_types`` of ``authorization_code`` and ``refresh_token`` only.
    Until both are fixed, an enterprise Okta customer provisions the application
    by hand in the Okta console however good the discovery story is. Population
    1 does not hit this, because we provision our own tenant once rather than
    per customer -- which makes the free tier the *easier* case, the opposite of
    what the withdrawn SPEC007 assumed.

.. spec:: Verification
    :id: HMD_CLI_NEURONSPHERE_NERD008_SPEC008
    :links: HMD_CLI_NEURONSPHERE_NERD008
    :status: implemented

    The flow must be exercised end to end by ``make check``, against the local
    mock, with the real client driving the real server in process. A login path
    covered only by mocked HTTP responses proves that the client agrees with
    the author's idea of the protocol, which is the thing most likely to be
    wrong.

    Beyond that, the cases worth pinning are the ones that are easy to get
    wrong and invisible when they are:

    - ``authorization_pending`` followed by success, and ``slow_down``
      **increasing the interval** rather than being treated as pending.
    - ``expired_token`` and ``access_denied`` reported as themselves.
    - Discovery falling back to ``oauth-authorization-server``, and a document
      with no device endpoint refused with the message SPEC003 requires.
    - Context cancellation returning promptly mid-poll.
    - Absent config refusing with exit ``2``; an invalid one never overwritten.
    - A build with no ``DefaultAuthURL`` refuses exactly as before, and one with
      it signs in against it having read no file; a ``[profile.neuronsphere]``
      table beats it; and it does not answer for another profile's name.
    - The token file's mode being ``0600``, unknown keys surviving a rewrite,
      and -- as a golden test -- the written file still parsing as
      ``login.access_token``. That last one is the contract with forty Python
      repositories and should fail loudly rather than subtly.

    Every test runs under ``t.Parallel()``, which is why nothing in these
    packages reads ``os.Getenv`` directly: configuration arrives as an
    ``hmdenv.Lookup`` and a ``home`` argument, as NERD002 SPEC004 requires.
