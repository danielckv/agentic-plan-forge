# ADR 0022: API providers join the CLI hosts, authenticated by API key

- Status: accepted
- Date: 2026-10-29
- Extends [ADR 0012](0012-a-broken-host-costs-one-retry-not-the-run.md) and
  [ADR 0019](0019-a-malformed-reply-costs-one-repair-not-the-run.md)

## Context

Every generation went through a local agent CLI. Two costs followed, and neither was fixable
inside that design.

**Time.** Measured on the owner's machine, one trivial tool-free `claude -p` call takes 2.83 s
wall. Only 1.10 s of that is the model (`duration_api_ms`): 0.59 s passes before the HTTP
request is issued (`time_to_request_ms`) and about 1.15 s goes to teardown after the result
arrives. The CLI also prepends its own scaffolding — 2,272 cache-creation plus 649 cache-read
tokens for a two-token prompt — on every call. A run issues roughly 25 to 60 calls: one product
manager, two architect parts, a documentation batch per drift group, up to four specialists and
one critic per cycle, across up to three cycles, plus synthesis, ingestion sections and impact
batches. That is 42 to 100 seconds of process overhead per run before any planning happens,
and about 2,900 tokens per call that Plan Forge never asked for.

**Breakage.** The adapters bind to CLI internals that change without notice. ADR 0012 exists
because a host passes `Detect`, `Auth` and `Preflight` and still fails on the first streamed
event. PR #25 was needed because Claude Code 2.1.312 began attaching `safeguard_results` to a
message delta, `findToolEvent` read the check's own name as a tool use, and every Claude call
was discarded — `doctor` reported `serves: NO` for a host that had used no tools at all. The
unwrapping in `jsonCandidate` and the repair loop in ADR 0019 exist because a CLI wraps a reply
in prose. Each fix is correct and none of them ends the pattern.

**What was asked for, and why it is not what shipped.** The request was to rewrite the provider
layer onto an API with OAuth sign-in for Claude, agy, Codex and Fireworks. OAuth against a
consumer subscription is not available to a tool like this one. Anthropic's legal and compliance
page states it directly:

> Anthropic does not permit third-party developers to offer Claude.ai login into their own
> applications, or to route requests through Free, Pro, or Max plan credentials on behalf of
> their users. Moreover, developers may not collect, store, or intermediate Claude.ai
> credentials or session tokens — sign-in to a Claude account must complete through Anthropic's
> own flow.

It directs developers building on Claude to an API key from the Console, and enforcement against
third-party harnesses using subscription OAuth began during 2026. The same page explicitly
permits what Plan Forge already does — "an end user signing in to the unmodified Claude Code
binary with their own Claude subscription". Fireworks authenticates with an API key and offers
no OAuth at all. OpenAI documents a Sign-in-with-ChatGPT token-sharing flow for local
open-source apps, but its terms for hosted or commercial use are not stated clearly enough to
build a public tool on.

So the subscription path and the developer path are different credentials with different rules,
and a tool that pretends otherwise either breaks the terms or breaks when enforcement arrives.

## Decision

**API providers are new hosts beside the CLI hosts, never a replacement, and they authenticate
with the owner's own API key.**

`KindAnthropic`, `KindOpenAI` and `KindFireworks` implement the existing `host.Adapter` and join
the same registry as `claude`, `omp` and `agy`. Placement, the ADR 0012 demotion loop, the
installer, the managed hooks and the skills are untouched.

The API kinds lead every entry in `roleHostOrder`, because they are the faster transport. A
provider with no key fails `Detect` on a cached file read, costing the walk nothing, so an
installation with no keys behaves exactly as it did before and lands on its CLI hosts. Two
placements are deliberate rather than a rotation: the reasoning-heavy roles lead with Anthropic,
because ADR 0009 settled that the architect's design stays on the strongest model and that load
spreads by role rather than by downgrading the model that designs; the narrator leads with the
cheapest provider, because it writes a status line that gates nothing.

**Keys live outside `config.json`.** `~/.plan-forge/credentials.json`, mode `0600`, written
through a temporary file and a rename. The settings file is quoted in `doctor` output and pasted
into bug reports, and a key must never travel with it. An environment variable
(`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `FIREWORKS_API_KEY`) overrides the stored file, so a
shell export, a secrets manager or a CI runner wins without the owner deleting anything. The
key is never echoed by an endpoint, never written to a receipt and never logged: the window
only ever receives a masked form, and the save endpoint replies with that same masked status so
a key cannot be read back by replaying it.

**Output is constrained at the provider, not repaired afterwards.** Anthropic structured
outputs (`output_config.format`) and the OpenAI-compatible `response_format: json_schema` take
the schema from the request, so a reply is schema-valid by construction. A schema the provider
refuses costs one retry without the constraint and then falls through to the existing
unwrap-and-repair path, because a rejected schema must not cost the stage. Forced `tool_choice`
is **not** used: it returns 400 on the current Anthropic models.

**The prompt prefix is cached.** A breakpoint on the system block, which is stable for a whole
run, and one on the user payload when it exceeds 8 KiB, which repeats across revision cycles.
ADR 0009 measured the architect design call at 94,184 input tokens; at Opus 5.5 rates a cache
read is $0.20 per million against $4.00, so a prefix that survives into the next cycle costs a
twentieth and starts streaming sooner.

**Models are resolved, not hardcoded.** Both OpenAI-compatible providers rotate their served
models — Fireworks retired nine serverless models in a single day in October 2026 — so the
adapter asks `/models` and takes the first preference actually served, remembering it for the
process. A constant baked in at release time becomes a "model not found" on every stage.

## Consequences

- An installation with an API key spends its run on model reasoning instead of process startup.
  The size of that win is not asserted here: it is measured by the live receipt described in the
  verification section of the change, and until that runs the numbers above bound only the
  overhead removed, not the end-to-end run time.
- `plan-forge doctor` and the Setup window gain a provider list. Startup preflights more hosts,
  but an unconfigured provider costs one cached file read and a configured one costs a single
  `/models` call that spends no tokens.
- The repair machinery stays. It is still the path for the CLI hosts, and the fallback when a
  provider rejects a schema.
- `MaxParallel` is still 4. It was sized for subprocesses and the HTTP transport no longer needs
  that bound, but raising it is a separate change that has to be measured against real provider
  rate limits.
- The Anthropic adapter uses the official Go SDK, which costs one direct dependency and
  eighteen indirect ones, OpenTelemetry among them, in a module file that had twelve. The SDK
  was chosen for typed access to structured outputs and cache control rather than for the
  transport. The OpenAI-compatible adapter shows the alternative works: it speaks the same
  class of API over `net/http` with no dependency at all. If the dependency weight proves worse
  than the typing is worth, the Anthropic adapter can be rewritten the same way without
  touching anything above `host.Adapter`.
- A consumer subscription remains reachable only through that vendor's own CLI. If Anthropic or
  OpenAI later publishes terms that permit a third-party harness to use a subscription, this
  decision is revisited; it is not revisited by reading the terms more optimistically.
