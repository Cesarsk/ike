package data

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV1"
)

// notebooks lists the org's notebooks (name, author, status, last modified).
// enter drills into one and renders its cells. Read-only.
func (l *Live) notebooks(ctx context.Context) ([]Row, error) {
	resp, httpresp, err := datadogV1.NewNotebooksApi(l.client).ListNotebooks(ctx,
		*datadogV1.NewListNotebooksOptionalParameters().WithCount(100).WithSortField("modified").WithSortDir("desc"))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("notebooks", err)
	}
	data := resp.GetData()
	rows := make([]Row, 0, len(data))
	for _, nb := range data {
		a := nb.GetAttributes()
		author := a.GetAuthor()
		id := strconv.FormatInt(nb.GetId(), 10)
		rows = append(rows, Row{
			ID: id,
			Cells: []string{
				a.GetName(), notebookAuthorName(author), string(a.GetStatus()),
				a.GetModified().Local().Format("2006-01-02 15:04"),
			},
			URL: l.web + "/notebook/" + id,
		})
	}
	return rows, nil
}

// Notebook fetches one notebook and assembles its cells into a readable body:
// markdown verbatim, other cell kinds as a short placeholder.
func (l *Live) Notebook(ctx context.Context, id string) (*NotebookView, error) {
	nid, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("notebook: invalid id %q", id)
	}
	ctx = l.authCtx(ctx)
	resp, httpresp, err := datadogV1.NewNotebooksApi(l.client).GetNotebook(ctx, nid)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("notebook", err)
	}
	nbData := resp.GetData()
	a := nbData.GetAttributes()
	v := &NotebookView{
		Name:   a.GetName(),
		Author: notebookAuthorName(a.GetAuthor()),
		Status: string(a.GetStatus()),
		URL:    l.web + "/notebook/" + id,
	}
	var b strings.Builder
	for _, cell := range a.GetCells() {
		ca := cell.GetAttributes()
		switch {
		case ca.NotebookMarkdownCellAttributes != nil:
			def := ca.NotebookMarkdownCellAttributes.GetDefinition()
			b.WriteString(def.GetText())
			b.WriteString("\n\n")
		case ca.NotebookTimeseriesCellAttributes != nil:
			b.WriteString("[timeseries chart]\n\n")
		case ca.NotebookToplistCellAttributes != nil:
			b.WriteString("[toplist]\n\n")
		case ca.NotebookHeatMapCellAttributes != nil:
			b.WriteString("[heatmap]\n\n")
		case ca.NotebookDistributionCellAttributes != nil:
			b.WriteString("[distribution]\n\n")
		case ca.NotebookLogStreamCellAttributes != nil:
			b.WriteString("[log stream]\n\n")
		}
	}
	v.Body = strings.TrimRight(b.String(), "\n")
	return v, nil
}

// notebookAuthorName prefers the author's name, then handle, then email.
func notebookAuthorName(a datadogV1.NotebookAuthor) string {
	switch {
	case a.GetName() != "":
		return a.GetName()
	case a.GetHandle() != "":
		return a.GetHandle()
	default:
		return a.GetEmail()
	}
}
