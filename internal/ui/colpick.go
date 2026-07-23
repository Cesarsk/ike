package ui

import (
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// colItem is one row of the column picker: a view column and whether it shows.
type colItem struct {
	name  string
	shown bool
}

// columnsFor returns a resource view's full registry column list.
func columnsFor(key string) []string {
	for _, r := range data.Resources() {
		if r.Key == key {
			return r.Columns
		}
	}
	return nil
}

// openColumnPicker opens the interactive column editor for the current table
// view: 'space' toggles a column shown/hidden, J/K reorder, esc applies+saves.
// Columns already shown come first (in their configured order), hidden ones
// after — so the list reads top-to-bottom as the table will.
func (a *App) openColumnPicker() {
	full := columnsFor(a.res.Key)
	if len(full) == 0 {
		return
	}
	a.colPickView = a.res.Key
	a.colPickItems = a.colPickItems[:0]
	if want := a.wantColumns(); len(want) > 0 {
		inWant := map[string]bool{}
		for _, w := range want {
			for _, f := range full {
				if strings.EqualFold(f, w) && !inWant[f] {
					a.colPickItems = append(a.colPickItems, colItem{name: f, shown: true})
					inWant[f] = true
				}
			}
		}
		for _, f := range full {
			if !inWant[f] {
				a.colPickItems = append(a.colPickItems, colItem{name: f, shown: false})
			}
		}
	} else {
		for _, f := range full {
			a.colPickItems = append(a.colPickItems, colItem{name: f, shown: true})
		}
	}
	a.pushNav()
	a.renderColumnPicker()
	a.colPick.SetCurrentItem(0)
	a.showPage("colpick")
}

func (a *App) renderColumnPicker() {
	cur := a.colPick.GetCurrentItem()
	a.colPick.Clear()
	for _, it := range a.colPickItems {
		mark := "[ ]"
		if it.shown {
			mark = "[x]"
		}
		// Escape the brackets: tview.List parses "[x]" as a colour tag and
		// would otherwise swallow the marker.
		a.colPick.AddItem(tview.Escape(mark+" "+it.name), "", 0, nil)
	}
	if cur >= 0 && cur < len(a.colPickItems) {
		a.colPick.SetCurrentItem(cur)
	}
	a.colPick.SetTitle(" Columns · " + a.colPickView + "  <space> show/hide · <J/K> move · <esc> done ")
}

// toggleColumn flips the highlighted column, keeping at least one visible.
func (a *App) toggleColumn() {
	i := a.colPick.GetCurrentItem()
	if i < 0 || i >= len(a.colPickItems) {
		return
	}
	if a.colPickItems[i].shown && a.shownColumns() == 1 {
		a.flash("at least one column must stay visible", true)
		return
	}
	a.colPickItems[i].shown = !a.colPickItems[i].shown
	a.renderColumnPicker()
	a.applyColPick()
}

func (a *App) shownColumns() int {
	n := 0
	for _, it := range a.colPickItems {
		if it.shown {
			n++
		}
	}
	return n
}

// moveColumn shifts the highlighted column by dir (-1 up, +1 down), following
// it with the selection.
func (a *App) moveColumn(dir int) {
	i := a.colPick.GetCurrentItem()
	j := i + dir
	if i < 0 || i >= len(a.colPickItems) || j < 0 || j >= len(a.colPickItems) {
		return
	}
	a.colPickItems[i], a.colPickItems[j] = a.colPickItems[j], a.colPickItems[i]
	a.renderColumnPicker()
	a.colPick.SetCurrentItem(j)
	a.applyColPick()
}

// applyColPick writes the picker state to the live column config. A full,
// registry-ordered selection clears the override (back to "all").
func (a *App) applyColPick() {
	var shown []string
	for _, it := range a.colPickItems {
		if it.shown {
			shown = append(shown, it.name)
		}
	}
	if sameStrings(shown, columnsFor(a.colPickView)) {
		delete(a.opts.Columns, a.colPickView)
	} else {
		if a.opts.Columns == nil {
			a.opts.Columns = map[string][]string{}
		}
		a.opts.Columns[a.colPickView] = shown
	}
	a.persistSettings() // save on every change, so navigating away never loses it
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
