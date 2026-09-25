Resolve the local hostnames
===========================

Starting a platform and deploying to it need nothing here: ``nsctl`` dials
Floci's own hostnames on loopback itself, so a first run needs no privileged
step and no ``/etc/hosts`` entry.

Reaching things by name does. Four of them:

- the identity provider's issuer (``auth.ns.local``),
- the package registry (``registry.ns.local``) and any
  control-plane extension,
- the legacy ``hmd build`` and ``push-artifact``, which follow Floci's
  presigned URLs through a client that has no loopback redirect,
- **every user interface** an environment deploys -- Airflow, Argo, Superset --
  reached at ``http://<instance>.ns.local/`` -- or
  ``http://<instance>.<env>.ns.local/`` outside the default environment.

The first two could not be reached on a port even in principle: each is read by
a browser, by a sibling container and by a cluster pod, and all three must
resolve the *same string*. ``http://localhost:<port>`` can never be that
string, because inside a pod ``localhost`` is the pod.

User interfaces could have been, and briefly were. It was withdrawn: serving a
UI on a port means rewriting the ``Host`` header the Ingress rule is matched
on, which holds for redirects and not for an absolute URL emitted inside a
page, and it is not how the cloud reaches the same chart.

Point this machine at the local resolver
----------------------------------------

Start the resolver with the control plane::

   nsctl control-plane start   # the resolver runs by default

Then print the one privileged step and run it yourself::

   nsctl dns install

``nsctl`` prints it rather than running it. It needs root, and it changes a
file that belongs to you.

Check it::

   nsctl dns status

The check resolves a name that has deliberately never been deployed. That is
the property worth testing: a wildcard covers names that do not exist yet,
which is exactly what a hosts file cannot do.

.. note::

   The resolver file is filed under ``ns.local``, **not** under ``local``. A
   resolver file captures its whole subtree, and the bare name would swallow the
   entire mDNS TLD -- every ``.local`` name this machine resolves -- into a
   server that answers ``127.0.0.1`` for everything it owns.

The resolver is authoritative for that one suffix and forwards nothing, so it
works with no network at all and involves no public DNS zone.

It listens on **19153**, not the obvious 5353. mDNS holds 5353 on macOS, and
not only ``mDNSResponder`` -- Chrome and Spotify take it too. They share it
with ``SO_REUSEPORT``, which Docker's port publisher does not set, so
publishing 5353 fails outright on a typical Mac. 19153 sits above the
``19000-19079`` band the proxy publishes and below the 49152 ephemeral floor,
so nothing else claims it. It is also *chosen*: if something already holds
19153 the platform moves to another port, and ``nsctl dns install`` prints
whichever port is actually in force. ``HMD_LOCAL_DNS_PORT`` pins it.

.. note::

   ``dig`` will not confirm any of this, and that is expected rather than a
   failure: ``dig`` reads ``/etc/resolv.conf`` and queries that nameserver
   directly, bypassing ``/etc/resolver`` entirely. Use ``nsctl dns status``,
   ``dscacheutil -q host -a name <name>``, or simply ``curl``.

Use /etc/hosts instead
----------------------

The older, narrower alternative still works::

   sudo sh -c 'echo "127.0.0.1 neuronsphere neuronsphere-workload" >> /etc/hosts'
   sudo sh -c 'echo "127.0.0.1 auth.ns.local" >> /etc/hosts'

On Linux this is not only the older path but sometimes the only one: where
``nss-mdns`` is installed, ``nsswitch.conf`` sends ``.local`` to mDNS with
``[NOTFOUND=return]`` before the lookup ever reaches ``dns``, which stops it
dead. ``nsctl dns status`` says whether the suffix resolves; if it does not on
a machine that is correctly pointed at the resolver, this is why.

It costs a line per name, every time a new UI, environment or extension
appears, because ``/etc/hosts`` has no wildcards.

If it does not work
-------------------

Some managed configurations -- a corporate VPN, an enterprise resolver --
take precedence over per-domain resolver settings. Where that happens the
behaviour degrades to what it was before: add the names to ``/etc/hosts``.
``nsctl env start`` prints every UI hostname it found, so the list is to hand.

A resolver file pointing at a port nothing listens on adds latency to every
lookup in the suffix. ``nsctl dns status`` is what makes that visible.
