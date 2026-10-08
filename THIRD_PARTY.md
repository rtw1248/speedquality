# Third-Party Sources and Services

SpeedQuality separates design references, external runtime services, and code dependencies. A
reference link does not imply endorsement, partnership, or permission from the operator of an
external service.

## Design reference

- [MiaM1ku/taierspeedtest](https://github.com/MiaM1ku/taierspeedtest): referenced while studying
  the publicly observable global network-test client flow and regional speed-test behavior. The
  project author requested attribution in the README. SpeedQuality does not vendor that project's
  source tree or distribute its protected Android client.
- TcpQuality: reviewed to compare SYN-response diagnostics and TCP transfer retransmission
  statistics. It is not a runtime dependency of SpeedQuality.

## External services and user-supplied content

- Global network-test compatible endpoints may be used by the private compatibility adapter.
  They remain third-party services and are subject to their own authentication, rate limits,
  availability, and terms. Their production discovery details are not part of the public repo.
- NodeQuality reports are linked only when a user supplies a report URL. SpeedQuality validates
  identity and test time, stores a size-limited sanitized snapshot for rendering, keeps the
  original report link visible, and does not claim affiliation with NodeQuality.

## Code dependencies

- `golang.org/x/text v0.20.0` is compiled into `sqprobe`; its BSD license is reproduced in
  `THIRD_PARTY_LICENSES/golang.org_x_text_LICENSE`.
- Cloudflare Wrangler is a development and deployment dependency. It is not bundled into the
  released `sqprobe` or `sq-node` binaries. Its package metadata and license remain available from
  the installed npm package and the Cloudflare Workers SDK repository.

Before a public release, regenerate dependency metadata from the locked Go modules and npm lock
files and check this list for changes.
