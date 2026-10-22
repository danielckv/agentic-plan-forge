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
configuration naming it always falls back. Reasoning:
[ADR 0004](adrs/0004-section-size-and-host-failure-detail.md).

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
