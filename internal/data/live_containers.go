package data

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// maxContainers caps the container list at one page; a TUI list beyond this
// isn't glanceable and the fleet can be large.
const maxContainers = 1000

// containers lists live containers, non-running first (stopped/terminated are
// the interesting ones), then by name. The query is a Datadog tag filter
// ('/', e.g. kube_namespace:payments). Read-only.
func (l *Live) containers(ctx context.Context, query string) ([]Row, error) {
	opts := datadogV2.NewListContainersOptionalParameters().WithPageSize(maxContainers)
	if q := strings.TrimSpace(query); q != "" {
		opts = opts.WithFilterTags(q)
	}
	resp, httpresp, err := datadogV2.NewContainersApi(l.client).ListContainers(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("containers", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, item := range data {
		c := item.Container // ungrouped list items are plain containers
		if c == nil {
			continue
		}
		a := c.GetAttributes()
		image := a.GetImageName()
		if tags := a.GetImageTags(); len(tags) > 0 {
			image += ":" + tags[0]
		}
		tags := a.GetTags()
		rows = append(rows, Row{
			ID: a.GetName(),
			// Order must match the resource's Columns:
			// NAME STATE IMAGE NAMESPACE CLUSTER HOST STARTED TAGS.
			Cells: []string{
				a.GetName(),
				a.GetState(),
				image,
				tagValue(tags, "kube_namespace", "namespace"),
				tagValue(tags, "kube_cluster_name", "cluster_name", "cluster"),
				a.GetHost(),
				containerAge(a.GetStartedAt()),
				strings.Join(tags, " "),
			},
			Raw:      c,
			URL:      l.web + "/containers?text=" + a.GetName(),
			LogQuery: "container_name:" + a.GetName(), // l → this container's logs
		})
	}
	// Non-running first (running == 2, everything else == 1), then name.
	rank := func(state string) int {
		if strings.EqualFold(state, "running") {
			return 2
		}
		return 1
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := rank(rows[i].Cells[1]), rank(rows[j].Cells[1]); ri != rj {
			return ri < rj
		}
		return rows[i].Cells[0] < rows[j].Cells[0]
	})
	return rows, nil
}

// tagValue returns the value of the first "key:value" tag whose key matches any
// of keys (in preference order), or "" if none is present.
func tagValue(tags []string, keys ...string) string {
	for _, k := range keys {
		pre := k + ":"
		for _, t := range tags {
			if strings.HasPrefix(t, pre) {
				return strings.TrimPrefix(t, pre)
			}
		}
	}
	return ""
}

// containerAge turns an RFC3339 started-at into a short age; unparseable values
// pass through truncated.
func containerAge(started string) string {
	if started == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, started)
	if err != nil {
		if len(started) > 19 {
			return started[:19]
		}
		return started
	}
	d := time.Since(t)
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
