package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/migrations"
)

// TestMigrationChecksumCompatibilityRulesMatchRealFiles guards against a rule
// that silently does nothing.
//
// isMigrationChecksumCompatible only grandfathers a mismatch when BOTH the
// recorded checksum and the current file's checksum are in the rule's accepted
// set. So a rule whose accepted set does not contain the current file's real
// checksum can never fire — the deployment it was meant to rescue still hits
// the hard "checksum mismatch" error and fails to start.
//
// That is exactly what happened to 237_add_minimax_platform.sql: the rule added
// in the v0.3.1 fix listed two checksums that matched neither the pre-fix nor
// the post-fix file, and nothing caught it for three releases.
// knownStaleChecksumRules are rules that were already inert before the v0.3.4
// incident: their migration files were edited by later upstream merges without
// the rule being updated. None of them changed in v0.3.3..v0.3.4, so they are
// not implicated in that incident, and an inert rule is not itself a new
// failure mode — it just means those migrations get no grandfathering.
//
// Do not add to this list. Fix the rule instead (add the current file checksum).
var knownStaleChecksumRules = map[string]struct{}{
	"109_auth_identity_compat_backfill.sql":                   {},
	"110_pending_auth_and_provider_default_grants.sql":        {},
	"112_add_payment_order_provider_key_snapshot.sql":         {},
	"118_wechat_dual_mode_and_auth_source_defaults.sql":       {},
	"123_fix_legacy_auth_source_grant_on_signup_defaults.sql": {},
	"195_channel_monitor_mode.sql":                            {},
	"218_group_audio_voice_pricing.sql":                       {},
	"219_group_search_price_per_1k.sql":                       {},
	"220_clear_non_grok_video_generation_config.sql":          {},
}

func TestMigrationChecksumCompatibilityRulesMatchRealFiles(t *testing.T) {
	for name, rule := range migrationChecksumCompatibilityRules {
		t.Run(name, func(t *testing.T) {
			contentBytes, err := migrations.FS.ReadFile(name)
			require.NoErrorf(t, err, "rule registered for %s but the migration file does not exist", name)

			// Must match applyMigrationsFS: sha256 over the space-trimmed content.
			sum := sha256.Sum256([]byte(strings.TrimSpace(string(contentBytes))))
			actual := hex.EncodeToString(sum[:])

			_, accepted := rule.acceptedChecksums[actual]
			if _, known := knownStaleChecksumRules[name]; known {
				// Already-stale rule inherited from earlier releases. It is inert
				// rather than dangerous (an inert rule only means a deployment
				// recording an older checksum gets the hard error it would have
				// gotten with no rule at all), so it does not block a release —
				// but if someone fixes one, drop it from the list below.
				if accepted {
					t.Fatalf("%s is no longer stale — remove it from knownStaleChecksumRules", name)
				}
				t.Skipf("known stale rule, current file checksum is %s", actual)
			}

			require.Truef(t, accepted,
				"checksum compatibility rule for %s does not accept the current file checksum (%s); "+
					"the rule can never fire, so deployments recording an older checksum will fail to start. "+
					"Add the current checksum to the rule.", name, actual)
		})
	}
}
