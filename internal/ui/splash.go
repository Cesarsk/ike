package ui

import (
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// splashDuration is how long the startup logo shows before auto-dismissing.
	// The first view loads underneath it, so this is not added latency.
	splashDuration = 1200 * time.Millisecond
	// splashHeight is the fixed row count of the logo block (wordmark + blank +
	// version + tagline), used to vertically centre it in build().
	splashHeight = 10
)

// ikeWordmark is the stylised logo shown on the splash. Its shade characters
// (░▒▓) are intentional; the splash background is transparent (see build) so
// they read cleanly against the terminal instead of a painted rectangle.
var ikeWordmark = []string{
	"█▀▀▀▀█ █▀▀▀▀█ ▓▀▀▀█  ▄▀▀▀▀▀▀▀▀▀█",
	"▀    ▓ ▀    ▓ ▒ ∙ █ █·   ▄▄▄▄▄▄█",
	"▓    ▓ ▓    ▓▄░   ▓ ▓  . ▓▄▄▄▄▄▄",
	"▒   ·▒ ▒   ·▄▄▄  ▀▄ ▒ ∙  ▄▄▄▄▄▄▒",
	"░ .  ░ ░ .  ░ ░  .░ ░    ░▄▄▄▄▄▄",
	"█    █ █    █ █∙  █ █    .    ·█",
	"█▄▄▄▄█ █▄▄▄▄█ █▄▄▄█ █▄▄▄▄▄▄▄▄▄▄█",
}

// splashText renders the startup logo: the IKE wordmark, the version, and the
// tagline. Wordmark lines are padded to the widest with spaces so centre-
// alignment keeps them aligned as one block.
func splashText(version string) string {
	if version == "" {
		version = "dev"
	}
	w := 0
	for _, l := range ikeWordmark {
		if n := utf8.RuneCountInString(l); n > w {
			w = n
		}
	}
	var b strings.Builder
	for _, l := range ikeWordmark {
		b.WriteString("[aqua]" + l + strings.Repeat(" ", w-utf8.RuneCountInString(l)) + "[-]\n")
	}
	// Prefix a "v" for numeric versions (0.1.5 → v0.1.5); leave "dev" (or any
	// non-numeric build string) untouched so we never render "vdev".
	label := version
	if label != "" && label[0] >= '0' && label[0] <= '9' {
		label = "v" + label
	}
	b.WriteString("\n")
	b.WriteString(label + "\n")
	b.WriteString("[gray]github.com/Cesarsk[-]")
	return b.String()
}

// showSplash swaps in the full-screen logo over the (already loading) initial
// view. It auto-dismisses after splashDuration; any keypress also dismisses it
// (see the "splash" case in keys). It remembers the page it covered so the
// dismissal restores it (e.g. the first-run getting-started page).
func (a *App) showSplash() {
	a.splashReturn = a.page
	a.splash.SetText(a.theme.recolor(splashText(a.opts.Version)))
	a.page = "splash"
	a.SetRoot(a.splashView, true)
	a.SetFocus(a.splash)
	go func() {
		time.Sleep(splashDuration)
		a.QueueUpdateDraw(a.dismissSplash)
	}()
}

// dismissSplash restores the normal layout and reveals the page the splash
// covered. Idempotent — the auto-dismiss timer and a keypress can both fire;
// whichever is second is a no-op.
func (a *App) dismissSplash() {
	if a.page != "splash" {
		return
	}
	a.SetRoot(a.rootView, true)
	ret := a.splashReturn
	if ret == "" || ret == "splash" {
		ret = "table"
	}
	a.showPage(ret)
}
