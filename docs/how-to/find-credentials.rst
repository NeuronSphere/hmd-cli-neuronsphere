Find the URL and credentials for what you deployed
==================================================

A deployed user interface is only useful if you can open it and sign in.
``nsctl env credentials`` answers both, from what each repo class declares::

   nsctl env credentials
   nsctl env credentials dev

It prints one row per declared way in: the instance, the URL with its
placeholders filled in, the user who signs in, and **where** the credential
lives — not the credential itself::

   Access to "dev":
     INSTANCE  NAME      URL                                     USERNAME  CREDENTIAL
     web       superset  http://web.ns.local/       admin     secrets-manager web-aaaa-dev-admin-credentials#password

   web/superset: Under AUTH_DB self-registration is off, so admin is the only account.

   Read a value with `nsctl env credentials dev --reveal`.

Reading a value is a separate, explicit step::

   nsctl env credentials dev --reveal
   nsctl env credentials dev --instance web --reveal

The default withholds the value on purpose. A credential printed in a deploy
summary lands in terminal scrollback, in CI logs and in screen shares, for a
value you needed once, and a password cannot be un-printed. Where it lives is
safe to repeat and is usually the answer you actually wanted.

``--json`` gives an agent the same document. It carries a value only when
``--reveal`` was passed, so a document produced without it cannot leak one.

What the columns mean when something is missing
-----------------------------------------------

``none declared``
   The entry names no credential. Either there is no login, or its credential is
   a fixture rather than a stored secret — the ``notes`` column says which.

``unresolved: ...``
   The entry was declared but could not be resolved, with the reason. The
   commonest one is a secret the instance has not written yet, because it writes
   it when it deploys. An unresolved entry is reported rather than hidden: its
   name appears nowhere else, and "declared, and here is why it is not answering
   yet" is what you need after a deploy that has not finished.

Everything except ``--reveal`` reads local state and works with nothing running.
Reading a value needs the control plane's Floci.

Nothing is listed
-----------------

If the command reports that no instance declares how to reach it, the instances
are deployed but their repo classes have not said how they are reached. That is
a declaration in the class's own BACON manifest, not a setting here — see
:doc:`../reference/bacon` and::

   nsctl repoclass access add superset \
       --url 'http://{ingress_host}/' \
       --username admin \
       --secret-store secrets-manager \
       --secret-key '{instance_name}-{deployment_id}-{environment}-admin-credentials' \
       --secret-property password

The declaration belongs to the workload's class, so it applies however that
instance arrived — ``nsctl stack add``, ``env add --from-repo`` or ``repo add``.

Where an endpoint is not a login
--------------------------------

Not everything an environment runs has a front door to declare. Local Trino is
reached on the host port the environment's slot streams, with authentication
disabled, so it declares no access entry; ``nsctl env status`` reports that
endpoint as a route once Trino is actually deployed. See
:doc:`manage-environments`.
