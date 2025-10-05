package cliutil

import (
	"regexp"
	"strings"
)

const redactedPlaceholder = "[redacted]"

var (
	templateVarPattern = regexp.MustCompile(`\$\{[^}]+\}`)
	secretKeyPattern   = regexp.MustCompile(`(?i)\b(` + strings.Join(secretKeys(), "|") + `)\b(\s*[:=]\s*)(?:"([^"]*)"|'([^']*)'|([^"'\s]+))`)
)

func secretKeys() []string {
	keys := []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AZURE_CLIENT_SECRET",
		"GCP_SERVICE_ACCOUNT_KEY",
		"DATABASE_PASSWORD",
		"DB_PASSWORD",
		"POSTGRES_PASSWORD",
		"REDIS_PASSWORD",
		"API_KEY",
		"ACCESS_TOKEN",
		"REFRESH_TOKEN",
		"CLIENT_SECRET",
	}
	escaped := make([]string, len(keys))
	for i, key := range keys {
		escaped[i] = regexp.QuoteMeta(key)
	}
	return escaped
}

// RedactSecrets masks common secret placeholders and sensitive key values from the
// supplied string. It replaces ${VAR} style template references and known secret
// key assignments with a generic [redacted] marker to avoid leaking secrets in
// user-facing output.
func RedactSecrets(message string) string {
	if message == "" {
		return message
	}
	redacted := templateVarPattern.ReplaceAllStringFunc(message, func(match string) string {
		return "${" + redactedPlaceholder + "}"
	})
	return secretKeyPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		submatches := secretKeyPattern.FindStringSubmatch(match)
		if len(submatches) == 0 {
			return match
		}

		key := submatches[1]
		assignment := submatches[2]

		switch {
		case submatches[3] != "":
			return key + assignment + "\"" + redactedPlaceholder + "\""
		case submatches[4] != "":
			return key + assignment + "'" + redactedPlaceholder + "'"
		default:
			return key + assignment + redactedPlaceholder
		}
	})
}
