# Phrouros

## Background
[φρουρά](https://en.wiktionary.org/wiki/%CF%86%CF%81%CE%BF%CF%85%CF%81%CE%AC)

Means watchdog, garrison, in ancient greek.

Phrouros will live in the same cluster with boskos service, and schedule to ping boskos every minute, to check service status and schedule clean up jobs for expired projects.

## K8s test:

Shoot phrouros up, together with boskos, you should be able to see they talking with each other.

Use curl to fakely request a project, with phrouros, boskos should release that project after the duration.
