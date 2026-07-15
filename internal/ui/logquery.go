package ui

import (
	"sort"
	"strings"
)

// logFacetKeys are the common Datadog log search attributes offered as
// completions regardless of what's currently loaded.
var logFacetKeys = []string{
	"service:", "host:", "status:", "source:", "env:", "version:",
	"@http.status_code:", "@http.method:", "@http.url_details.path:",
	"@duration:", "@error.kind:", "@error.message:",
}

// logOperators are search operators offered when composing a fresh token.
var logOperators = []string{"AND", "OR", "NOT"}

// logColumnFacet maps a loaded Logs row column to the facet key whose values
// it carries, so completions can be harvested from the current result set —
// zero extra API calls, and more relevant than a global facet dump.
var logColumnFacet = map[string]int{"status": 1, "service": 2, "host": 3}

// logQueryCompletions returns full-field completion candidates for a Datadog
// log search query. It completes the LAST whitespace-delimited token and
// preserves the prefix, so "status:error serv" → "status:error service:…".
// Facet *values* come only from rows already loaded (see logColumnFacet);
// it never calls the API.
func (a *App) logQueryCompletions(field string) []string {
	// Split into everything-before-last-token (kept verbatim) and the token
	// currently being typed.
	lastSpace := strings.LastIndexAny(field, " \t")
	prefix, token := "", field
	if lastSpace >= 0 {
		prefix, token = field[:lastSpace+1], field[lastSpace+1:]
	}
	if token == "" {
		return nil
	}
	lower := strings.ToLower(token)

	var cands []string
	if key, valPrefix, ok := strings.Cut(token, ":"); ok {
		// value completion: key:valuePrefix → values seen for that key
		for _, v := range a.facetValues(strings.ToLower(key)) {
			if strings.HasPrefix(strings.ToLower(v), strings.ToLower(valPrefix)) {
				cands = append(cands, key+":"+v)
			}
		}
	} else {
		// key/operator completion on a bare token
		for _, k := range logFacetKeys {
			if strings.HasPrefix(strings.ToLower(k), lower) {
				cands = append(cands, k)
			}
		}
		for _, op := range logOperators {
			if token != lower && strings.HasPrefix(op, token) { // only when typing uppercase
				cands = append(cands, op)
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Strings(cands)
	// tview replaces the whole field, so prepend the preserved prefix. Drop
	// any candidate identical to the current field: an exact match means the
	// token is already complete, and leaving the dropdown open would make
	// Enter accept-the-completion instead of submitting the query.
	var out []string
	for _, c := range cands {
		full := prefix + c
		if full != field {
			out = append(out, full)
		}
	}
	return out
}

// facetValues returns the distinct values for a facet key harvested from the
// currently loaded Logs rows (empty for keys with no mapped column).
func (a *App) facetValues(key string) []string {
	col, ok := logColumnFacet[key]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, r := range a.rows {
		if col < len(r.Cells) && r.Cells[col] != "" {
			set[r.Cells[col]] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
