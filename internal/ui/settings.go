package ui

import (
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// settingKind is the editor behaviour for a :settings row.
type settingKind int

const (
	setTheme   settingKind = iota // enter cycles through the built-in themes
	setTTL                        // enter prompts for a per-view cache TTL
	setColumns                    // enter prompts for a per-view column list
)

type settingRow struct {
	kind  settingKind
	key   string // resource key (setTTL/setColumns); "" for setTheme
	label string
}

// showSettings opens the :settings editor — a table of settings each edited in
// place (theme cycles on enter; TTL/columns open a prompt). Every change
// applies live and persists to the config file. Saved queries have their own
// per-view picker ('Q'), so they're intentionally not duplicated here.
func (a *App) showSettings() {
	a.settingRows = a.settingRows[:0]
	a.settingRows = append(a.settingRows, settingRow{kind: setTheme, label: "Theme"})
	for _, r := range data.Resources() {
		a.settingRows = append(a.settingRows, settingRow{kind: setTTL, key: r.Key, label: "TTL · " + r.Key})
	}
	for _, r := range data.Resources() {
		a.settingRows = append(a.settingRows, settingRow{kind: setColumns, key: r.Key, label: "Columns · " + r.Key})
	}
	a.pushNav()
	a.renderSettings()
	a.settingsTbl.Select(1, 0) // land on the first setting (row 0 is the header)
	a.showPage("settings")
}

// settingValue is the display string for a row (with a "(default …)"/"(all)"
// hint when no override is set).
func (a *App) settingValue(s settingRow) string {
	switch s.kind {
	case setTheme:
		return a.theme.Name + "  (" + strings.Join(ThemeNames, " ") + ")"
	case setTTL:
		if d, ok := a.opts.TTLOverrides[s.key]; ok {
			return d.String()
		}
		return "(default " + defaultTTL(s.key).String() + ")"
	case setColumns:
		if c, ok := a.opts.Columns[s.key]; ok && len(c) > 0 {
			return strings.Join(c, ",")
		}
		return "(all)"
	}
	return ""
}

// settingRawValue is the editable value used to prefill the prompt (empty when
// no override is set, so a blank submit clears back to the default).
func (a *App) settingRawValue(s settingRow) string {
	switch s.kind {
	case setTTL:
		if d, ok := a.opts.TTLOverrides[s.key]; ok {
			return d.String()
		}
	case setColumns:
		if c, ok := a.opts.Columns[s.key]; ok {
			return strings.Join(c, ",")
		}
	}
	return ""
}

func (a *App) renderSettings() {
	sel, _ := a.settingsTbl.GetSelection() // survive the Clear so repeat edits work
	a.settingsTbl.Clear()
	for c, h := range []string{"SETTING", "VALUE", "EDIT"} {
		a.settingsTbl.SetCell(0, c, tview.NewTableCell(h).
			SetTextColor(tcell.ColorWhite).SetAttributes(tcell.AttrBold).
			SetSelectable(false).SetExpansion(c))
	}
	for i, s := range a.settingRows {
		hint := "enter cycles"
		if s.kind != setTheme {
			hint = "enter → type value (blank = default)"
		}
		a.settingsTbl.SetCell(i+1, 0, tview.NewTableCell(s.label).SetTextColor(a.theme.Title))
		a.settingsTbl.SetCell(i+1, 1, tview.NewTableCell(tview.Escape(a.settingValue(s))).
			SetTextColor(tcell.ColorWhite).SetExpansion(1))
		a.settingsTbl.SetCell(i+1, 2, tview.NewTableCell(hint).SetTextColor(tcell.ColorGray))
	}
	if sel < 1 {
		sel = 1
	} else if sel > len(a.settingRows) {
		sel = len(a.settingRows)
	}
	a.settingsTbl.Select(sel, 0)
	a.settingsTbl.SetTitle(" Settings — applies live + saved to config · <esc> back ")
}

// editSetting handles enter on a settings row.
func (a *App) editSetting(row int) {
	i := row - 1
	if i < 0 || i >= len(a.settingRows) {
		return
	}
	if a.settingRows[i].kind == setTheme {
		a.cycleTheme()
		return
	}
	a.editingSet = i
	a.openPrompt(promptSettings)
}

// cycleTheme advances to the next built-in theme and repaints everything live.
func (a *App) cycleTheme() {
	cur := 0
	for i, n := range ThemeNames {
		if n == a.theme.Name {
			cur = i
		}
	}
	a.theme = ResolveTheme(ThemeNames[(cur+1)%len(ThemeNames)])
	a.applyTheme()
	a.updateInfo() // header accents use the theme's tags
	a.setHints()
	a.renderSettings()
	a.persistSettings()
	a.flash("theme: "+a.theme.Name, false)
}

// applySettingInput applies a typed TTL/columns value to the row being edited.
func (a *App) applySettingInput(text string) {
	if a.editingSet < 0 || a.editingSet >= len(a.settingRows) {
		return
	}
	s := a.settingRows[a.editingSet]
	switch s.kind {
	case setTTL:
		if text == "" {
			delete(a.opts.TTLOverrides, s.key)
		} else {
			d, err := time.ParseDuration(text)
			if err != nil {
				a.flash("✗ not a duration (e.g. 120s, 5m)", true)
				return
			}
			if a.opts.TTLOverrides == nil {
				a.opts.TTLOverrides = map[string]time.Duration{}
			}
			a.opts.TTLOverrides[s.key] = d
			if s.key == a.res.Key {
				a.res.TTL = d // effective on the view we came from, immediately
			}
		}
	case setColumns:
		if text == "" {
			delete(a.opts.Columns, s.key)
		} else {
			var cols []string
			for _, c := range strings.Split(text, ",") {
				if c = strings.TrimSpace(c); c != "" {
					cols = append(cols, c)
				}
			}
			if a.opts.Columns == nil {
				a.opts.Columns = map[string][]string{}
			}
			a.opts.Columns[s.key] = cols
		}
	}
	a.renderSettings()
	a.persistSettings()
	a.flash("saved "+s.label, false)
}

// persistSettings writes the live theme/TTL/columns back to the config file
// (no-op if the environment doesn't support persistence, e.g. demo mode).
func (a *App) persistSettings() {
	if a.opts.SaveSettings == nil {
		return
	}
	ttl := make(map[string]string, len(a.opts.TTLOverrides))
	for k, d := range a.opts.TTLOverrides {
		ttl[k] = d.String()
	}
	if err := a.opts.SaveSettings(a.theme.Name, ttl, a.opts.Columns); err != nil {
		a.flash("✗ save: "+err.Error(), true)
	}
}

// defaultTTL is a resource's built-in cache TTL from the registry.
func defaultTTL(key string) time.Duration {
	for _, r := range data.Resources() {
		if r.Key == key {
			return r.TTL
		}
	}
	return 0
}
