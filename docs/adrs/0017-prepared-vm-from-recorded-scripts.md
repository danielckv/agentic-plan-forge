# ADR 0017: Build and preview an approved plan in a prepared VM, from recorded scripts

## Status

Proposed

## Context

Plan Forge stops at an approved spec. `internal/delivery` then keeps development
on the owner's machine: `Build` runs a coding host in a local worktree
(`build.go`), builds an image with the local Docker daemon (`box.go`), and can
push to Agent Box (`deploy.go`). Nothing verifies that the approved design can
actually run, and a team that wants a preview must keep a machine, a Docker
daemon and a browser open.

Two requirements change that path.

The owner's rule is that builds, tests and runs happen only in CI or a prepared
environment, never on a workstation. Page 2 also gains an "outcome first" view,
and the plan needs a way to prove the approved plan works outside the planning
machine.

An approved plan also says *what to build* but not **how this repository builds
it**. A coding agent that improvises `npm run dev` against a project whose
convention is `make serve` produces a plausible failure, and the same guess is
repeated on every run.

## Decision

**Add an optional prepared-VM stage behind the existing Build page.** It launches
a private E2B sandbox (provider `e2b` in `config.SandboxConfig`), runs the
recorded commands, and exposes a preview through a local proxy. Nothing else in
the product changes: plan approval, document export and implementation
acceptance work with the sandbox disabled.

**The project, not the model at deploy time, defines how it is built.** A
versioned `execution_recipe.v1` document records the build, test and run
commands, their working directories, timeouts, toolchain, runtime port,
readiness probe and the hashes of the sources that justified each command. The
recipe is derived once from the repository's own docs, specs, plans, CI
configuration, manifests, lockfiles and existing build assets. Existing `make`,
`package.json`, task-runner and container commands are reused unchanged; a
missing command becomes a reviewed generated script, or an explicit
`not configured`, never an invented passing check.

**The VM executes the recorded recipe and nothing else.** The sandbox receives a
CI-packaged archive of the exact revision plus the approved scripts. No
`git clone` runs on the provider, no model writes shell at deploy time, and a
hash mismatch against the approved recipe blocks execution. Scripts the model
generated are untrusted input even after review: they run with no GitHub write
token and no provider credential.

**Preview traffic crosses an authenticated local proxy.** E2B private sandboxes
require the `e2b-traffic-access-token` header, which a browser cannot set for
navigation. Plan Forge therefore serves the preview on its own origin behind a
single-use nonce exchanged for an HttpOnly cookie, forwards only to the fixed
sandbox host and application port, and never exposes the provider token, the
control session cookie, or a generic URL proxy.

**A launch is spent deliberately.** The owner confirms revision, scripts,
resources, permissions, expiry and the estimated cost before creation; the
default lifetime is one hour; an ambiguous provider result is reconciled by
operation identity instead of retried; and a state of `ready` requires receipts
for build, test and readiness. Deleting a sandbox is attempted on failure,
cancel and expiry, and an unconfirmed deletion is shown as pending rather than
assumed.

**Implementation acceptance stays a separate gate.** A green preview plus the
product manager's sign-off does not accept the implementation on its own: a
server-verified GitHub approval from a developer with write permission, covering
the same head commit and recipe hash, is also required.

## Consequences

Document generation, script preparation and export no longer depend on cloud
credentials. A project with no sandbox configured still produces its plan,
specification, ADRs and recipe, and the window reports the validation state as
`not configured` rather than failed.

Preview correctness depends on the recipe being current. Changing the revision,
scripts, lockfiles or the instructions the recipe cites invalidates earlier
receipts, so a green preview can never outlive the code it described.

Cost is bounded by the provider timeout and by the owner's confirmation. The
window quotes an estimate only when rates are configured; otherwise it says the
estimate is unavailable.

Tests can exercise the provider contract, the lease state machine and the proxy
without a network. A live launch needs explicit spend authorization, and CI
reports it as not live-verified when it did not run.

The local coding and Docker paths remain for owners who choose them, but the
prepared-VM path is the one that proves the plan can build, test and run.
Recorded commands are the single place a build definition lives, so the next run
reuses them instead of paying a model to rediscover the project.

## Alternatives considered

Hard-code a per-language command table. Rejected: it guesses at a project's real
convention and drifts from the CI file the project already maintains.

Let the coding agent decide the commands inside the VM. Rejected: it spends model
calls on every run, cannot be reviewed before spending money, and makes a
successful preview unreproducible.

Expose the E2B public URL directly. Rejected: with public traffic allowed, anyone
holding the URL reaches the application, and with it restricted the provider
header cannot be supplied by browser navigation.
