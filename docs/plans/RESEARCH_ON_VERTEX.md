# Research on Vertex: remote research agents, reached agent to agent

Status: design, not built. Researched 2026-10-25. Every external fact below carries its source and its
confidence, because two of them change what is buildable.

## Scope: Research mode only

This applies **only when the user is in Research mode**. It is opt-in, and it changes nothing about a
normal run.

| | Default run | Research mode |
| :--- | :--- | :--- |
| Where it runs | your machine | may run remotely |
| Model | the host agent CLI you are signed in to | Vertex, under ADC |
| Network | nothing leaves the machine | reaches GCP, and the web only behind a further flag |
| Tool-free tripwire | on | on for avatars; a research call is a separate kind of call |

No Vertex adapter, no GCP credential and no remote call may appear on the path of a normal
deliberation. A machine with no `gcloud`, no project and no network keeps working exactly as it does
today, and the four avatars keep running through the local CLI. If Research mode is off, none of this
code is reached.

That boundary is a test, not a convention: the default run's generator is resolved by
`internal/host.Router` over the three CLI adapters, and a Vertex generator must be constructible only by
the research batch.

## What was asked for

Research batching that runs on GCP with Vertex AI, authenticated the common way (`gcloud auth`, not
stored credentials), so a research run needs nothing on the local machine — and shaped "like Vertex
agents to agents".

## What the research found

### A2A in Go is real

`github.com/a2aproject/a2a-go` is the official Go SDK, module path `github.com/a2aproject/a2a-go/v2/a2a`.

- Server: `a2asrv.NewJSONRPCHandler(requestHandler)` mounts on a plain `net/http` mux; `a2agrpc.NewHandler`
  and `a2asrv.NewRESTHandler` are the other two transports.
- Agent card: `a2a.AgentCard` carries name, description, version, input and output modes and
  capabilities; the endpoint is declared with `a2a.NewAgentInterface(url, a2a.TransportProtocolJSONRPC)`
  in `SupportedInterfaces`. Discovery is `/.well-known/agent-card.json`.
- Client: `a2aclient.NewFromCard`, `a2aclient.WithConfig` for `PreferredTransports`, and
  `client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})`.
- Governance: an Agentic AI Foundation / Linux Foundation project, 150+ organisations, described as
  production ready at its one-year mark.

Confidence: high on the SDK shape, **low on exact method names and version**. The spec on GitHub and the
page on a2a-protocol.org disagree about JSON-RPC method naming and version headers. Pin the SDK version
and read its own spec before writing a line against it.

### Agent Engine is now Agent Runtime, and Go deployment is unconfirmed

Google's Vertex AI release notes record that **Vertex AI Agent Engine was renamed Agent Runtime on
2026-04-22**. Searching the old name finds stale pages.

ADK is described by several secondary sources as code-first in Python, Go, Java and TypeScript. But
**every deployment path to the managed runtime that the research surfaced is Python** —
`vertexai.agent_engines` / `client.agent_engines.create`, a staging bucket, the
`google-cloud-aiplatform[agent_engines,adk]` extra. No Go deployment guide was found.

Confidence: **low**. This is the one fact that must be checked against the live Agent Runtime
documentation before committing, because it decides the hosting shape.

The fallback is not painful: A2A agents are ordinary HTTP services, and Google's own A2A codelab covers
Cloud Run as well as Agent Engine. A Go agent container on **Cloud Run** gives the same thing without
depending on a Go path through the managed runtime.

### ADC removes the credentials problem

`google.golang.org/genai` is the Go Gen AI SDK. Vertex routing is by environment:
`GOOGLE_GENAI_USE_VERTEXAI`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`. Authentication is
Application Default Credentials: `gcloud auth application-default login` on a workstation, or the
attached service account on Cloud Run, GKE or GCE. The caller needs `roles/aiplatform.user`.

Claude models are available on Vertex through Model Garden as a managed serverless API, using the
Anthropic-native `rawPredict` and `streamRawPredict` endpoints, and Anthropic's own Go SDK supports
Vertex. A third-party Go project (Docker's `docker-agent`) already routes Claude on Vertex this way and
authenticates with ADC, which is evidence the path works in Go.

**This is better than the API key we agreed on earlier.** ADC means Plan Forge stores no secret and asks
for none: it reads the ambient Google credential exactly as it reads the ambient agent-CLI credential
today. The README promise survives a rewording rather than a retraction:

> it uses the credentials your host agent already has

becomes, for this adapter, *the credentials `gcloud` already has, or the service account the job runs
as*. `CLOUD_MODE.md` should be revised to match; the ADR for the credentials change gets easier to
write, not harder.

## The design judgement: what must not become an agent

Plan Forge's avatars are not agents in the ADK sense, and turning them into agents that converse would
dismantle the product.

They are single-shot, tool-free, stateless generations whose order and outcome are decided by
deterministic Go: `internal/avatars` owns the cycle, the checkpoint, the `setHash` barrier that stops a
stale critique certifying newer drafts, the decision cards, and the cycle cap. That machinery is the
reason a Plan Forge run is reproducible and resumable, and it is what the README's "Why" section sells.
Dissolving it into agents talking to each other trades all of it for emergence.

So: **the orchestrator stays deterministic Go. A2A is used at the boundary, not inside the cycle.**

Two uses of A2A that are worth having, in order:

1. **Research workers as remote agents.** A research batch holds questions. Each question is sent to a
   research agent — a Go container on Cloud Run, fronted by an agent card, generating through Vertex
   under ADC — which spends minutes on it and returns findings in the `research_finding.v1` shape that
   is already committed. The batch stays the deterministic part: it owns the budget, the dedup, the
   checkpoint after every finding, and the resolve step that keeps a model from authoring a path. This
   is exactly "research that does not need the local machine", and it is the first thing to build.

2. **Plan Forge as an A2A server.** Publish an agent card so other agents can ask Plan Forge for a
   verified plan. This is the version of "agents to agents" that earns its keep: the gate becomes
   callable by the coding agents it exists to constrain, rather than only by a person in a browser.

## What this changes in the committed work

Nothing committed is wasted. `contracts.ResearchFinding`, `research_finding.v1.json` and
`contracts.RoleResearch` were deliberately transport-free: a finding is a finding whether a local CLI or
a remote agent produced it. The batch engine's shape is unchanged; only the generator moves.

The one revision needed is to `CLOUD_MODE.md`, which records "a provider API key" as the credentials
decision. ADC supersedes it.

## Open, in the order they block work

1. Does Agent Runtime deploy a Go agent? If not, Cloud Run is the host. **Check the live docs.**
2. Which A2A SDK version, and which spec version does it target? Pin both before writing against it.
3. Vertex model choice: Gemini through `google.golang.org/genai`, or Claude through Model Garden and
   Anthropic's Go SDK. The tool-free guarantee is simpler here than on the CLIs — an HTTP call that
   sends no tool declarations cannot make a tool call — but `ToolFreeReceipt` carries `Argv` and has no
   meaning for an HTTP adapter. It needs a second shape before an API adapter can attest anything.
4. Whether a remote research agent may reach the web at all, which is the `--web` trust boundary from
   the research plan, now with a different blast radius because the fetch happens off the user's
   machine.
