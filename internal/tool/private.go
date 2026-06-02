package tool

import (
	"regexp"
)

func ReplaceMasks(input string, masks map[string]string) string {
	pattern := regexp.MustCompile(`<mask:[^>]+>`)
	return pattern.ReplaceAllStringFunc(input, func(match string) string {
		if val, ok := masks[match]; ok {
			return val
		}
		return match
	})
}
