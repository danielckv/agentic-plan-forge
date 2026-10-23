---
name: plan-config
description: Review and change Plan Forge settings (generation hosts and models, per-avatar model placement, stage timeout, workday hours). Use when the user runs /plan-config or asks to change Plan Forge configuration.
---

# Plan Forge — configuration

1. Run `"{{PLAN_FORGE_BIN}}" config --show --json` and summarize the current settings to the user.
2. Ask the user which values to change: main/judge host and model (list candidates from `plan-forge models --json`), where each avatar role runs, what one deliberation call may take, and workday hours.
3. Apply the changes with `plan-forge config --model-main <id> --model-judge <id> --main-host <host> --judge-host <host> --workday-hours <n> --stage-timeout <seconds> --narrator <on|off> --role <role>=<host>/<model>`, passing only the values the user chose.

## Where each avatar runs

With no `main_host` set, each role is placed on its own host, so a machine signed in to several uses them all. Pin a role with `--role`, repeatable: `--role architect=claude/opus --role specialist=omp/claude-sonnet-5-5 --role narrator=/claude-haiku-5-5`. `--role <name>=` clears a pin and returns the role to the spread. Roles: `pm`, `architect`, `specialist`, `critic`, `synthesizer`, `narrator`.

Setting `main_host` forces every role on that slot onto one host; that is how the user turns the spread off.

Pick the architect's model for reasoning, not for price. One measured design call: `opus` 110s with 5,960 thinking tokens, `sonnet` 282s with 34,287 for the same answer. Move the parallel roles — specialists, narrator — to spread load, never the architect design.

## What a stage may take

`--stage-timeout` bounds one deliberation call, 60..1800 seconds, default 600. Below about 300 the architect stage fails on any repository with real documentation drift. Report a failure that reads `context deadline exceeded` as "the stage ran out of its budget" and offer to raise it.

Relay URL changes and relay tokens require the user's own terminal: tell the user to run `plan-forge config --relay-url <url>` (interactive origin confirmation) and `plan-forge config --set-relay-token` (hidden prompt). Never handle secrets or relay origins yourself.
