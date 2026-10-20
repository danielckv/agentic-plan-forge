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
| `browser_path` | unset | The executable that opens the forge and board tabs. Unset uses the platform URL opener, so the tab lands in your default browser. Set it to a browser executable to send the tab there instead. A value that is not executable is an error, never a silent fall-back to a browser you did not choose. |
| `center_auto_open` | `false` | Whether starting a harness session may open a command center tab. Off by default: starting a session is not a request to look at a dashboard. `plan-forge center open` ignores this and always opens one. |
| `center_port` | `8420` | The loopback port the command center binds. `PLAN_FORGE_CENTER_PORT` overrides it. A port already in use is reported rather than worked around, because a dashboard that moves breaks every bookmark. |

## Self-host the relay on Cloud Run

Run your own relay as a Cloud Run service instead of the public one. The deploy script builds the image with Cloud
Build, stores the token in Secret Manager, and deploys with the proxy and WebSocket settings the relay expects:

```sh
./scripts/deploy-relay-cloudrun.sh --project my-project
```

Then point your client at the printed URL. Full reference: [relay-cloud-run.md](deploy/relay-cloud-run.md).
