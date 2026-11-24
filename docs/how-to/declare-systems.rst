Declare which system a repo class belongs to
============================================

A **system** is a group of repo classes that are worked on together. The
transform service is one: its API, engines, scheduler, images and libraries.
A system is not a unit of deployment; that is a bundle. Nothing ``nsctl``
builds, deploys or resolves reads a system. It answers "what is this made
of?" for people and agents.

Each repo class declares its own membership in its manifest's ``system`` key
(BACON NERD0002). Membership is never inferred from a name or a directory.

Declare membership
------------------

From the repo class's root::

   nsctl repoclass system set transform --role engine

``--role`` is optional and is one of ``core``, ``engine``, ``image``,
``library``, ``ui``, ``cli`` or ``docs``. With no flags, ``nsctl`` writes
the shorthand, ``"system": "transform"``.

A repo class belongs to at most one system. To remove the declaration::

   nsctl repoclass system clear

Define a system once
--------------------

A system's description and its parent come from the one manifest that
introduces it. A subsystem names its parent::

   nsctl repoclass system set cli-go --role cli --parent cli \
     --description "The Go command-line tools"

The other members give just the name and their role. If two manifests define
the same system differently, ``nsctl repoclass systems`` reports a conflict
naming both, and nothing is chosen by order.

See a workspace's systems
-------------------------

::

   nsctl repoclass systems --root ~/projects
   nsctl repoclass systems --root ~/projects --json

This reads every repo class one level under each root, skipping hidden
directories and git worktrees. For each system it prints the members and
their roles, the subsystems, and the resolved description and parent. A
system named only as a parent is listed too. The command exits 1 if it finds
a conflict, a parent loop or an unreadable ``system`` key, so it can run as a
check.

``nsctl repoclass describe`` shows a repo class's own declaration.
