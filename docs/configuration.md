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

## One run, every host you are signed in to

With no `main_host` set, each avatar role is placed on its own host, so one host's rate limit slows one role instead
of the whole run and the critic reviews on a different host than the architect drafted on. `plan-forge config --show`
prints the placement, and `/api/status` carries it with the reason any host was skipped.

| Key | Default | Effect |
| :--- | :--- | :--- |
| `role_models` | unset | Pins a role to a host, a model, or both. Keys: `pm`, `architect`, `specialist`, `critic`, `synthesizer`, `narrator`. A role with no entry is spread across the ready hosts. |
| `main_host` / `judge_host` | unset | Naming one forces every role on that slot onto it. This is how you turn the spread off. |
| `stage_timeout_seconds` | `600` | What one deliberation call may take, 60..1800. The old 180-second cap killed every architect run; see below. |
| `disable_narrator` | `false` | Turns off the avatar that narrates the run. |

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

## Self-host the relay on Cloud Run

Run your own relay as a Cloud Run service instead of the public one. The deploy script builds the image with Cloud
Build, stores the token in Secret Manager, and deploys with the proxy and WebSocket settings the relay expects:

```sh
./scripts/deploy-relay-cloudrun.sh --project my-project
```

Then point your client at the printed URL. Full reference: [relay-cloud-run.md](deploy/relay-cloud-run.md).
