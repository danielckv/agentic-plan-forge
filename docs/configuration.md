# Configuration

```sh
plan-forge config --show
```

Sets the relay URL, the main and critic host/model pair, and the human workday baseline used for estimates. Relay
credentials can only be set from your own terminal (`plan-forge config --set-relay-token`), never through an agent skill.

The `/plan-config` slash command reviews and changes the same settings from inside your agent.

## Hosts and models belong together

A model identifier is host-specific. `claude-opus-5-5-high` is an `agy` name; Claude Code calls the same family `opus`
and answers 404 for anything else. Run `plan-forge models` to see what each signed-in host offers, and
`plan-forge doctor` to see which hosts are ready.

When the host you configured cannot serve a slot, Plan Forge falls back to another signed-in host and still uses the
model you configured — your choice is not second-guessed, because a CLI often accepts identifiers its listing does not
print. If that host does not have the model, the run fails naming it, for example:

```text
Claude generation failed: There's an issue with the selected model (claude-opus-5-5-high). It may not exist or you may
not have access to it.
```

Fix it by setting a model the host that ran actually has.

One host never serves: `agy` fails its preflight because it exposes no way to run with isolated settings, so a
configuration naming it always falls back, and so does any role whose rotation reaches it. It stays in every rotation
and joins the spread with no configuration change the day its preflight can pass. Reasoning:
[ADR 0004](adrs/0004-section-size-and-host-failure-detail.md).

`plan-forge doctor` prints `serves: NO — <reason>` for such a host, and names any `main_host` or `judge_host` that is
being ignored because of it. A host that passes preflight and then breaks the tool-free contract once a call is open —
a stale OMP advertising a tools list is the live case — costs one retry on the next host rather than the stage it was
serving. Reasoning: [ADR 0012](adrs/0012-a-broken-host-costs-one-retry-not-the-run.md).

## One run, every host you are signed in to

With no `main_host` set, each avatar is placed on its own host, so one host's rate limit slows one avatar instead
of the whole run and the critic reviews on a different host than the architect drafted on. `plan-forge config --show`
prints the placement, and `/api/status` carries it with the reason any host was skipped.

The Domain Specialists are placed one by one, not as a group. Four workstreams draft at the same time, so putting all
four on one host is four concurrent calls against one rate limit. Each workstream reaches for a different adapter
first, falling back the usual way when it is not ready. The host a workstream gets is derived from its name, so a
retry, a resumed run and a second window all place it the same way. Reasoning:
[ADR 0015](adrs/0015-an-avatar-is-placed-not-a-role.md).

| Key | Default | Effect |
| :--- | :--- | :--- |
| `role_models` | unset | Pins a role to a host, a model, or both. Keys: `pm`, `architect`, `specialist`, `critic`, `synthesizer`, `narrator`. A role with no entry is spread across the ready hosts. Pinning `specialist` pins every workstream. |
| `main_host` / `judge_host` | unset | Naming one forces every role on that slot onto it. This is how you turn the spread off. A host that cannot serve is skipped; `plan-forge doctor` says so rather than leaving it silent. |
| `max_parallel` | `4` | Maximum concurrent deliberation calls (1..4), matching the 4-workstream cap so all specialists run in one wave. |
| `stage_timeout_seconds` | `600` | What one deliberation call may take, 60..1800. The old 180-second cap killed every architect run; see below. |
| `disable_narrator` | `true` | Turns off the avatar that narrates the run. Set to `false` to enable periodic narration on step transitions ([ADR 0016](adrs/0016-five-minute-deliberation-budget.md)). |

A binding takes a host, a model, or both:

```json
{
  "role_models": {
    "architect":   { "host": "claude", "model": "opus" },
    "specialist":  { "host": "omp", "model": "claude-sonnet-5-5" },
    "narrator":    { "model": "claude-haiku-5-5" }
  }
}
```

A model id reaches a host only if that host lists it. `default_main_model` and `default_judge_model` are slot-wide,
so they are dropped for any host that does not advertise them and that host's own default is used instead — naming
`claude-opus-5-5-high`, which is an Antigravity id, does not send it to Claude Code. A `role_models` model is taken as
written: you named the role and the host together, so it is your choice to make. When an avatar runs on `omp` with no
explicit model in `config.json`, Plan Forge maps the avatar role to your `~/.omp/agent/config.yml` `modelRoles` (`plan`
for `architect`, `judge` for `critic`, `advisor` for `pm`, `smol` for `ingestion`/`impact`/`narrator`, and rotating
`task`/`worker`/`slow`/`explore` across concurrent `specialist` lanes).

Pick the architect's model for reasoning, not for price. Measured on one design call with a 206 KiB payload: `opus`
finished in 110s using 5,960 thinking tokens; `sonnet` took 282s using 34,287 for the same answer. The cheaper model
is the slower one here. Spread load by moving the *parallel* roles — the specialists, the documentation batches, the
narrator — not the design.

## What a stage is allowed to take

One deliberation call gets `stage_timeout_seconds`, default 600. The architect design part reads the whole repository
graph and writes the workstream DAG and every interface contract in one call; it was measured at 110 seconds on
`opus` with a payload carrying no project context, no decisions and no revision. The old 180-second budget left a real
cycle no room, and every run died at `architect stage failed: context deadline exceeded`.

Interpretation and impact mapping still use the 180-second budget their input sizing was measured against. Reasoning:
[ADR 0009](adrs/0009-one-budget-per-stage-roles-across-hosts-and-a-live-room.md).

## What the room shows while it works

Each avatar card names the host and model serving it, the part of the stage in flight (`design 1/3`,
`docs 3/3 · batch 2/5`), how long the call has run and how much it has written, and the evidence that call can see
(`graph 184 files · context 12 excerpts · drift 20 items · 201 KiB`). A running call refreshes that every 10 seconds,
so a slow stage is visibly slow rather than indistinguishable from a hung one.

Above the faces, one avatar narrates: it reads the other avatars' step reports and says what the run is doing. It
holds no draft, gates nothing, and never fails a run — a narration that fails is a lost line and nothing else.

## Where the UI opens

| Key | Default | Effect |
| :--- | :--- | :--- |
| `browser_path` | unset | The executable that opens the forge tab. Unset uses the platform URL opener, so the tab lands in your default browser. Set it to a browser executable to send the tab there instead. A value that is not executable is an error, never a silent fall-back to a browser you did not choose. |

The tab opens only when you run `/plan-forge`. Starting an agent session never opens it.

## After the approval: build, preview, deploy

An approved plan can be implemented, run and deployed without leaving the window. The agent writes into a git worktree
under the Plan Forge cache, on a `plan-forge/<approval-id>` branch — never into the repository you are working in, and
never to a remote. You merge the branch yourself. Reasoning:
[ADR 0013](adrs/0013-the-approved-plan-is-built-in-a-worktree.md).

| Key | Default | Effect |
| :--- | :--- | :--- |
| `deploy_target` | unset | `agent-box`, `docker` or `none`. Unset means the window asks once, then keeps the answer. `none` keeps the pre-0.7 behaviour: the window closes five seconds after an approval and nothing is built. |
| `agent_box_url` | `https://agent-box.sh/` | The Agent Box endpoint. `https` anywhere, `http` on loopback only; no userinfo, query string or fragment. |

The **Code** tab shows the changed files, the diff and what the agent did, step by step. The **Preview** tab builds the
worktree's root `Dockerfile` and runs it on `127.0.0.1`, framed in the page and sandboxed, so the container cannot
reach the window that framed it. **Deploy** publishes that same image — there is one build path, so what you preview is
what you ship.

`docker` needs the Docker CLI; `agent-box` needs the AgentBox CLI (`npm -g i @madarco/agentbox && agentbox install`),
which owns its own provider choice — local Docker, your own hardware over SSH, Hetzner, DigitalOcean, Daytona or
Vercel. The window greys out a target whose CLI is missing instead of failing after a build you waited for.

## What runs the build, and where

The window's **Work** tab reads the repository's own planning files — `.agents/STATE.md`, `docs/plans`, `docs/specs`,
`docs/adrs`, `specs`, root `PLAN*`/`FOLLOW*`/`EPIC*`/`TASK*`/`STATE*` files — and shows the next items exactly as the
state file orders them. Browsing is read-only: it starts no model call and changes no file.

| Key | Default | Effect |
| :--- | :--- | :--- |
| `repository_work_roots` | unset | Extra repository-relative directories to index, for a project that keeps its plans outside the usual hierarchy. Absolute paths and `..` are refused. |

Implementation does not guess how a repository builds itself. Plan Forge derives a versioned `execution_recipe.v1`
from the project's real assets — manifests, lockfiles, CI, `Makefile`, package scripts, task runners and containers —
and stores it beside the approved plan as `execution_recipe.json`. Existing commands are reused unchanged. A missing
build or test command is reported as **not configured**, never as passed.

A prepared sandbox runs that recorded recipe. Plan Forge packages the exact revision in CI, transfers the archive into
the sandbox and executes the recorded commands there. No repository is cloned onto the provider, and the model writes
no shell command at launch time. Running a preview also requires a recorded run command; a project without one can
still be built and tested.

| Key | Default | Effect |
| :--- | :--- | :--- |
| `sandbox.provider` | unset | `e2b` enables the prepared-VM path. Unset keeps every document, implementation and acceptance feature and disables only launching. |
| `sandbox.api_key_env` | `E2B_API_KEY` | The environment variable holding the provider key. A name, never a value; the key stays server-side and outside the guest. |
| `sandbox.template` | `base` | The toolchain image inside the provider. A project whose toolchain differs must name its own template before a launch is allowed. |
| `sandbox.timeout_seconds` | `3600` | The provider's own lifetime. Bounded to 60–3600: an idle sandbox must not outlive the session that paid for it. |
| `sandbox.vcpu`, `sandbox.memory_mb` | `2`, `4096` | Resources for one launch. A workload that needs more says so before money is spent. |
| `sandbox.domain_suffix` | `.e2b.app` | Bounds which hosts the preview proxy can reach. Any other upstream is refused. |
| `sandbox.hourly_cpu_cents`, `sandbox.hourly_memory_cents` | unset | Rates the window quotes before a launch. Without both, it says the estimate is unavailable instead of inventing a price. |

A launch is confirmed before it happens: revision, commands, resources, expiry and the cost estimate. Deleting the
sandbox is attempted on stop, failure and expiry; an unconfirmed deletion is shown as pending, and the provider's own
timeout is the backstop. Reasoning: [ADR 0017](adrs/0017-prepared-vm-from-recorded-scripts.md).

The preview is served by a local proxy on its own origin, not by the provider URL. Browser navigation cannot set the
provider's traffic header, so the proxy injects it server-side behind a single-use link exchanged for an `HttpOnly`
cookie. The control session's cookie and the provider key never reach the previewed application.

**Documents only** needs no sandbox at all: the same plan, specification, ADRs and recipe are generated and exported
without a provider account, and generated scripts stay marked unverified until something runs them.

## Self-host the relay on Cloud Run

Run your own relay as a Cloud Run service instead of the public one. The deploy script builds the image with Cloud
Build, stores the token in Secret Manager, and deploys with the proxy and WebSocket settings the relay expects:

```sh
./scripts/deploy-relay-cloudrun.sh --project my-project
```

Then point your client at the printed URL. Full reference: [relay-cloud-run.md](deploy/relay-cloud-run.md).
