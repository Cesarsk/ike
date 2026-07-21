package data

import (
	"context"
	"sort"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// maxCostMonths caps the history range; the Datadog historical-cost endpoint
// serves at most 15 months back.
const maxCostMonths = 15

// Cost reports this org's Datadog spend: the current month's estimated-so-far
// (GetEstimatedCostByOrg) plus the projected end-of-month total
// (GetProjectedCost), and — when o.Months > 1 — closed months via
// GetHistoricalCostByOrg. At most three bounded calls, no polling; the
// figures move at most daily. o.SubOrgs switches the API's "summary" view to
// "sub-org", which breaks every line down per child org. The endpoints are
// admin/usage-scoped, so a non-privileged key or OAuth user gets a permission
// error here — surfaced to the user, not swallowed.
func (l *Live) Cost(ctx context.Context, o CostOptions) (*CostView, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := datadogV2.NewUsageMeteringApi(l.client)

	months := o.Months
	if months < 1 {
		months = 1
	}
	if months > maxCostMonths {
		months = maxCostMonths
	}
	view := "summary"
	if o.SubOrgs {
		view = "sub-org"
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	// Estimated cost accrued this month, by product.
	est, httpresp, err := api.GetEstimatedCostByOrg(ctx,
		*datadogV2.NewGetEstimatedCostByOrgOptionalParameters().
			WithView(view).WithStartMonth(monthStart))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("cost (estimated)", err)
	}

	v := &CostView{Currency: "USD"}
	cur := &CostMonth{Month: now.Format("2006-01"), Current: true}
	lines := map[string]*CostLine{}
	for _, org := range est.GetData() {
		a := org.GetAttributes()
		if v.OrgName == "" || !o.SubOrgs {
			v.OrgName = a.GetOrgName()
		}
		cur.Total += a.GetTotalCost()
		addCostCharges(lines, a.GetOrgName(), a.GetCharges(), o.SubOrgs, false)
	}

	// Projected end-of-month total. A failure here isn't fatal — show the
	// estimated figures without projection rather than nothing.
	proj, httpresp, err := api.GetProjectedCost(ctx,
		*datadogV2.NewGetProjectedCostOptionalParameters().WithView(view))
	l.track(httpresp)
	if err == nil {
		for _, org := range proj.GetData() {
			a := org.GetAttributes()
			cur.Projected += a.GetProjectedTotalCost()
			addCostCharges(lines, a.GetOrgName(), a.GetCharges(), o.SubOrgs, true)
		}
	}
	cur.Lines = sortedCostLines(lines)
	v.Months = append(v.Months, *cur)

	// Closed months, one bucket per calendar month.
	if months > 1 {
		histStart := monthStart.AddDate(0, -(months - 1), 0)
		histEnd := monthStart.AddDate(0, -1, 0)
		hist, httpresp, err := api.GetHistoricalCostByOrg(ctx, histStart,
			*datadogV2.NewGetHistoricalCostByOrgOptionalParameters().
				WithEndMonth(histEnd).WithView(view))
		l.track(httpresp)
		if err != nil {
			return nil, apiErr("cost (historical)", err)
		}
		buckets := map[string]*CostMonth{}
		bucketLines := map[string]map[string]*CostLine{}
		for _, org := range hist.GetData() {
			a := org.GetAttributes()
			key := a.GetDate().UTC().Format("2006-01")
			m, ok := buckets[key]
			if !ok {
				m = &CostMonth{Month: key}
				buckets[key] = m
				bucketLines[key] = map[string]*CostLine{}
			}
			m.Total += a.GetTotalCost()
			addCostCharges(bucketLines[key], a.GetOrgName(), a.GetCharges(), o.SubOrgs, false)
		}
		for key, m := range buckets {
			m.Lines = sortedCostLines(bucketLines[key])
			v.Months = append(v.Months, *m)
		}
	}

	sort.Slice(v.Months, func(i, j int) bool { return v.Months[i].Month > v.Months[j].Month })
	return v, nil
}

// addCostCharges folds one org's "total" charge lines into the accumulator,
// skipping committed/on_demand rows that would double-count. The org name is
// recorded on the line only in sub-org view.
func addCostCharges(lines map[string]*CostLine, orgName string, charges []datadogV2.ChargebackBreakdown, subOrgs, projected bool) {
	if !subOrgs {
		orgName = ""
	}
	for _, c := range charges {
		if c.GetChargeType() != "total" {
			continue
		}
		line := costLineFor(lines, orgName, c.GetProductName())
		if projected {
			line.Projected += c.GetCost()
		} else {
			line.Total += c.GetCost()
		}
	}
}

// costLineFor returns the accumulator for an org+product pair, creating it on
// first sight.
func costLineFor(lines map[string]*CostLine, org, product string) *CostLine {
	if product == "" {
		product = "other"
	}
	key := org + "\x00" + product
	if l, ok := lines[key]; ok {
		return l
	}
	l := &CostLine{Org: org, Product: product}
	lines[key] = l
	return l
}

// sortedCostLines flattens the accumulator map, highest cost first.
func sortedCostLines(lines map[string]*CostLine) []CostLine {
	out := make([]CostLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		if out[i].Projected != out[j].Projected {
			return out[i].Projected > out[j].Projected
		}
		return out[i].Product < out[j].Product
	})
	return out
}
