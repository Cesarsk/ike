package data

import (
	"regexp"
	"sort"
	"strings"
)

// LogPattern is a cluster of log messages sharing a normalized template.
type LogPattern struct {
	Template string // message with variable tokens replaced by placeholders
	Count    int
	Example  string // a representative raw message
}

var (
	reIP    = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	reUUID  = regexp.MustCompile(`\b[0-9a-fA-F-]{16,}\b`)
	reHex   = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`) // ids, hashes
	reQuote = regexp.MustCompile(`"[^"]*"`)
	// A number NOT glued to the front by a letter (so "123ms" → "<n>ms" but
	// an identifier like "v1" is left alone). The leading non-alnum char (or
	// start) is preserved via the capture group.
	reNum = regexp.MustCompile(`(^|[^A-Za-z0-9])\d+(?:\.\d+)?`)
	reWS  = regexp.MustCompile(`\s+`)
)

// normalizeLog collapses a log message to a pattern template by replacing
// the parts that vary between otherwise-identical lines (ids, numbers, ips,
// quoted values) with placeholders. Order matters: ip/uuid/hex before plain
// numbers.
func normalizeLog(msg string) string {
	s := reIP.ReplaceAllString(msg, "<ip>")
	s = reUUID.ReplaceAllString(s, "<id>")
	s = reHex.ReplaceAllString(s, "<id>")
	s = reQuote.ReplaceAllString(s, "<str>")
	s = reNum.ReplaceAllString(s, "${1}<n>")
	return strings.TrimSpace(reWS.ReplaceAllString(s, " "))
}

// ClusterLogs groups messages by normalized template, most frequent first.
// It operates on whatever messages are passed (ike feeds the loaded rows),
// so it's a zero-API triage aid over the current sample, not a server-side
// pattern computation over the full volume.
func ClusterLogs(messages []string) []LogPattern {
	idx := map[string]int{}
	var out []LogPattern
	for _, m := range messages {
		if strings.TrimSpace(m) == "" {
			continue
		}
		tmpl := normalizeLog(m)
		if i, ok := idx[tmpl]; ok {
			out[i].Count++
			continue
		}
		idx[tmpl] = len(out)
		out = append(out, LogPattern{Template: tmpl, Count: 1, Example: m})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
