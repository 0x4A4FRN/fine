package safety

import "strings"

var negationMarkersStrong = []string{
	"don't",
	"dont",
	"never",
	"nevermind",
	"nvm",
	"never mind",
	"cancel",
	"abort",
}

func IsNegation(cleaned string) bool {
	if cleaned == "" {
		return false
	}

	lower := strings.ToLower(cleaned)
	for _, marker := range negationMarkersStrong {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
