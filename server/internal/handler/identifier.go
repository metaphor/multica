package handler

import (
	"regexp"
	"strings"
)

// identifierRe extracts "PREFIX-NUMBER" identifiers from text (title, body,
// branch name). The prefix is 2..10 lowercase letters/digits, optionally
// uppercase. Word boundary on the left prevents matching inside email-style
// strings (e.g. "abc@MUL-1") and the digit anchor on the right rules out
// version numbers like "v1.2-3".
var identifierRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{1,9})-(\d+)\b`)

// closingIdentifierRe extracts identifiers that appear immediately after a
// GitHub-style closing keyword ("close[sd]?", "fix(e[sd])?", "resolve[sd]?"),
// optionally separated by a colon and whitespace. Matching is intentionally
// strict on adjacency — "Fix MUL-1" closes MUL-1, but "Fix login MUL-1"
// does not. This mirrors GitHub's own closing-keyword grammar and is the
// gate the webhook uses to decide whether to auto-advance an issue to
// `done` after a PR merges. References like "Follow up in MUL-2" and bare
// title prefixes like "MUL-1: ..." link the PR (via identifierRe) but
// never auto-close.
var closingIdentifierRe = regexp.MustCompile(
	`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[:\s]+([a-z][a-z0-9]{1,9})-(\d+)\b`,
)

// extractIdentifiers pulls every "PREFIX-NUMBER" match across the supplied
// fields, deduplicating in input order.
func extractIdentifiers(parts ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, src := range parts {
		for _, m := range identifierRe.FindAllStringSubmatch(src, -1) {
			ident := strings.ToUpper(m[1]) + "-" + m[2]
			if _, dup := seen[ident]; dup {
				continue
			}
			seen[ident] = struct{}{}
			out = append(out, ident)
		}
	}
	return out
}

// extractClosingIdentifiers pulls every "PREFIX-NUMBER" identifier that
// appears immediately after a GitHub-style closing keyword in the supplied
// fields, deduplicating in input order. Identifiers in branch names are
// intentionally excluded — callers should pass only title and body — because
// branch names are not natural-language fields and treating "mul-1/fix-login"
// as a close declaration would silently re-open the bug this gate is meant
// to fix.
func extractClosingIdentifiers(parts ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, src := range parts {
		for _, m := range closingIdentifierRe.FindAllStringSubmatch(src, -1) {
			ident := strings.ToUpper(m[1]) + "-" + m[2]
			if _, dup := seen[ident]; dup {
				continue
			}
			seen[ident] = struct{}{}
			out = append(out, ident)
		}
	}
	return out
}
