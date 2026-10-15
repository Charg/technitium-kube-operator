/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a dotted numeric Technitium version such as 15.6.1. Missing
// trailing components are zero, so 15.5 equals 15.5.0.
type Version struct {
	Major, Minor, Patch int
}

// ParseVersion parses a leading dotted numeric version from s, with an
// optional "v" prefix. Anything after the numeric part (for example the
// "-alpine" in "15.6.1-alpine") is ignored. It fails when s does not start
// with a number.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	end := 0
	for end < len(s) && (s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	parts := strings.Split(strings.Trim(s[:end], "."), ".")
	if parts[0] == "" || len(parts) > 3 || strings.HasPrefix(s, ".") || strings.Contains(s[:end], "..") {
		return Version{}, fmt.Errorf("technitium: no version in %q", s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, fmt.Errorf("technitium: invalid version %q: %w", s, err)
		}
		nums[i] = n
	}
	return Version{nums[0], nums[1], nums[2]}, nil
}

// ParseImageVersion extracts the Technitium version from a container image
// reference's tag. It handles registry hosts with ports and digests. ok is
// false when no version can be determined, such as no tag, "latest", a digest
// only, or a custom non-numeric tag.
func ParseImageVersion(image string) (v Version, ok bool) {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	name := image[strings.LastIndex(image, "/")+1:]
	_, tag, found := strings.Cut(name, ":")
	if !found {
		return Version{}, false
	}
	v, err := ParseVersion(tag)
	if err != nil {
		return Version{}, false
	}
	return v, true
}

// Compare returns -1, 0 or 1 as v is older than, equal to, or newer than o.
func (v Version) Compare(o Version) int {
	for _, d := range []int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		switch {
		case d < 0:
			return -1
		case d > 0:
			return 1
		}
	}
	return 0
}

// String renders the version as major.minor.patch.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}
