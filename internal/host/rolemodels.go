//go:build client && !server

package host

import "github.com/danielckv/agentic-plan-forge/internal/contracts"

// Which model each avatar asks for when the owner has pinned nothing.
//
// Before this table existed, an unpinned role sent no model id at all and took
// whatever the host picked. On oh-my-pi that meant the owner's own
// `modelRoles` map in `~/.omp/agent/config.yml` decided: a run was observed
// drafting on `together/zai-org/GLM-5.3-Flash`, which that map names for
// widgets. A model chosen for a widget cannot hold an architecture schema.
//
// Plan Forge names its own models now. The owner's pins still win, and every
// id here is a preference resolved against what the host actually lists, the
// same way geminiModelPreference is: an id the host does not serve falls
// through to the next, and an exhausted list leaves the host its own default.

// modelTier is how much reasoning a role needs. It is not a price band: the
// strong tier is cheaper overall, because one call that holds the schema costs
// less than three that do not.
type modelTier string

const (
	// tierStrong decides the architecture or compiles the plan. ADR 0009
	// settled that this tier is never downgraded to spread load.
	tierStrong modelTier = "strong"
	// tierStandard drafts and reviews in parallel. This is where load spreads.
	tierStandard modelTier = "standard"
	// tierLight writes a status line or extracts a bounded field. It gates
	// nothing, and a failure here is dropped.
	tierLight modelTier = "light"
)

// roleTier maps every avatar to the tier it needs. A role missing from the map
// is standard: a new role is drafting work until someone says otherwise.
var roleTier = map[contracts.RoleID]modelTier{
	contracts.RoleArchitect:      tierStrong,
	contracts.RoleSynthesizer:    tierStrong,
	contracts.RoleTechLead:       tierStrong,
	contracts.RoleProductManager: tierStrong,
	contracts.RoleIngestion:      tierStrong,

	contracts.RoleSpecialist:   tierStandard,
	contracts.RoleCritic:       tierStandard,
	contracts.RoleCodeReviewer: tierStandard,
	contracts.RoleGapAgent:     tierStandard,
	contracts.RoleResearch:     tierStandard,
	contracts.RoleImpact:       tierStandard,

	contracts.RoleNarrator: tierLight,
}

// tierModels names the ids each host is asked for, per tier, most wanted
// first. Only the hosts serving Claude or Gemini models have entries: those
// are the families the strong tier is defined in. oh-my-pi has none — it runs
// Plan Forge and never generates for it.
var tierModels = map[Kind]map[modelTier][]string{
	KindAnthropic: {
		tierStrong:   {"claude-opus-5-5"},
		tierStandard: {"claude-sonnet-5-5"},
		tierLight:    {"claude-haiku-5-5"},
	},
	// Claude Code names the family, not the version.
	KindClaude: {
		tierStrong:   {"opus"},
		tierStandard: {"sonnet"},
		tierLight:    {"haiku"},
	},
	KindGemini: {
		tierStrong:   {"gemini-4-argon", "gemini-3.1-pro-preview", "gemini-3-pro-preview"},
		tierStandard: {"gemini-3-flash-preview", "gemini-2.5-flash"},
		tierLight:    {"gemini-3-flash-preview", "gemini-2.5-flash"},
	},
	KindAntigravity: {
		tierStrong:   {"gemini-4-argon", "claude-opus-4-6-thinking", "claude-opus-5-5"},
		tierStandard: {"claude-sonnet-4-6", "gemini-3.8-flash-medium"},
		tierLight:    {"gemini-3.8-flash-low", "gemini-3.8-flash-medium"},
	},
}

// tierFor returns the tier a role needs.
func tierFor(role contracts.RoleID) modelTier {
	if t, ok := roleTier[role]; ok {
		return t
	}
	return tierStandard
}

// defaultModelsFor returns the ids Plan Forge asks kind for when serving role,
// most wanted first. An empty result means this host has no opinion and should
// use its own default.
func defaultModelsFor(kind Kind, role contracts.RoleID) []string {
	return tierModels[kind][tierFor(role)]
}
