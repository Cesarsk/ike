package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// Theme is a named colour palette for the TUI chrome. The structural colours
// drive the tview widgets (borders, titles, selection highlight, form fields);
// Accent/Key/Dim are tview dynamic-colour tag names woven into the header,
// hint bar and help text. Semantic colours (alert red, ok green, budget
// health) are deliberately NOT themed — a muted alert must still read as red.
type Theme struct {
	Name     string
	Border   tcell.Color
	Title    tcell.Color
	SelectBg tcell.Color
	SelectFg tcell.Color
	// MarkBg tints rows that are "marked" (active orgs in :ctx) so a
	// multi-selection reads as a set. Dimmer than SelectBg, so the cursor
	// bar stays distinguishable on top of it.
	MarkBg  tcell.Color
	FieldBg tcell.Color
	FieldFg tcell.Color
	Label   tcell.Color
	Button  tcell.Color
	Accent  string // section headers / labels in dynamic text (default "orange")
	Key     string // key names in dynamic text (default "aqua")
	Dim     string // secondary notes in dynamic text (default "gray")
}

// themes are the built-in palettes. "ike" is the signature look (and what an
// unset theme resolves to); "default" preserves the original pre-identity look.
var themes = map[string]Theme{
	"ike": {
		Name: "ike", Border: tcell.ColorCoral, Title: tcell.ColorLightSalmon,
		SelectBg: tcell.ColorDarkSlateGray, SelectFg: tcell.ColorWhite,
		MarkBg:  tcell.NewRGBColor(58, 36, 30), // dim warm coral tint
		FieldBg: tcell.ColorBlack, FieldFg: tcell.ColorMediumTurquoise,
		Label: tcell.ColorCoral, Button: tcell.ColorCoral,
		Accent: "coral", Key: "mediumturquoise", Dim: "gray",
	},
	"default": {
		Name: "default", Border: tcell.ColorDodgerBlue, Title: tcell.ColorOrange,
		SelectBg: tcell.ColorDarkSlateGray, SelectFg: tcell.ColorWhite,
		MarkBg:  tcell.NewRGBColor(24, 38, 58), // dim blue tint
		FieldBg: tcell.ColorBlack, FieldFg: tcell.ColorAqua,
		Label: tcell.ColorOrange, Button: tcell.ColorDodgerBlue,
		Accent: "orange", Key: "aqua", Dim: "gray",
	},
	"mono": {
		Name: "mono", Border: tcell.ColorWhite, Title: tcell.ColorWhite,
		SelectBg: tcell.ColorGray, SelectFg: tcell.ColorWhite,
		MarkBg:  tcell.NewRGBColor(44, 44, 44), // dim gray tint
		FieldBg: tcell.ColorBlack, FieldFg: tcell.ColorWhite,
		Label: tcell.ColorWhite, Button: tcell.ColorGray,
		Accent: "white", Key: "white", Dim: "gray",
	},
	"nord": {
		Name: "nord", Border: tcell.ColorSteelBlue, Title: tcell.ColorLightCyan,
		SelectBg: tcell.ColorDarkSlateBlue, SelectFg: tcell.ColorWhite,
		MarkBg:  tcell.NewRGBColor(46, 52, 64), // nord0 polar night
		FieldBg: tcell.ColorBlack, FieldFg: tcell.ColorLightCyan,
		Label: tcell.ColorLightCyan, Button: tcell.ColorSteelBlue,
		Accent: "#88c0d0", Key: "#a3be8c", Dim: "gray",
	},
	"solarized": {
		Name: "solarized", Border: tcell.ColorDarkCyan, Title: tcell.ColorGoldenrod,
		SelectBg: tcell.ColorDarkSlateGray, SelectFg: tcell.ColorWhite,
		MarkBg:  tcell.NewRGBColor(7, 54, 66), // solarized base02
		FieldBg: tcell.ColorBlack, FieldFg: tcell.ColorDarkCyan,
		Label: tcell.ColorGoldenrod, Button: tcell.ColorDarkCyan,
		Accent: "#b58900", Key: "#2aa198", Dim: "#657b83",
	},
}

// ThemeNames lists the built-in theme names (for docs/validation).
var ThemeNames = []string{"ike", "default", "mono", "nord", "solarized"}

// ResolveTheme returns the named palette. Empty or unknown names resolve to
// "ike" (the signature look) — an unrecognised theme should never break the
// UI, and `theme: default` restores the original palette.
func ResolveTheme(name string) Theme {
	if t, ok := themes[strings.ToLower(strings.TrimSpace(name))]; ok {
		return t
	}
	return themes["ike"]
}

// recolor rewrites the canonical dynamic-colour tags used when building header,
// hint and help strings ([orange]/[aqua]/[gray]) into this theme's tag names,
// so themed text matches the structural colours. A no-op for the default theme.
func (t Theme) recolor(s string) string {
	if t.Accent == "orange" && t.Key == "aqua" && t.Dim == "gray" {
		return s
	}
	return strings.NewReplacer(
		"[orange]", "["+t.Accent+"]",
		"[aqua]", "["+t.Key+"]",
		"[gray]", "["+t.Dim+"]",
	).Replace(s)
}
