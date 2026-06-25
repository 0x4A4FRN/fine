package cache

import (
	"regexp"
	"strings"
)

var snowflakeRe = regexp.MustCompile(`\b\d{17,20}\b`)

var userMentionRe = regexp.MustCompile(`<@!?\d+>`)

var roleMentionRe = regexp.MustCompile(`<@&\d+>`)

func BuildTemplate(content string, userIDs []string) string {
	result := content

	for _, id := range userIDs {
		result = strings.ReplaceAll(result, "<@"+id+">", "<USER>")
		result = strings.ReplaceAll(result, "<@!"+id+">", "<USER>")
	}

	result = userMentionRe.ReplaceAllString(result, "<USER>")
	result = roleMentionRe.ReplaceAllString(result, "<ROLE>")
	result = snowflakeRe.ReplaceAllString(result, "<USER>")

	return strings.ToLower(strings.TrimSpace(result))
}
