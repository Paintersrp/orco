package cliutil

import (
	"regexp"
	"strings"
)

const redactedPlaceholder = "[redacted]"

var (
	templateVarPattern = regexp.MustCompile(`\$\{[^}]+\}`)
	secretKeyPattern   = regexp.MustCompile(`(?i)\b(` + strings.Join(secretKeys(), "|") + `)\b(\s*[:=]\s*)(["']?)([^"'\s]+)(["']?)`)
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
	return secretKeyPattern.ReplaceAllString(redacted, "$1$2$3"+redactedPlaceholder+"$5")
}
