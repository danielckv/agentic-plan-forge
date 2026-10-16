//go:build client && !server

// Package integrations embeds the agent-integration templates the installer
// writes: the plan-forge and plan-config skill bodies, the OMP session-start
// hook factory, the git post-merge managed block, and the Antigravity
// dual-location note. The archive copies under integrations/ in a release are
// byte-identical to these embedded files.
package integrations

import _ "embed"

//go:embed skill-plan-forge.md
var rawSkillPlanForge string

//go:embed skill-plan-config.md
var rawSkillPlanConfig string

//go:embed omp-hook-plan-forge.ts
var rawOMPHook string

//go:embed git-hook-post-merge.txt
var rawGitHook string

//go:embed agy-locations.md
var rawAGYLocations string

// Template variables substituted at install time:
//   {{PLAN_FORGE_BIN}}  absolute, symlink-resolved path of the running binary
//   {{HOST}}            claude|omp|agy
//   {{WAIT_TIMEOUT}}    per-host wait timeout (100s claude, 50s omp, 50s agy)
//   {{PLAN_MODE_HINT}}  per-host plan-mode hint text

// SkillPlanForge is the unsubstituted plan-forge skill body.
func SkillPlanForge() string { return rawSkillPlanForge }

// SkillPlanConfig is the unsubstituted plan-config skill body.
func SkillPlanConfig() string { return rawSkillPlanConfig }

// OMPHook is the unsubstituted OMP session-start hook factory.
func OMPHook() string { return rawOMPHook }

// GitHook is the unsubstituted managed block appended to git's post-merge.
func GitHook() string { return rawGitHook }

// AGYLocations is the Antigravity dual-location note with the manual
// SessionStart hook snippet (its {{PLAN_FORGE_BIN}} is substituted when the
// snippet is printed or written).
func AGYLocations() string { return rawAGYLocations }
