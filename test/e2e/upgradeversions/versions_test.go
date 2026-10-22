package upgradeversions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        [3]string
	}{
		{"baseline", "2.4.4,2.5.5,2.6.4", [3]string{"2.4.4", "2.5.5", "2.6.4"}},
		{"modern", " 2.5.5, 2.6.4, 2.7.1 ", [3]string{"2.5.5", "2.6.4", "2.7.1"}},
		{"numeric ordering", "2.8.0,2.9.0,2.10.0", [3]string{"2.8.0", "2.9.0", "2.10.0"}},
		{"prerelease ordering", "2.7.0-rc.1,2.7.0-rc.2,2.7.0", [3]string{"2.7.0-rc.1", "2.7.0-rc.2", "2.7.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, input := range []string{
		"", "2.6.4,2.7.1", "2.4.4,2.5.5,2.6.4,2.7.1", "2.5.5,,2.7.1", "2.5.5,nope,2.7.1",
		"2.5.5,2.6,2.7.1", "v2.5.5,2.6.4,2.7.1", "2.6.4,2.6.4,2.7.1", "2.6.4,2.5.5,2.7.1",
		"2.6.4+build,2.7.0,2.7.1", "2.6.4,2.7.0+build,2.7.1", "2.6.4,2.7.0,2.7.1+build",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := Parse(input)
			require.Error(t, err)
		})
	}
}
