package safety

import "strings"

func IsNegation(cleaned string) bool {
	if cleaned == "" {
		return false
	}
	lower := strings.ToLower(cleaned)
	for _, marker := range []string{
		"don't",
		"dont",
		"never",
		"nevermind",
		"nvm",
		"never mind",
		"cancel",
		"abort",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
