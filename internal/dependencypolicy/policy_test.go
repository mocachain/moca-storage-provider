package dependencypolicy

import (
	"fmt"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMocachainModulesKeepChecksumVerificationEnabled(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	require.NoError(t, err)
	require.NotContains(t, string(dockerfile), "GOPRIVATE=github.com/mocachain")
	require.NotContains(t, string(dockerfile), "GONOSUMDB=github.com/mocachain/*")
	require.NotContains(t, string(dockerfile), "GONOSUMCHECK=github.com/mocachain/*")
}

// Dependency pins cover direct requirements and every replacement target.
// Indirect requirements are selected by the upstream module graph.
func dependencyPinViolations(data []byte) ([]string, error) {
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return nil, err
	}
	pinnedReplacement := make(map[string]bool)
	var violations []string
	for _, r := range f.Replace {
		if !immutableVersion(r.New.Version) {
			violations = append(violations, fmt.Sprintf("replace %s => %s %s", r.Old.Path, r.New.Path, r.New.Version))
		} else if r.Old.Version == "" {
			pinnedReplacement[r.Old.Path] = true
		}
	}
	for _, r := range f.Require {
		if !r.Indirect && !immutableVersion(r.Mod.Version) && !pinnedReplacement[r.Mod.Path] {
			violations = append(violations, fmt.Sprintf("require %s %s", r.Mod.Path, r.Mod.Version))
		}
	}
	return violations, nil
}

func immutableVersion(version string) bool {
	if !semver.IsValid(version) {
		return false
	}
	if module.IsPseudoVersion(version) {
		_, err := module.PseudoVersionTime(version)
		return err == nil
	}
	return semver.Prerelease(version) == ""
}

func TestDirectDependenciesAndReplacementsArePinned(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	violations, err := dependencyPinViolations(data)
	require.NoError(t, err)
	require.Empty(t, violations, "use release versions or commit pseudo-versions")
}

func TestDependencyPinPolicy(t *testing.T) {
	tests := []struct {
		name           string
		directives     string
		wantViolations int
	}{
		{"release", "require example.com/a v1.2.3", 0},
		{"rc tag", "require example.com/a v1.2.3-rc1", 1},
		{"beta tag", "require example.com/a v1.2.3-beta.5", 1},
		{"custom tag", "replace example.com/a => example.com/b v1.2.3-moca.1", 1},
		{"alpha replacement", "replace example.com/a => example.com/b v1.2.3-alpha.regen.1", 1},
		{"pseudo version", "require example.com/a v1.2.4-0.20260320043135-b1f1e6fd4e65", 0},
		{"v2 pseudo without base", "require example.com/a/v2 v2.0.0-20220913044829-782d4b3284fe", 0},
		{"v0 pseudo without base", "replace example.com/a => example.com/b v0.0.0-20240909101200-6bec9fa64975", 0},
		{"rc pseudo version", "require example.com/a v1.2.3-rc0.0.20260320043135-b1f1e6fd4e65", 0},
		{"invalid pseudo timestamp", "require example.com/a v1.2.3-rc0.0.20261320043135-b1f1e6fd4e65", 1},
		{"indirect rc", "require example.com/a/v2 v2.0.0-rc.3 // indirect", 0},
		{"indirect alpha", "require example.com/a v1.4.0-alpha.1 // indirect", 0},
		{"whole module release replacement", "require example.com/a v1.0.0-beta.5\nreplace example.com/a => example.com/b v1.0.0", 0},
		{"whole module pseudo replacement", "require example.com/a v1.0.0-beta.5\nreplace example.com/a => example.com/a v1.0.0-beta.4.0.20260320043135-b1f1e6fd4e65", 0},
		{"version specific replacement", "require example.com/a v1.0.0-beta.5\nreplace example.com/a v1.0.0-beta.5 => example.com/b v1.0.0", 1},
		{"mutable replacement", "require example.com/a v1.0.0-beta.5\nreplace example.com/a => example.com/b v1.0.0-rc1", 2},
		{"unrelated replacement", "require example.com/a v1.0.0-beta.5\nreplace example.com/b => example.com/b v1.0.0", 1},
		{"local replacement", "replace example.com/a => ../a", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations, err := dependencyPinViolations([]byte("module example.com/main\n" + tt.directives + "\n"))
			require.NoError(t, err)
			require.Len(t, violations, tt.wantViolations)
		})
	}
}
