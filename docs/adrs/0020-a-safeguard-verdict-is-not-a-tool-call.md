# ADR 0020: A safeguard verdict is not a tool call

- Status: accepted
- Date: 2026-10-29
- Follows [ADR 0012](0012-a-broken-host-costs-one-retry-not-the-run.md) and
  [ADR 0015](0015-an-avatar-is-placed-not-a-role.md)

## Context

The owner asked why every run went to OMP when the configuration named other hosts. `plan-forge
doctor` on this machine, 2026-10-29:

```text
agy     found 1.4.9     serves: NO — AGY exposes no verified isolated settings mechanism
claude  found 2.1.312   serves: NO — tool use observed from claude
                        (dangerous_tool_use); generation discarded
omp     found 18.13.3   serves: yes

main_host = claude IGNORED: tool use observed from claude (dangerous_tool_use)
judge_host = claude IGNORED: tool use observed from claude (dangerous_tool_use)
```

AGY has never served: `NewAGY` leaves `isolation` nil and nothing in the tree sets
`selectionVerified`, so `AGY.Preflight` fails closed by construction. That is the deliberate
position of ADR 0012 and is unchanged here.

Claude was different. It was signed in, current, and used no tools — and every call was thrown
away. A captured probe stream says why. Claude Code 2.1.312 attaches classifier verdicts to the
final `message_delta`:

```json
"safeguard_results": [
  {"type": "dangerous_tool_use", "status": {"type": "available", "tool_uses": {}}}
]
```

`tool_uses` is empty. The classifier checked for dangerous tool use and found none. But the
verdict is named after the check it performed, and `findToolEvent` in `internal/host/stream.go`
matches any `type` or `subtype` whose value contains `tool` followed by `use`, `call`, `execution`
or `result`. `dangerous_tool_use` normalises to `dangeroustooluse`, which contains `tooluse`.

So the tool-free gate read a clean bill of health as a confession. With AGY gated and Claude
discarded, one host was left, and every avatar on a three-host machine went to OMP — and a run
died whenever OMP returned something the schema rejected.

Suffix matching does not separate the two: `dangerous_tool_use` ends in the tool token exactly as
`server_tool_use` and `mcp_tool_use` do. The difference is structural, not lexical. A safeguard
result is a report *about* the content; it is never content.

## Decision

`findToolEvent` does not read a `safeguard_results` subtree as content. It hands the subtree to
`findSafeguardToolUse`, which reports a violation only when the verdict's own `tool_uses` map
names a tool. An empty or absent map is clean, however the check is named.

Everything else is unchanged. A `tool_use`, `server_tool_use`, `mcp_tool_use`, `tool_result` or
`toolCall` content block still discards the generation, and a safeguard verdict that does name a
tool still discards it, reporting the tool's own name rather than the check's.

The regression test is built from the captured probe, not from a hand-written shape.

## Consequences

Claude serves again on this machine. The live role probe, `PLAN_FORGE_LIVE_ROLES=1`, places all
six avatars — pm, architect, specialist, critic, synthesizer, narrator — on Claude and gets an
answer from every one in 18.1s. Before the change it got none.

The tool-free guarantee is narrower in wording and the same in force: the claim is no longer "no
field anywhere in the stream is named after a tool", which was never what the contract meant, but
"no tool ran", which is what the verdict actually reports.

A host that buries a real tool use inside a subtree named `safeguard_results` without listing it
in `tool_uses` would pass. No host does, and such a stream would be lying rather than ambiguous.
