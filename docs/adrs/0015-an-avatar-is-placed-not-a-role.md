# ADR 0015: Place an avatar, not a role, and let the router own the model

- Status: accepted
- Date: 2026-10-25

## Context

ADR 0009 spread the avatar roles across the hosts an installation is signed in to, so one host's rate
limit slows one role rather than the whole run. It placed a *role*: `Router.byRole` is keyed by
`contracts.RoleID`, and `PlaceRole` caches the first answer so an avatar never changes host mid-cycle.

One role is not one avatar. The Domain Specialist runs once per workstream, and those calls run at the
same time — that is the point of the stage, bounded by `max_parallel`. They all shared the single
`specialist` placement, so a cycle with four workstreams sent four concurrent calls to one host. The
spread that ADR 0009 bought was worth nothing for the only stage that fans out, which is the stage
where it is worth the most.

The second fault was found while reading that code. Each stage spec carried a model id:

```go
avatar: relay.StagePM, role: contracts.RoleProductManager, slot: contracts.SlotMain, model: o.in.Models.Main,
```

`Models.Main` is `config.DefaultMainModel`, and `Router.GenerateStream` treats a model named on the
request as final:

```go
req.Model = wanted
if req.Model == "" {
    req.Model = place.Model
}
```

So `place.Model` — the only value computed by `modelForRoleLocked`, which reads `role_models` and then
checks whether the serving host actually lists the configured id — was never used by any avatar.
`role_models["architect"].model` was dead. The ADR 0004 guard against sending one host's id to another
was dead with it: on an installation whose `default_main_model` is the Antigravity id
`claude-opus-5-5-high`, every avatar asked every host for that id, including Claude Code, which lists
`haiku`, `opus` and `sonnet`.

Both faults are invisible to a reading of the configuration file. The key is there, the documentation
describes it, and `plan-forge config --show` prints it back.

## Decision

**A placement is keyed by role and lane.** `Router.RoleLane{Role, Lane}` replaces `RoleID` as the
cache key. A lane names which avatar of a role is calling: the workstream id for a Domain Specialist,
empty for every role that runs one avatar. `contracts.GenerateRequest.Lane` carries it with the call,
the way ADR 0012's follow-up made the architect's step travel with the call rather than through a
shared per-avatar record.

**A lane rotates the role's host order; it does not reorder it.** `roleCandidates` enters
`roleHostOrder[role]` at an offset derived from the lane, so two workstreams reach for different
adapters first and fall back through the same sequence. The offset is FNV-1a of the lane name, not a
counter: a counter renumbers the lanes whenever one finishes first, which would move a workstream to a
different host on a retry or a resumed run.

**An explicit host is never rotated.** A `role_models` host, `main_host` and `judge_host` are still
tried first, for every lane. The spread is what happens when the owner chose nothing.

**No stage names a model.** The `model` field is gone from `stageSpec`, and `avatars.ModelSlots` keeps
only what identifies a run for checkpoint compatibility. The router decides the model for every call:
`role_models[role].model` first, then the slot default *if the serving host lists it*, then the host's
own default.

## Consequences

A cycle with four workstreams now reaches up to four hosts. On an installation with one serving host
nothing changes: every lane falls back to it, as before, through the same order.

A slot-wide `default_main_model` is now dropped for a host that does not advertise it, where it used
to be sent anyway. That is the ADR 0004 behaviour finally reaching the avatars. An owner who wants a
specific id on a specific host names both in `role_models`, which is taken as written.

What is not built: a per-workstream *model* in the configuration file. Workstream ids are invented by
the architect at run time, so an owner cannot name one in advance. Pinning `specialist` pins every
workstream; choosing per workstream would need the choice to be made in the room, after the design
part lands, and nothing yet asks for that.

Verified by `TestSpecialistLanesSpreadAcrossReadyHosts` (four lanes, more than one host),
`TestLanePlacementDoesNotDependOnOrder` (the same lane lands on the same host whatever order the lanes
are placed in), `TestPinnedSpecialistHostCoversEveryLane` and `TestRunHappyPathStageOrderAndModels`
(no stage names a model; a specialist carries its workstream as its lane).
