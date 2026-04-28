package sitedeploy

import (
	"strings"

	goversion "github.com/hashicorp/go-version"
)

// MatchesTagRef checks if a git tag matches the configured ref.tag setting.
//   - "semver": matches any tag that is a valid semver version
//   - "any": matches any tag
//   - specific value: matches that exact tag name
func MatchesTagRef(tagName string, refTag string) bool {
	switch strings.ToLower(refTag) {
	case "semver":
		// Strip leading 'v' prefix common in git tags
		_, err := goversion.NewSemver(tagName)
		return err == nil
	case "any":
		return true
	default:
		return tagName == refTag
	}
}

// HugoVersionSatisfied checks if the installed hugo version meets the requirements.
func HugoVersionSatisfied(installed string, config *HugoVersionConfig) (bool, error) {
	if config == nil {
		return true, nil
	}

	installedVer, err := goversion.NewSemver(installed)
	if err != nil {
		return false, err
	}

	if config.Version != "" {
		requiredVer, err := goversion.NewSemver(config.Version)
		if err != nil {
			return false, err
		}
		return installedVer.Equal(requiredVer), nil
	}

	if config.AtLeast != "" {
		minVer, err := goversion.NewSemver(config.AtLeast)
		if err != nil {
			return false, err
		}
		return installedVer.GreaterThanOrEqual(minVer), nil
	}

	return true, nil
}

// CompareSemverTags compares two semver tag names, returning:
//
//	-1 if a < b, 0 if a == b, 1 if a > b.
//
// Non-semver tags return an error.
func CompareSemverTags(a, b string) (int, error) {
	va, err := goversion.NewSemver(a)
	if err != nil {
		return 0, err
	}
	vb, err := goversion.NewSemver(b)
	if err != nil {
		return 0, err
	}
	return va.Compare(vb), nil
}
