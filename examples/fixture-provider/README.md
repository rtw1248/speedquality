# Fixture Provider

This development Provider implements the public lease contract for Beijing (`bj`), Shanghai
(`sh`), and Guangdong (`gd`). It contains no production nodes. Configure `FIXTURE_NODES` with
servers you control that implement the generic activate, download, upload, and release paths.

It is intended for local integration tests and as a starting point for independent Providers.
Production deployments should keep their node inventory and selection rules outside the public
repository.

Each configured server must implement the activate, download, upload, and release lifecycle used
by `sqprobe`; this fixture does not start those services. Node operators only need the endpoint
protocol and validation steps in
[`../../docs/node-protocol.md`](../../docs/node-protocol.md).

Lease requests require `duration_seconds: 5`, `target_mbps: 100|200|400`, and `modes: ["s"]`.
This fixture validates the contract but does not implement production discovery, physical-node
concurrency reservations, health scoring, or target-aware capacity admission.
