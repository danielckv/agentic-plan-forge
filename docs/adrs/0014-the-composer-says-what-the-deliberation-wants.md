# ADR 0014: The composer says what the deliberation wants, and the window says what the install can do

- Status: accepted
- Date: 2026-10-25
- Surfaces the host facts [ADR 0012](0012-a-broken-host-costs-one-retry-not-the-run.md) made correct

## Context

Two things the product knew and never told the person using it.

**Page 1 was one textarea and a placeholder.** The deliberation works from five things — who the work
is for, what done looks like, why now, what is out of scope, and what already exists. That is not a
guess: `~/.claude/skills/plan-forge/SKILL.md` interviews for exactly those five, one question per
message, before it ever opens the window. Someone who skips the interview, or who opens the window
directly, gets no hint that any of it is wanted. They write a paragraph, press Confirm, and find out
what was missing from the shape of the plan that comes back an hour later.

**`plan-forge doctor` knew which hosts could serve and printed it to a terminal.** ADR 0012 traced a
run where all three hosts reported `auth: ready`, the owner had pinned `main_host: agy`, and every
avatar quietly went somewhere else because AGY fails `Preflight`. That ADR made the router correct
and added the `serves: NO` line to `doctor`. It did not put the fact anywhere near the person writing
the brief, who is the one who chose the host in the first place.

## Decision

**A Brief tab lists the five topics, ticks the ones the document mentions, and inserts the heading
for one it does not.** `web/src/ui/brief.ts` holds the sections in the skill's own order, so the
window and the pre-launch interview ask for the same things in the same order; a writer who answered
the interview finds the tab already ticked.

**The detection makes no model call.** Whether a brief is good enough is the judgement the four
avatars exist to make. A sixth opinion, rendered as a score in the corner of the composer, would be
slower than the run and less honest than it: it would be guessing at the same question with less
evidence. A word search can report something narrower and falsifiable — the words are there, or they
are not — and that is all this claims.

**It is an outline, not a gate.** Confirm is never disabled by it. A word search is not entitled to
stop a run, and a writer who knows their brief is complete should not have to argue with a checklist
to start one. The wording follows: "not mentioned yet", never "missing"; a tick means the topic is
mentioned, never that it is answered. `briefSummary` is tested against the words *ready*, *complete*,
*good*, *sufficient* and *score*, because the moment it says any of them it is lying.

**The known miss is a test, not a footnote.** Job titles are open-ended. A brief that opens
"Radiographers need the scan queue to survive a restart" answers the audience question perfectly and
reads as not mentioned, because the cue list does not carry *radiographer* and never will carry every
title. `TestReadBrief` asserts that miss directly, so the limit is recorded where someone changing the
cues will see it.

**A Setup panel in the top bar shows hosts, tools, skills and hooks.** `app.Capabilities` probes the
same things `doctor` does and adds what the Build page needs (Docker, AgentBox) and what each tool is
for. The panel is built around one distinction: **signed in** versus **serving**. A host that is
installed, authenticated and still unable to take a call gets its own state and its own colour, and
the host's refusal text is shown verbatim rather than summarised. A `main_host` or `judge_host` being
ignored because of it is named at the top.

## Consequences

The Brief tab opens first in the rail. Impact, Epics, Codebase and Notes are all empty until the
requirements say something, and Brief is the only one that helps before anything is written.

Probing shells out to every installed host CLI and costs seconds, so it runs when the panel opens and
on an explicit re-check, never on a timer. The Go tests share one probe rather than paying for it per
test: six probes was 48s, one is 13s.

`doctor` and `app.Capabilities` now compute overlapping facts in two places. That is deliberate. They
answer different consumers in different shapes — `doctor` carries the browser opener and an OCR map
in a format its own tests pin, the panel carries per-tool purposes and the pinned-host warning — and
forcing one shape on both would make each worse than the duplication does. If a third consumer
appears, extract then.

**What is not addressed.** The five topics are the ones the skill interviews for, and nothing
verifies that they are the five that most change a plan's quality; that would need runs of the same
brief with and without each section, which has not been done. The cue lists are judgement, not
measurement. And the panel reports what the tools are for, not whether a configured deploy target
will work end to end — `TargetAvailable` answers the narrower question of whether the CLI exists.
