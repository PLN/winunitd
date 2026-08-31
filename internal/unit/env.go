package unit

import (
	"fmt"
	"strings"
	"unicode"
)

func parseEnvironment(s string) ([]EnvVar, error) {
	tokens, err := splitEnvTokens(s)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	out := make([]EnvVar, 0, len(tokens))
	for _, tok := range tokens {
		name, value, ok := strings.Cut(tok, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid Environment assignment %q", tok)
		}
		out = append(out, EnvVar{Name: name, Value: value})
	}
	return out, nil
}

func splitEnvTokens(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	quote := rune(0)
	started := false
	for _, r := range s {
		switch {
		case (r == '"' || r == '\'') && !inQuote:
			inQuote = true
			quote = r
			started = true
		case inQuote && r == quote:
			inQuote = false
			quote = 0
		case unicode.IsSpace(r) && !inQuote:
			if started {
				tokens = append(tokens, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in Environment")
	}
	if started {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}
