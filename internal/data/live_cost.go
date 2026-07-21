package data

import (
	"context"
	"sort"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// Cost reports this org's current-month Datadog spend: estimated-so-far
// (GetEstimatedCostByOrg) plus the projected end-of-month total
// (GetProjectedCost), merged into a per-product breakdown. Two bounded calls,
// no polling; the caching layer gives it a long TTL since the figures move
// at most daily. The endpoints are admin/usage-scoped, so a non-privileged
// key or OAuth user gets a permission error here — surfaced to the user, not
// swallowed.
func (l *Live) Cost(ctx context.Context) (*CostView, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := datadogV2.NewUsageMeteringApi(l.client)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	// Estimated cost accrued this month, by product.
	est, httpresp, err := api.GetEstimatedCostByOrg(ctx,
		*datadogV2.NewGetEstimatedCostByOrgOptionalParameters().
			WithView("summary").WithStartMonth(monthStart))
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("cost (estimated)", err)
	}

	v := &CostView{Month: now.Format("2006-01"), Currency: "USD"}
	lines := map[string]*CostLine{}
	for _, org := range est.GetData() {
		a := org.GetAttributes()
		if v.OrgName == "" {
			v.OrgName = a.GetOrgName()
		}
		v.Estimated += a.GetTotalCost()
		for _, c := range a.GetCharges() {
			if c.GetChargeType() != "total" { // avoid double-counting committed/on_demand
				continue
			}
			line := lineFor(lines, c.GetProductName())
			line.Estimated += c.GetCost()
		}
	}

	// Projected end-of-month total. A failure here isn't fatal — show the
	// estimated figures without projection rather than nothing.
	proj, httpresp, err := api.GetProjectedCost(ctx,
		*datadogV2.NewGetProjectedCostOptionalParameters().WithView("summary"))
	l.track(httpresp)
	if err == nil {
		for _, org := range proj.GetData() {
			a := org.GetAttributes()
			v.Projected += a.GetProjectedTotalCost()
			for _, c := range a.GetCharges() {
				if c.GetChargeType() != "total" {
					continue
				}
				lineFor(lines, c.GetProductName()).Projected += c.GetCost()
			}
		}
	}

	v.Lines = make([]CostLine, 0, len(lines))
	for _, line := range lines {
		v.Lines = append(v.Lines, *line)
	}
	sort.Slice(v.Lines, func(i, j int) bool {
		if v.Lines[i].Estimated != v.Lines[j].Estimated {
			return v.Lines[i].Estimated > v.Lines[j].Estimated
		}
		return v.Lines[i].Projected > v.Lines[j].Projected
	})
	return v, nil
}

// lineFor returns the accumulator for a product, creating it on first sight.
func lineFor(lines map[string]*CostLine, product string) *CostLine {
	if product == "" {
		product = "other"
	}
	if l, ok := lines[product]; ok {
		return l
	}
	l := &CostLine{Product: product}
	lines[product] = l
	return l
}
