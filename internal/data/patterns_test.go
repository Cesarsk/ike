package data

import "testing"

func TestClusterLogs(t *testing.T) {
	msgs := []string{
		"upstream timeout status=504 upstream=payments-api attempt=2",
		"upstream timeout status=504 upstream=payments-api attempt=7",
		"upstream timeout status=500 upstream=payments-api attempt=1",
		`reconciliation finished app=platform-workloads revision=f3a9c1b2`,
		`reconciliation finished app=platform-workloads revision=99aa88bb`,
		"healthcheck ok",
	}
	pats := ClusterLogs(msgs)
	// The three "upstream timeout … status=<n> … attempt=<n>" lines collapse
	// to one template and must rank first (count 3).
	if len(pats) == 0 || pats[0].Count != 3 {
		t.Fatalf("expected top pattern count 3, got %+v", pats)
	}
	if got := len(pats); got != 3 { // timeout×3, reconciliation×2, healthcheck×1
		t.Errorf("expected 3 clusters, got %d: %+v", got, pats)
	}
	// The two reconciliation lines (differing only by hex revision) collapse.
	var recon *LogPattern
	for i := range pats {
		if pats[i].Count == 2 {
			recon = &pats[i]
		}
	}
	if recon == nil {
		t.Fatalf("reconciliation lines did not cluster: %+v", pats)
	}
}

func TestNormalizeLog(t *testing.T) {
	cases := map[string]string{
		"latency=123ms path=/v1/x":       "latency=<n>ms path=/v1/x",
		"host 10.1.2.11 down":            "host <ip> down",
		"lease id deadbeefcafe expired":  "lease id <id> expired",
		`error "connection refused" x=5`: "error <str> x=<n>",
	}
	for in, want := range cases {
		if got := normalizeLog(in); got != want {
			t.Errorf("normalizeLog(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorIsRateLimit(t *testing.T) {
	if !ErrorIsRateLimit(apiErr("logs", errString("429 Too Many Requests"))) {
		t.Error("429 error should be detected as rate limit")
	}
	if ErrorIsRateLimit(apiErr("logs", errString("500 Internal Server Error"))) {
		t.Error("500 should not be a rate limit")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
