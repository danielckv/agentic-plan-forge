Plan Forge supports Antigravity through skills in both documented global locations:

- `~/.gemini/antigravity-cli/skills/{plan-forge,plan-config}/SKILL.md` (Antigravity CLI)
- `~/.gemini/config/skills/{plan-forge,plan-config}/SKILL.md` (Antigravity App/IDE)

Both locations receive identical skill bodies. `plan-forge install --host agy` writes both. Skills become `/plan-forge` and `/plan-config` slash commands automatically.

SessionStart hook: the documented global hooks file is `~/.gemini/config/hooks.json` (command-type handlers, JSON on stdin/stdout). To add the plan-status notice manually, merge this entry:

```json
{
  "plan-forge": {
    "SessionStart": [
      {
        "type": "command",
        "command": "\"{{PLAN_FORGE_BIN}}\" hook check-state --format agy",
        "timeout": 2
      }
    ]
  }
}
```
