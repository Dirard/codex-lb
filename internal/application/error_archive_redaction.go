package application

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// diagnosticTokens masks credentials that commonly appear in free text:
// bearer values, OpenAI/Anthropic keys, JWTs, AWS access key IDs, Google API
// keys, GitHub and Slack tokens and PEM private-key blocks.
var diagnosticTokens = regexp.MustCompile(`(?i)Bearer\s+[^\s"'<>]+|\bsk-[A-Za-z0-9_-]{8,}|\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+` +
	`|(?-i:\b(?:AKIA|ASIA)[0-9A-Z]{16}\b|\bAIza[0-9A-Za-z_-]{35})|\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})|\bxox[abposr]-[A-Za-z0-9-]{10,}` +
	`|-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)

// diagnosticURLCredentials masks the userinfo part of URLs (scheme://user:secret@host).
var diagnosticURLCredentials = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@"'<>]+@`)

func sensitiveDiagnosticField(name string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(name))
	switch normalized {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "password", "apikey", "openaikey", "openaiapikey", "accesstoken", "refreshtoken", "idtoken", "token", "secret", "clientsecret", "credential", "credentials":
		return true
	}
	return false
}

func redactDiagnostic(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	if len(raw) > maxDiagnosticBody {
		return json.RawMessage(`{"omitted":"diagnostic size limit"}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{"omitted":"non-JSON diagnostic"}`)
	}
	nodes := 0
	var clean func(any, int) any
	clean = func(value any, depth int) any {
		nodes++
		if depth > 64 || nodes > 4096 {
			return "[omitted: diagnostic structure limit]"
		}
		switch v := value.(type) {
		case map[string]any:
			for name, item := range v {
				if sensitiveDiagnosticField(name) {
					v[name] = "[redacted]"
				} else {
					v[name] = clean(item, depth+1)
				}
			}
			return v
		case []any:
			for index, item := range v {
				v[index] = clean(item, depth+1)
			}
			return v
		case string:
			return diagnosticURLCredentials.ReplaceAllString(diagnosticTokens.ReplaceAllString(v, "[redacted]"), "${1}[redacted]@")
		default:
			return v
		}
	}
	value = clean(value, 0)
	result, err := json.Marshal(value)
	if err != nil || len(result) > maxDiagnosticBody {
		return json.RawMessage(`{"omitted":"diagnostic size limit"}`)
	}
	return result
}

func safeDiagnosticCode(code string) string {
	if code == "" || len(code) > 128 {
		return "upstream_error"
	}
	for _, character := range code {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return "upstream_error"
		}
	}
	return code
}
