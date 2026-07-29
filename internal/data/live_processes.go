package data

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

const maxProcesses = 500

func (l *Live) processes(ctx context.Context, query string) ([]Row, error) {
	api := datadogV2.NewProcessesApi(l.client)
	opts := datadogV2.NewListProcessesOptionalParameters().WithPageLimit(maxProcesses)
	q := strings.TrimSpace(query)
	// The API splits filtering: tag filters (key:value) go to tags, free
	// text goes to the command-line search.
	if q != "" && q != "*" {
		if strings.ContainsRune(q, ':') {
			opts = opts.WithTags(q)
		} else {
			opts = opts.WithSearch(q)
		}
	}
	resp, httpresp, err := api.ListProcesses(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("processes", err)
	}
	list := resp.GetData()
	rows := make([]Row, 0, len(list))
	for _, p := range list {
		attrs := p.GetAttributes()
		cmd := attrs.GetCmdline()
		if i := strings.IndexByte(cmd, '\n'); i >= 0 {
			cmd = cmd[:i]
		}
		rows = append(rows, Row{
			ID: p.GetId(),
			Cells: []string{
				cmd,
				attrs.GetUser(),
				attrs.GetHost(),
				strconv.FormatInt(attrs.GetPid(), 10),
				strconv.FormatInt(attrs.GetPpid(), 10),
				processAge(attrs.GetStart()),
				strings.Join(attrs.GetTags(), " "),
			},
			Raw: p,
			URL: l.web + "/process?query=" + url.QueryEscape(q),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cells[2] != rows[j].Cells[2] {
			return rows[i].Cells[2] < rows[j].Cells[2]
		}
		return rows[i].Cells[0] < rows[j].Cells[0]
	})
	return rows, nil
}

// processAge renders the process start time as an age ("3d", "45m"); the API
// returns it as an RFC3339 string.
func processAge(start string) string {
	t, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return start
	}
	return HumanWindow(time.Since(t).Truncate(time.Minute))
}
