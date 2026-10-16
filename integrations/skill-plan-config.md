---
name: plan-config
description: Review and change Plan Forge settings (generation hosts and models, workday hours). Use when the user runs /plan-config or asks to change Plan Forge configuration.
---

# Plan Forge — configuration

1. Run `"{{PLAN_FORGE_BIN}}" config --show --json` and summarize the current settings to the user.
2. Ask the user which values to change: main/judge host and model (list candidates from `plan-forge models --json`) and workday hours.
3. Apply the changes with `plan-forge config --model-main <id> --model-judge <id> --main-host <host> --judge-host <host> --workday-hours <n>`, passing only the values the user chose.

Relay URL changes and relay tokens require the user's own terminal: tell the user to run `plan-forge config --relay-url <url>` (interactive origin confirmation) and `plan-forge config --set-relay-token` (hidden prompt). Never handle secrets or relay origins yourself.
