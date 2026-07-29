package data

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
)

// maxHosts caps the host list; ListHosts serves at most 1000 per page and a
// TUI list beyond that isn't glanceable anyway.
const maxHosts = 1000

// hosts lists reporting infrastructure hosts, problems first: down before
// muted before up, then by name. Read-only; m mutes/unmutes a host.
// hostCluster derives a host's cluster from its tags. Datadog's Host object
// has no first-class cluster field — the web UI's "Cluster Name" facet is
// built from these same tags, checked in specificity order.
func hostCluster(tags []string) string {
	prefixes := []string{"kube_cluster_name:", "cluster_name:", "eks_cluster-name:", "ecs_cluster_name:"}
	for _, p := range prefixes {
		for _, t := range tags {
			if v, ok := strings.CutPrefix(strings.TrimSpace(t), p); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

func (l *Live) hosts(ctx context.Context) ([]Row, error) {
	resp, httpresp, err := datadogV1.NewHostsApi(l.client).ListHosts(ctx,
		*datadogV1.NewListHostsOptionalParameters().WithCount(maxHosts).WithIncludeMutedHostsData(true))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("hosts", err)
	}
	list := resp.GetHostList()
	rows := make([]Row, 0, len(list))
	for _, h := range list {
		name := h.GetName()
		if hn := h.GetHostName(); hn != "" {
			name = hn
		}
		up, muted := h.GetUp(), h.GetIsMuted()
		tags := hostTags(h.GetTagsBySource())
		meta := h.GetMeta()
		rows = append(rows, Row{
			ID: name,
			Cells: []string{
				name,
				hostStatus(up, muted),
				hostCluster(tags),
				h.GetName(),
				h.GetAwsName(),
				strings.Join(h.GetAliases(), ","),
				strings.Join(h.GetSources(), ","),
				meta.GetPlatform(),
				strings.Join(h.GetApps(), ","),
				hostCPU(h.GetMetrics()),
				hostLastReported(int64(h.GetLastReportedTime())),
				strings.Join(tags, " "),
			},
			Raw: map[string]any{"muted": muted, "up": up},
			URL: l.web + "/infrastructure?host=" + name,
		})
	}
	// Problems first: down (0) < muted (1) < up (2), then name.
	rank := func(r Row) int {
		switch r.Cells[1] {
		case "down":
			return 0
		case "muted":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := rank(rows[i]), rank(rows[j]); ri != rj {
			return ri < rj
		}
		return rows[i].Cells[0] < rows[j].Cells[0]
	})
	return rows, nil
}

// SetHostMute mutes (indefinitely) or unmutes a host.
func (l *Live) SetHostMute(ctx context.Context, host string, mute bool) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := datadogV1.NewHostsApi(l.client)
	if mute {
		_, httpresp, err := api.MuteHost(ctx, host, datadogV1.HostMuteSettings{})
		l.track(httpresp)
		return apiErr("mute host", err)
	}
	_, httpresp, err := api.UnmuteHost(ctx, host)
	l.track(httpresp)
	return apiErr("unmute host", err)
}

// hostStatus folds up/muted into one word; down wins over muted so a muted-but-
// down host still reads as a problem.
func hostStatus(up, muted bool) string {
	switch {
	case !up:
		return "down"
	case muted:
		return "muted"
	default:
		return "up"
	}
}

// hostCPU renders the host's CPU metric as a percent, blank when absent.
func hostCPU(m datadogV1.HostMetrics) string {
	if !m.HasCpu() {
		return ""
	}
	return fmt.Sprintf("%.0f%%", m.GetCpu())
}

// hostLastReported turns the epoch-seconds last-report into a short age.
func hostLastReported(epoch int64) string {
	if epoch <= 0 {
		return ""
	}
	d := time.Since(time.Unix(epoch, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// hostTags flattens the by-source tag map into a de-duplicated, sorted list.
func hostTags(bySource map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tags := range bySource {
		for _, t := range tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}
