Test local authorization
========================

Use the local identity provider to produce realistic claims for policy and
application tests. It can mint user and service tokens, including custom
groups, audiences, and JSON-valued claims.

Enable the local provider
-------------------------

With ``HMD_HOME`` set::

   export HMD_LOCAL_NEURONSPHERE_ENABLE_AUTH=true
   nsctl control-plane start

On first use, startup builds the local ``hmd-img-nsctl`` image from sources
embedded in the binary. You do not need a separate source checkout.
For development, ``HMD_NSCTL_IMAGE`` can select another locally built image.

The issuer is one of the few things that cannot be reached on a port. The same
hostname is routed through the proxy from the host, the Docker network and
Kubernetes pods alike, and keeping it identical in every place matters: a token
with one ``iss`` value is not valid for a consumer expecting another. That is
exactly what ``http://localhost:<port>`` cannot be, because inside a pod
``localhost`` is the pod.

So the host has to resolve it. Either::

   nsctl dns install     # covers *.ns.local, wildcard included

   sudo sh -c 'echo "127.0.0.1 auth.ns.local" >> /etc/hosts'

The first covers the package registry and any control-plane extension too, and
keeps covering names added later.

Mint and inspect user claims
----------------------------

Generate a token for a local application role::

   nsctl authd token --group 'NeuronSphere Airflow Admin - Local (none)'

To inspect the payload without printing an encoded token::

   nsctl authd token --group 'NeuronSphere Airflow Admin - Local (none)' \
       --claim tenant=acme --claim 'level=3' --show-claims

Values that parse as JSON retain their types. Here ``tenant`` is a string
and ``level`` is a number. Repeat ``--group``, ``--scope``, or ``--claim``
to add values. Use ``--sub`` to set the subject and ``--lifetime`` to control
the token lifetime.

The group convention used by application role mapping is::

   NeuronSphere <app> <role> - <Environment> (<customer code>)

Test the exact spelling and case your application expects. A token can be
well-formed and still receive no role because its group name is unmatched.

Decode a minted token
---------------------

The decoder reads standard input::

   nsctl authd token --claim 'enabled=true' | nsctl authd token --decode

Decoding shows claims; it is not signature validation. Minting tokens works
without a running provider, using the local key material under the configured
home. HTTP discovery and login tests do require the running provider.

Exercise service authorization separately
-----------------------------------------

The two authorization servers use different audiences:

.. list-table::
   :header-rows: 1

   * - Token server
     - Audience
     - Intended identity
   * - ``ns`` (default, ``/oauth2/ns``)
     - ``api://neuronsphere``
     - User or application SSO
   * - ``services`` (``/oauth2/services``)
     - ``api://neuronsphere-services``
     - Service account

Generate a service identity with::

   nsctl authd token --server services --sub ms-deployment

For a policy test, compare an allowed group or service identity with an
unmatched group and an incorrect audience. ``--aud`` lets you make the
audience mismatch explicit. Assert both the allowed and denied result in the
test harness that evaluates your policy or calls the service.

Test the device-login flow
--------------------------

Add a local profile to ``$HMD_HOME/.config/nsctl.toml`` without replacing
other profiles::

   [profile.local]
   auth_url = "http://auth.ns.local/oauth2/ns"

Then run::

   nsctl login --profile local --no-browser
   nsctl whoami

Follow the printed verification URL and code. This tests discovery, device
authorization, and the shared credential cache. It does not prove every
application accepts the resulting issuer.

Current verification limit
--------------------------

The local issuer uses HTTP and signs tokens with locally generated keys.
It will mint the claims you request and is a development test facility.

The ``hmd-lib-auth.verify_token`` path used by the OPA authorizer requires an
HTTPS issuer through its verifier. This HTTP provider therefore does not by
itself demonstrate end-to-end authorizer success. A Rego test that decodes
claims and an application test that verifies the JWT exercise different
boundaries; distinguish them when reporting test results.
