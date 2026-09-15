package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 240 is the backstop for the v0.3.4 incident: it restores kiro into the two
// platform CHECK constraints for deployments that successfully applied the
// kiro-less 238 (so the fix to 238 itself never re-runs for them).
func TestRestoreKiroPlatformConstraintsMigration(t *testing.T) {
	content, err := FS.ReadFile("240_restore_kiro_platform_constraints.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	// Must be idempotent: only rebuild when the constraint has no kiro yet.
	require.Contains(t, sql, "position('kiro' IN quota_constraint_def) = 0")
	require.Contains(t, sql, "position('kiro' IN route_constraint_def) = 0")

	// Rebuilt constraints must be the full superset, not just kiro — dropping
	// minimax or opencode_go here would recreate the very bug this fixes.
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'kiro'))")
	require.Contains(t, sql,
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'kiro'))")
}

// TestPlatformCheckMigrationsKeepKiro pins the invariant that broke twice
// (v0.3.1 via 237, v0.3.4 via 238): every migration that rebuilds one of the
// two platform CHECK constraints must keep kiro in the whitelist.
//
// It inspects the whitelist inside the ADD CONSTRAINT ... CHECK (...) clause,
// not the file text: a prose mention of kiro in a comment must not satisfy it.
func TestPlatformCheckMigrationsKeepKiro(t *testing.T) {
	entries, err := FS.ReadDir(".")
	require.NoError(t, err)

	clauses := []struct {
		constraint string
		column     string
	}{
		{"user_platform_quotas_platform_check", "platform"},
		{"composite_model_routes_target_platform_check", "target_platform"},
	}

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		content, err := FS.ReadFile(name)
		require.NoError(t, err)
		sql := strings.Join(strings.Fields(string(content)), " ")

		for _, c := range clauses {
			for _, list := range checkWhitelists(sql, c.constraint, c.column) {
				// 234 predates minimax/opencode_go; scope to the migrations that
				// rebuild the constraint from the modern platform list.
				if !strings.Contains(list, "'minimax'") {
					continue
				}
				checked++
				require.Containsf(t, list, "'kiro'",
					"%s rebuilds %s but drops the fork-only kiro platform (whitelist: %s); "+
						"deployments holding platform='kiro' rows fail ADD CONSTRAINT and crash-loop "+
						"(v0.3.1 and v0.3.4 incidents)", name, c.constraint, list)
			}
		}
	}

	// Guard the guard: if the scan matches nothing, the assertion above is vacuous.
	require.NotZero(t, checked, "no platform CHECK whitelists were inspected — the scan pattern no longer matches")
}

// checkWhitelists returns the whitelist bodies of every
// "ADD CONSTRAINT <constraint> CHECK (<column> IN (...))" clause in sql.
func checkWhitelists(sql, constraint, column string) []string {
	var out []string
	prefix := "ADD CONSTRAINT " + constraint + " CHECK (" + column + " IN ("
	rest := sql
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return out
		}
		rest = rest[i+len(prefix):]
		end := strings.Index(rest, ")")
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
	}
}
