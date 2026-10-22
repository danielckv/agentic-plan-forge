# ADR 0003: Do not scan the composed requirements text

- Status: accepted
- Date: 2026-10-22
- Supersedes: the `FREEZE_REQUEST` secret scan specified in
  [PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md](../plans/PLAN_FORGE_COMPLETE_SYSTEM_PLAN.md) (Page 1, footer).

## Context

Page 1 of the planning window holds one shared Markdown document, `prd`: the requirements the user writes, pastes or
uploads. Before this change, confirming the context ran a secret scan over `prd`. The scan had two parts:

- nine regular expressions for known token shapes (cloud keys, PEM headers, vendor API key prefixes);
- an entropy rule that flagged any run of 24 or more characters with at least 4.0 bits of entropy per character.

Any hit refused the confirmation with "N possible secret(s) found". The scan failed in three ways:

1. **It refused ordinary planning text.** The entropy rule flagged repository paths (`github.com/<owner>/<repo>` scores 4.30), branch names (4.20) and Go test names (4.07). A plan that names the code it changes trips it.
2. **The window could not clear it.** The page never called the resolve endpoint, so a refused confirmation stayed refused. The only way out was to delete the text.
3. **It guarded the wrong boundary.** The text goes to the user's own signed-in agent CLI on the user's machine. The user chose to write it. Remote mode sends it to a relay the user configured.

The new pre-planning context step adds more repository paths and identifiers to the planning flow. The scan would refuse
more confirmations still.

## Decision

Remove the scan. Plan Forge does not inspect, redact or block the user's requirements text.

Removed: `internal/app/secrets.go`, the executor's scan and redaction (`SecretHits`, `ResolveSecret`, `beforeFreeze`),
`GET /api/secrets`, `POST /api/secrets/resolve`, the `secrets` status field, the owner `EditDoc` path that existed only
for redaction, and the window's chips and Confirm gate.

These protections stay, because they protect something other than the user's own words:

- size and encoding bounds on the document, uploads and frames;
- file-ingestion bounds (size, pages, archive nesting, magic bytes);
- relay authentication and the browser-origin refusal;
- the relay egress guard on control events (the document channel was already exempt);
- the tool-free tripwire on avatar runs;
- Mermaid sanitizing of model output in the window.

## Consequences

- Confirming the context depends only on the document being non-empty and the upload being finished.
- A credential pasted into the requirements reaches the host agent CLI, and in remote mode the relay. That is the user's
  choice, as it is in any chat with the same agent. [SECURITY.md](../../SECURITY.md) does not promise otherwise.
- `internal/app/freeze_test.go` keeps the decision: a document full of paths, hashes and key-shaped strings confirms.
