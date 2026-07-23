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
// the interesting ones), then by name. Read-only.
func (l *Live) containers(ctx context.Context) ([]Row, error) {
	resp, httpresp, err := datadogV2.NewContainersApi(l.client).ListContainers(ctx,
		*datadogV2.NewListContainersOptionalParameters().WithPageSize(maxContainers))
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
		rows = append(rows, Row{
			ID: a.GetName(),
			Cells: []string{
				a.GetName(),
				a.GetState(),
				image,
				a.GetHost(),
				containerAge(a.GetStartedAt()),
				strings.Join(a.GetTags(), " "),
			},
			Raw: c,
			URL: l.web + "/containers?text=" + a.GetName(),
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
