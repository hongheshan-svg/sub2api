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
// knownStaleChecksumRules is intentionally empty.
//
// It briefly held nine rules that were registered with sha256 over the raw file
// bytes instead of the TrimSpace'd content applyMigrationsFS actually hashes,
// which made them inert from the day they were added. They have since been
// recomputed with the runtime's algorithm, so every rule is now live and the
// assertion below applies to all of them.
//
// Do not add to this list. Fix the rule instead (recompute the checksums the
// way applyMigrationsFS does).
var knownStaleChecksumRules = map[string]struct{}{}

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

			// A rule listing only one version can never fire either: the
			// compatibility path is only reached when db != file, and both must
			// be in the set. Such a rule means a historical version is missing.
			require.Greaterf(t, len(rule.acceptedChecksums), 1,
				"checksum compatibility rule for %s lists only one checksum, so it can never fire "+
					"(the path is only taken when the recorded and current checksums differ); "+
					"register the historical version(s) it was meant to grandfather", name)
		})
	}
}
