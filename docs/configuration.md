# Configuration

```sh
plan-forge config --show
```

Sets the relay URL, the main and critic host/model pair, and the human workday baseline used for estimates. Relay
credentials can only be set from your own terminal (`plan-forge config --set-relay-token`), never through an agent skill.

The `/plan-config` slash command reviews and changes the same settings from inside your agent.

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
