// Package safety cleans untrusted output at terminal, artifact and provider boundaries.
package safety

import (
	"regexp"
	"strings"
	"unicode"
)

// Cleaner holds known credentials without placing them in diagnostic output.
type Cleaner struct{ secrets []string }

func New(secrets ...string) Cleaner { return Cleaner{secrets: append([]string(nil), secrets...)} }

// Clean removes terminal controls and credentials, including common unknown secret formats.
func (c Cleaner) Clean(s string) string {
	s = stripControls(s)
	for _, secret := range c.secrets {
		if len(secret) > 0 {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	private := regexp.MustCompile(`(?s)-----BEGIN [^\n]*PRIVATE KEY-----.*?-----END [^\n]*PRIVATE KEY-----`)
	s = private.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	token := regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{8,}|gh[pousr]_[a-z0-9_]{12,}|bearer\s+[a-z0-9._~+/-]{8,})`)
	s = token.ReplaceAllString(s, "[REDACTED]")
	urls := regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@:]+:[^\s/@]+@`)
	s = urls.ReplaceAllString(s, "${1}[REDACTED]@")
	auth := regexp.MustCompile(`(?im)(authorization\s*[:=]\s*)(?:basic|bearer|digest)\s+[^\r\n]+`)
	s = auth.ReplaceAllString(s, "${1}[REDACTED]")
	jwt := regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\b`)
	s = jwt.ReplaceAllString(s, "[REDACTED]")
	assign := regexp.MustCompile(`(?im)(["']?[a-z0-9_]*(?:password|passwd|secret|api[_-]?key|token)["']?\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,}\n]+)`)
	return assign.ReplaceAllString(s, "${1}[REDACTED]")
}

func stripControls(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[':
				i++
				for i < len(s) {
					ch := s[i]
					i++
					if ch >= 0x40 && ch <= 0x7e {
						break
					}
				}
			case ']', 'P', '^', '_':
				i++
				for i < len(s) {
					if s[i] == 7 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default:
				i++
			}
			continue
		}
		if s[i] == '\r' {
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, b.String())
}

// SafeTask conservatively rejects potentially private structured input for external classification.
func SafeTask(task string, c Cleaner) bool {
	if c.Clean(task) != task || len(task) > 12000 {
		return false
	}
	if regexp.MustCompile(`\d{6,}|(?:\+?\d[\d ()-]{7,}\d)|(?i)\b(?:customer|client|patient|card|passport|ssn|iban|account)\s*(?:id|number|record|name|:|#)`).MatchString(task) {
		return false
	}
	patterns := []string{"```", "-----BEGIN", "@", "SELECT ", "INSERT ", "Stack trace", "<?php", "func ", "function ", "{", "}", "=>"}
	for _, p := range patterns {
		if strings.Contains(strings.ToLower(task), strings.ToLower(p)) {
			return false
		}
	}
	for _, line := range strings.Split(task, "\n") {
		if strings.Contains(line, "=") || regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}`).MatchString(line) {
			return false
		}
	}
	return true
}
