# Configuration

```sh
plan-forge config --show
```

`/plan-config` changes the same settings from inside your agent, and an interactive `plan-forge install` offers a
terminal editor over `config.json`. All three save through the same validation. Relay credentials can only be set from
your own terminal (`plan-forge config --set-relay-token`), never through an agent.

## Models

A plan runs either through a coding-agent CLI you are signed in to, or over a provider API with your own key. Open
**Setup** in the window to add a key; a first run without one opens it for you.

| Provider | Key from | Environment variable |
| --- | --- | --- |
| Anthropic | <https://platform.claude.com/settings/keys> | `ANTHROPIC_API_KEY` |
| Google | <https://aistudio.google.com/apikey> | `GEMINI_API_KEY` |
| OpenAI | <https://platform.openai.com/api-keys> | `OPENAI_API_KEY` |
| Fireworks | <https://app.fireworks.ai/settings/users/api-keys> | `FIREWORKS_API_KEY` |

An environment variable wins over the stored key. Keys are shown masked and never written to a log or a receipt.

**There is no subscription sign-in.** Anthropic reserves Claude.ai OAuth for its own applications, and Fireworks has no
OAuth at all. A Claude or ChatGPT subscription is reached only by running that vendor's own CLI, which is exactly what
the CLI hosts do and is explicitly permitted. So: no API key needed if you use Claude Code or Antigravity; a key for
everything else.

An API provider is preferred over a CLI host when it has a key, because it is faster — no process per call, the prompt
prefix is cached between stages, and the reply is constrained to the schema rather than repaired afterwards.

A model id is host-specific. `claude-opus-5-5-high` is an Antigravity name; Claude Code calls that family `opus` and
404s on anything else. `plan-forge models` lists what each signed-in host offers.

`omp` serves on an explicit pin only — never a default, never a fallback. One tool-free call costs 2.83s wall against
1.10s of model time and about 2,900 tokens of the CLI's own scaffolding, on a run of 25 to 60 calls. The pin is scoped
to what named it.

## Keys

| Key | Default | Effect |
| :--- | :--- | :--- |
| `role_models` | unset | Pins a role to a host, a model, or both. Roles: `pm`, `architect`, `specialist`, `critic`, `synthesizer`, `narrator`, `tech_lead`, `code_reviewer`, `gap_agent`. An unpinned role spreads across the ready hosts. |
| `main_host` / `judge_host` | unset | Forces every role on that slot onto one host. This turns the spread off. |
| `default_main_model` / `default_judge_model` | unset | Slot-wide model. Dropped for a host that does not list it. |
| `max_parallel` | `4` | Concurrent deliberation calls, 1..4. |
| `max_gap_agents` | `2` | Temporary expertise-gap agents one run may admit, 0..3. |
| `stage_timeout_seconds` | `600` | What one deliberation call may take, 60..1800. |
| `disable_narrator` | `true` | `false` enables the avatar that narrates the run. |
| `browser_path` | unset | The executable that opens the tab. Unset uses your default browser. |
| `deploy_target` | unset | `agent-box`, `docker` or `none`. |

A binding takes a host, a model, or both:

```json
{
  "role_models": {
    "architect":  { "host": "claude", "model": "opus" },
    "specialist": { "host": "anthropic", "model": "claude-sonnet-5-5" },
    "narrator":   { "model": "claude-haiku-5-5" }
  }
}
```

With nothing pinned, each avatar is placed on its own host, so one host's rate limit slows one avatar instead of the
whole run, and the critic reviews on a different host than the architect drafted on. Each workstream is placed
separately, derived from its name, so a retry places it the same way.

## Three settings that break runs

**`main_host` and `default_main_model` are slot-wide**, and the MAIN slot is almost every avatar: the Product Manager,
the architect, every specialist and the Lead Synthesizer. Setting them to something fast and cheap sends the design and
the compilation to a weak model, leaves `judge_host` as the only place a strong model runs, and bypasses any API key
you configured. A weak model also fails the strict schema decode far more often, which reads as "the synthesizer is
broken" when it is a model choice. Leaving both unset is the supported configuration.

**`stage_timeout_seconds` below 600 kills runs on a real repository.** The architect's design call was measured at 110s
on a minimal payload, 8m16s on an actual codebase, and a specialist at 6m14s. A timed-out design call retries by
halving its payload, twice, each attempt spending the full budget first — at 360s that is about eighteen minutes before
the run says anything. Symptoms: `architect stage failed: context deadline exceeded`, or a run that feels slow and then
produces nothing.

Pick the architect's model for reasoning, not price: `opus` finished one design call in 110s using 5,960 thinking
tokens; `sonnet` took 282s and 34,287 for the same answer. Spread load by moving the parallel roles — specialists,
documentation batches, narrator — never the design.

## Logs

Every `run` and `join` writes `~/.plan-forge/logs/plan-forge-YYYY-MM-DD.log`, mode 0600, deleted after two days. A
detached session also writes a per-session log under the cache, which `plan-forge wait` reads.

## After the approval

An approved plan is implemented in a git worktree under the Plan Forge cache, on a `plan-forge/<approval-id>` branch —
never in your repository, never to a remote. You merge it yourself. The **Code** tab shows the diff; **Preview** builds
the worktree's root `Dockerfile` and runs it on loopback, framed and sandboxed; **Deploy** publishes that same image.
`docker` needs the Docker CLI, `agent-box` needs the AgentBox CLI.

Why each boundary sits where it does: [decisions.md](decisions.md).
