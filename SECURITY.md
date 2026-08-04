# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately**, not in a public issue or
pull request.

Use GitHub's private vulnerability reporting for this repository:
**Security → Advisories → Report a vulnerability**
(<https://github.com/kfet/fir/security/advisories/new>). This opens a private
advisory visible only to you and the maintainer.

If you cannot use GitHub advisories, open a minimal public issue asking for a
private contact channel — **without** any exploit details — and it will be
followed up privately.

In your report, please include:

- the version (`fir --version`) and platform;
- a clear description of the issue and its impact;
- the smallest set of steps, config, or input that reproduces it;
- whether the flaw is already public or known to anyone else.

## Scope

fir is a command-line coding agent that, by design, does powerful things on the
host it runs on. The following are the areas where security reports are most
valuable:

- **Command execution.** fir runs shell commands and tools on the local machine
  (and, via the `remote` extension, over SSH). Reports of *unintended* command
  execution — command injection, sandbox/confirmation bypass, a path where a
  tool runs something the user did not authorise — are in scope.
- **OAuth and credential handling.** fir drives OAuth flows for third-party
  vendors and stores tokens locally (e.g. `~/.config/fir/auth.json`). In scope:
  credential leakage, tokens written world-readable, a token minted for one host
  being sent to another, redirect/`resource`-indicator handling flaws, or
  credentials logged in plaintext.
- **Extensions.** fir executes Python (and other) extensions as external
  processes over a JSON-RPC bridge, including a trust prompt before first run.
  In scope: bypass of the trust/confirmation gate, privilege escalation through
  the extension bridge, or an extension being loaded from an unexpected location.
- **Session and config data at rest.** Unexpected exposure of transcript,
  session, or config files.
- **The release/distribution chain.** Issues in how binaries are built,
  checksummed, or mirrored (`kfet/fir-dist`, the Homebrew tap) such that a user
  could receive a tampered artifact.

### Out of scope

- The fact that fir runs commands, edits files, and calls out to model providers
  **when asked to** — that is its purpose. A model or a user instructing fir to
  do something destructive is expected behaviour, not a vulnerability.
- Vulnerabilities in third-party model providers, MCP servers, or extensions not
  maintained in this repository. Report those to their respective maintainers.
- Anything requiring an already-compromised host or a malicious local user with
  the same privileges as the person running fir.
- Test coverage percentages, lint findings, and similar quality signals.

## Response posture

fir is maintained by one person as a personal project published in the open.
There is **no guaranteed response-time SLA.** Reports are handled on a
best-effort basis, and genuine security issues are prioritised over features.

Expect a plan roughly along these lines, not a contractual commitment:

- an acknowledgement of your report when it is seen;
- an assessment of severity and scope, discussed with you in the advisory;
- a fix released as promptly as is practical for a solo maintainer, with the
  advisory published (and credit given, if you want it) once users have had a
  chance to update.

Please practise coordinated disclosure: give the maintainer a reasonable chance
to ship a fix before disclosing publicly.

## Supported versions

Only the **latest released version** is supported. Fixes ship in a new release
rather than being back-ported; upgrade to the latest `fir` to receive security
fixes.
