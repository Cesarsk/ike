package ui

import (
	"sort"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// The command palette is ike's take on ':' — instead of k9s's bottom prompt,
// a centered Spotlight-style overlay: type to fuzzy-filter every command
// (name, aliases, description), ↑/↓ to pick, enter to run, esc to cancel.
// Muscle memory survives: ':mon<enter>' behaves exactly like the old prompt,
// because the typed text's best match runs on enter — and unmatched input
// falls through to execCommand verbatim.

// paletteItem is one runnable entry: what's shown, what's searched, what runs.
type paletteItem struct {
	label  string // ":monitors — Monitors"
	search string // name + aliases + description, the fuzzy haystack
	run    string // the command execCommand receives
}

// paletteItems builds the catalog: every registered resource plus the
// pseudo-commands — the same source as :menu, so it can never drift.
func paletteItems() []paletteItem {
	var items []paletteItem
	for _, r := range data.Resources() {
		items = append(items, paletteItem{
			label:  ":" + r.Key + "  [gray]" + r.Title + resourceHint(r.Key) + "[-]",
			search: r.Key + " " + strings.Join(r.Aliases, " ") + " " + r.Title,
			run:    r.Key,
		})
	}
	for _, c := range pseudoCommands {
		items = append(items, paletteItem{
			label:  c.name + "  [gray]" + c.opens + "[-]",
			search: c.name + " " + c.aliases + " " + c.opens,
			run:    c.run,
		})
	}
	return items
}

// openPalette shows the palette over the current page, input cleared.
func (a *App) openPalette() {
	if a.page == "palette" {
		return
	}
	a.paletteReturn = a.page
	a.paletteInput.SetText("")
	a.showPage("palette")
	a.renderPalette()
}

// renderPalette ranks the catalog against the query. Empty query = the full
// catalog in registry order (a browsable menu, not a blank box).
func (a *App) renderPalette() {
	const maxHits = 12
	query := a.paletteInput.GetText()
	type hit struct {
		item  paletteItem
		score int
	}
	var hits []hit
	for _, it := range paletteItems() {
		if score, ok := fuzzyMatch(strings.TrimPrefix(query, ":"), it.search); ok {
			hits = append(hits, hit{it, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score < hits[j].score })
	if len(hits) > maxHits {
		hits = hits[:maxHits]
	}
	a.paletteList.Clear()
	a.paletteRuns = a.paletteRuns[:0]
	for _, h := range hits {
		a.paletteList.AddItem(h.item.label, "", 0, nil)
		a.paletteRuns = append(a.paletteRuns, h.item.run)
	}
	if len(hits) == 0 {
		a.paletteList.AddItem(tview.Escape("(no matching command — enter runs the text as typed)"), "", 0, nil)
	}
	a.paletteList.SetCurrentItem(0)
}

// paletteMove shifts the highlighted command (the input keeps focus).
func (a *App) paletteMove(delta int) {
	n := a.paletteList.GetItemCount()
	if n == 0 {
		return
	}
	i := a.paletteList.GetCurrentItem() + delta
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	a.paletteList.SetCurrentItem(i)
}

// closePalette dismisses the overlay; on run it executes the highlighted
// command, or the raw text when nothing matched (so ':q' and friends keep
// working exactly as the old prompt did).
func (a *App) closePalette(run bool) {
	query := strings.TrimSpace(a.paletteInput.GetText())
	i := a.paletteList.GetCurrentItem()
	ret := a.paletteReturn
	if ret == "" {
		ret = "table"
	}
	a.showPage(ret)
	if !run {
		return
	}
	if i >= 0 && i < len(a.paletteRuns) {
		a.execCommand(a.paletteRuns[i])
		return
	}
	if query != "" {
		a.execCommand(query)
	}
}
