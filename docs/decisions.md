# Decisions

Why each boundary in Plan Forge sits where it does. One entry per decision, newest first, numbered as it was
when it was taken. The long versions were separate files until 0.11.0; `git log docs/adrs/` still has them.

## 0027. Oh-my-pi gets the skills; it still does not generate

2026-10-31. `plan-forge install` writes the same two skills and the managed hook into every agent it finds,
oh-my-pi included. Installing an agent integration and choosing which model writes the plan are different
questions, and 0026 answered only the second. Oh-my-pi still generates nothing unless it is pinned.

The hook no longer registers a `/plan-forge` command, because a skill of that name would shadow it. It keeps
the session-start status banner and the plan-mode handoff, which now fires when the skill's own `plan-forge
wait` returns an approved plan.

## 0026. Oh-my-pi serves a pin only

2026-10-31. Oh-my-pi generates only when `main_host`, `judge_host` or a `role_models` entry names it, and the
pin covers only what it named. Each of its calls costs about 2.83s wall against 1.10s of model time and carries
roughly 2,900 tokens of the CLI's own scaffolding, on a run that makes 25 to 60 calls. A fallback nobody asked
for is still a silent second choice. Supersedes 0025, which gated it behind a readiness probe instead.

## 0025. Oh-my-pi is the last host

2026-10-31. Superseded by 0026. It kept oh-my-pi in every rotation behind a readiness probe: it served only
when neither Claude Code, a Google credential nor `agy` could.

## 0030. agy generation runs in an isolated profile

2026-10-31. Every `agy` child runs against `$PLAN_FORGE_HOME/profiles/agy` with `HOME` rewritten to it, not
against the machine's own agy sign-in. Plan Forge owns the `settings.json` there and denies every tool family;
a hand-edited profile that re-enables a tool stops generation rather than running with tools. The cost is a
one-time separate sign-in, which `plan-forge doctor` prints.

## 0024. Temporary agents, and a schema on every stage

The architect may propose up to three temporary specialists for expertise the fixed team lacks. The server
admits or rejects each one and records why; an admitted agent is labelled temporary everywhere, so a run-scoped
agent is never read as part of the permanent team. Separately, every avatar answers against an embedded JSON
schema rather than prose instructions alone.

## 0023. A run always produces a plan

When the cycle budget runs out with blocking findings still open, the run does not fail. It raises one decision:
record the blockers on the plan, or pause and revise. The plan opens with a readiness verdict naming every open
finding. The work was paid for; throwing it away helps nobody.

## 0022. API providers join the CLI hosts

Anthropic, OpenAI and Fireworks are hosts beside Claude Code, agy and oh-my-pi, authenticated with the owner's
own API key. They lead the rotation because they are the faster transport. The key lives in
`~/.plan-forge/credentials.json`, outside `config.json`, because that file ends up in bug reports.

## 0021. Independent sections and batches run together

Normalization sections and documentation batches that do not depend on each other are dispatched concurrently,
bounded at four, and folded back in index order. A section that already answered is never called again.

## 0020. A safeguard verdict is not a tool call

A model reply is discarded when it contains a real tool use. A safeguard verdict that merely mentions tools in
its own result tree is not one, and no longer throws the generation away.

## 0019. A malformed reply costs one repair, not the run

A reply wrapped in a markdown fence or surrounded by prose is unwrapped before validation, which costs nothing.
A reply that still fails its schema gets exactly one repair call quoting the schema error. A second failure ends
the stage.

## 0018. Documents are written on request

Generated documents land in a repository only when the request explicitly asks for it. A destination that
already holds a document stops the whole set before the first write: a generated file never replaces one a
person wrote, and a refused export leaves the repository untouched.

## 0017. A prepared VM builds from recorded scripts

The optional sandbox runs the project's own recorded build, test and run commands from a versioned
`execution_recipe.v1` document, not commands a model invents at deploy time. Revision, cost and expiry are
stated before launch. Everything else works with the sandbox disabled.

## 0016. A five-minute budget for ingestion and grounding

Normalization is cached by document text, and ambiguity resolutions are applied in memory with no model call.
Page 1 uses the deterministic lexical impact map by default; the impact model runs only on an explicit
re-interpret. Workstreams and contract claims are bounded.

## 0015. Place an avatar, not a role

A placement is keyed by role *and* lane, where a lane names which avatar of a role is calling — the workstream
id for a specialist, empty for roles that run one avatar. Two workstreams reach for different hosts first and
fall back through the same sequence, so one slow host does not serialise them.

## 0014. The composer says what the deliberation wants

The Brief tab lists the five topics the avatars will ask about, ticks the ones the document already covers, and
inserts a heading for one it does not. The detection is a word search with no model call: whether a brief is
good enough is the judgement the avatars exist to make, and a score in the corner would guess at it with less
evidence.

## 0013. The approved plan is built in a worktree

Implementation happens in a git worktree under the Plan Forge cache, on a `plan-forge/<approval-id>` branch,
never inside the repository. A plan that goes wrong costs a directory. The owner merges the branch themselves;
Plan Forge never commits to their branch, never pushes and never touches a remote.

## 0012. A broken host costs one retry, not the run

When a host fails on a capability rather than on content, the router marks it unusable for the session, forgets
every role placed on it, and places them again. One role pays the cost once; every later role walks past it.

## 0011. Documentation batches leave the critical path

The architect's design part publishes to the specialists immediately, while its diagrams and documentation
batches keep running beside them. The specialists read the summary, constraints, workstreams and contracts, and
the design part carries all four, so nothing they read changes underneath them.

## 0010. A specialist sees its own workstream

Each Domain Specialist receives the code for its own workstream and nothing else. A whole run is measured
end to end against real hosts, per role: call count, wall time and tokens.

## 0009. One budget per stage

A deliberation stage asks for its own timeout, default 600 seconds, bounded to 60–1800. The old 180-second
default stays the budget a caller gets when it asks for none. Roles spread across the hosts the machine is
signed in to, and the window names what each avatar is doing.

## 0008. The Product Manager is judged on code-shaped names only

A symbol name counts as a technical leak only when it could not be ordinary English: it carries `_` or `.`, a
digit, or a capital after the first character. `article` and `Article` are what a sentence looks like;
`ArticleRepository` is not.

## 0007. The architect answers in parts

The architect stage runs as three calls — design, diagrams, documentation tasks — assembled and validated as one
draft. The specialists wait for the design part and for nothing else. The session reports what each stage spent.

## 0006. Bound every piece of evidence, and keep an unfinished run

Every piece of stage evidence has a measured byte budget, and a stage over budget gives up evidence in a fixed
order, cheapest first, rather than failing. An unfinished run stays on disk; a checkpoint whose inputs no longer
match is dropped rather than silently reused.

## 0005. Map the requirements onto the project

The impact map is a sibling of the normalized requirements, never an input to them: the Product Manager receives
product fields only, so product decisions are not made by code-level accident. The model judges candidates it
was shown and never writes a file path, so a fabricated path is structurally impossible.

## 0004. Size a section by the clock

A normalization section is 8,000 characters, sized from measurements against the call timeout. A section that
times out is replaced by its halves and retried down to a 1,000-character floor — strictly smaller work, not the
same work billed twice. The host's own failure text is carried into the error, stripped and bounded.

## 0003. Do not scan the composed requirements

Plan Forge does not inspect, redact or block the text the user writes. Size and encoding bounds, file-ingestion
limits, relay authentication and the browser-origin check all stay: they protect something other than the user's
own words.

## 0002. Ground planning in the project's own notes

Before any model call, `internal/gather` collects what the project already says — decision records, specs,
plans, session notes — plus branch and recent commits, and passes it to the avatars as data. `plan-forge
context` prints the same result.

## 0001. Remove the live task views

Plan Forge ends at the approved plan and the plan-mode handoff. No daemon, no board, no swarm view. Session
start runs one bounded status check; it never starts a process and never opens a tab.
