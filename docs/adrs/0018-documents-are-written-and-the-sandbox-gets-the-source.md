# ADR 0018: Generated documents are written on request, and the sandbox receives the source

- Status: accepted

## Context

Two capabilities shipped in v0.8.0 as previews only, and the changelog said so.

The document bundle rendered in memory and the window listed the paths it would
write, but nothing called `storage.CommitBundle`. It took an approval, checked
bounds, refused an existing destination, wrote in path order, undid its own work
on failure and published a manifest last. Every rule was implemented and tested
except the one a person needs: writing the files.

The prepared-VM path created a private E2B sandbox and ran the recorded build,
test and readiness commands in it. `sandbox.Executor.Upload` existed and took a
bounded archive, but no route called it, so a real launch ran commands against a
VM with no project in it. A build that fails because the files were never there
looks like a broken project, and the rent has already started.

## Decision

**Write the documents on an explicit request, and upload the source before the
first command runs.**

`POST /api/documents/export` calls `CommitBundle`. The confirmation is part of
the request body rather than implied by a prior step, because the export is the
only path that puts generated files into a repository a person owns. A
destination that already holds a document stops the whole set before the first
write: a generated file never replaces one a person wrote, and a refused export
leaves the repository untouched rather than half-written. A re-run of the same
approval is success, not a second copy, so a crash mid-export is finished by
pressing the button again.

`sandbox.PackageSource` packs the project into a deterministic gzipped tar and
`Executor.RunRecipe` uploads it before the first command. Determinism matters
because a hash of the archive identifies the revision that was built: entries are
sorted, timestamps are zeroed and modes are normalised, so the same tree always
produces the same bytes.

**What never travels.** `.git`, dependency directories, build output, host state
(`.agents`, `.omp`, `.plan`) and credential files (`.env`, `*.pem`, `*.key`,
`*.log`) are excluded. `.git` is the largest thing in most repositories and the
least useful to a build; a credential that reaches a rented VM is a leak the
owner cannot see. Symlinks are never packed, because a link can point outside the
checkout. An include path that escapes the repository is refused rather than
cleaned. Every skipped path is reported, so an exclusion is never silent.

## Consequences

The documents-only path now writes without a sandbox, a provider key or a
sandbox account, which is what makes plan, spec and decision records useful to a
team that never launches a VM.

A launch now fails before any command runs when the project cannot be packaged,
instead of after the commands fail for an invisible reason. The lease report
carries what was uploaded, so the VM panel shows which files were built and which
were left out.

The exclusions are a fixed list, not a project's own ignore rules. A project that
needs a directory the packager drops has to name it as an include. Reading
`.gitignore` was rejected: it is a suggestion about what to commit, not a
statement about what a build needs, and honouring it would silently drop a
generated script from a directory someone happens to ignore.

Nothing here has been run against a live provider. The packager and the export
are covered by tests against a temporary directory and a local double; a real E2B
launch still needs spend authorization.

## Alternatives considered

Write the documents as a side effect of approval. Rejected: approval is a
decision about the plan, and writing files into a repository is a separate act
with its own consequences. A person who approves a plan to read it should not
find four new files in their working tree.

Clone the repository inside the sandbox. Rejected: the guest would need a
credential with repository read access, and a clone brings the whole history to
a machine that has no use for it. The archive is bounded and allowlisted; a clone
is neither.

Upload on launch instead of on run. Rejected: a launch is the rent starting, and
packaging can fail for reasons unrelated to the provider. Packaging first keeps a
failure free.
