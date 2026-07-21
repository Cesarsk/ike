package ui

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/config"
	"github.com/Cesarsk/ike/internal/data"
)

// showContexts opens the :ctx view listing the configured Datadog orgs.
func (a *App) showContexts() {
	if a.page == "table" && a.res.Key == ctxResource.Key {
		return
	}
	if a.res.Key != "" {
		a.pushNav()
	}
	a.res = ctxResource
	a.resetView()
	a.rows = nil
	a.filtered = nil
	a.pendingSel = 1
	a.showPage("table")
	a.render()
	a.load(false)
}

func (a *App) contextRows() []data.Row {
	rows := make([]data.Row, 0, len(a.ctxInfos))
	for _, c := range a.ctxInfos {
		// One state, one word: every org participating in views reads
		// "active" — the org you switched to (enter) and any space-marked
		// ones alike. Blank = not participating. Which org you're driving
		// is already in the header (Mode: live [name]).
		status := ""
		if c.Name == a.current || c.Active {
			status = "active"
		}
		rows = append(rows, data.Row{
			ID:    c.Name,
			Cells: []string{status, c.Name, c.Site, authLabel(c.Auth), c.Keys},
			Raw:   map[string]any{"name": c.Name, "site": c.Site, "auth": authLabel(c.Auth), "keys": c.Keys, "active": c.Active},
		})
	}
	return rows
}

// authLabel is the :ctx AUTH column value for a stored auth shape.
func authLabel(auth string) string {
	switch auth {
	case "oauth":
		return "oauth"
	case "token":
		return "token"
	default:
		return "keys"
	}
}

// ctxActive reports a context's explicit-activation flag (space in :ctx).
func (a *App) ctxActive(name string) bool {
	for _, c := range a.ctxInfos {
		if c.Name == name {
			return c.Active
		}
	}
	return false
}

// activeEntries returns the active providers in stable order: the current
// context first, then explicitly-activated contexts in config order.
func (a *App) activeEntries() []ctxProvider {
	out := []ctxProvider{{a.current, a.provider}}
	for _, c := range a.ctxInfos {
		if c.Name == a.current || !c.Active {
			continue
		}
		if p, ok := a.providers[c.Name]; ok {
			out = append(out, ctxProvider{c.Name, p})
		}
	}
	return out
}

// spanning reports whether the current view should fan out over active orgs.
func (a *App) spanning() bool {
	return spanningResources[a.res.Key] && len(a.activeEntries()) > 1
}

// providerFor routes a row-scoped call (detail, drill-down, write) to the
// row's origin org; rows without a Ctx belong to the current context.
func (a *App) providerFor(r data.Row) *data.Cached {
	if r.Ctx != "" {
		if p, ok := a.providers[r.Ctx]; ok {
			return p
		}
	}
	return a.provider
}

// toggleContextActive flips a context's explicit activation (space in :ctx).
// Activating brings up its provider; deactivating tears it down (unless it is
// the current context, which stays active implicitly — the flag then only
// controls whether it survives a current-context switch).
func (a *App) toggleContextActive(name string) {
	for i, c := range a.ctxInfos {
		if c.Name != name {
			continue
		}
		if !c.Active { // activate
			if name != a.current {
				p, err := a.opts.Factory(name)
				if err != nil {
					a.flash("✗ context "+name+": "+err.Error(), true)
					return
				}
				a.providers[name] = data.NewCached(p)
			}
			a.ctxInfos[i].Active = true
			if name == a.current {
				a.flash("context "+name+" marked — it will stay active when you switch to another org", false)
			} else {
				a.flash("context "+name+" activated — spanning views merge it", false)
			}
		} else { // deactivate
			a.ctxInfos[i].Active = false
			if name != a.current {
				delete(a.providers, name) // hard teardown, same boundary as a switch
				a.flash("context "+name+" deactivated", false)
			} else {
				// The driven org can't leave the views — its row stays "active".
				// Without this message a space here looks like a no-op bug.
				a.flash("context "+name+" stays active while you're driving it — it drops out when you switch away", false)
			}
		}
		if a.opts.PersistActive != nil {
			if err := a.opts.PersistActive(name, a.ctxInfos[i].Active); err != nil {
				slog.Warn("persist active failed", "context", name, "err", err)
			}
		}
		a.load(false) // refresh the :ctx table markers
		return
	}
}

// switchContext moves the current context. The target keeps its cache if it
// was already active; the old current is torn down unless explicitly active
// (space) — so single-active usage behaves exactly like the old hard switch,
// while activated orgs survive. Navigation history and queries always reset.
func (a *App) switchContext(name string) {
	if name == a.current {
		a.flash("already on context "+name, false)
		return
	}
	p, ok := a.providers[name]
	if !ok {
		np, err := a.opts.Factory(name)
		if err != nil {
			slog.Error("context switch failed", "to", name, "err", err)
			// An OAuth context with no tokens yet isn't an error the user should
			// see raw — it just hasn't been signed into. Point them at 'O'.
			if a.ctxAuth(name) == "oauth" {
				a.flash("context "+name+" is not signed in yet — press O to sign in", false)
			} else {
				a.flash("✗ context "+name+": "+err.Error(), true)
			}
			return
		}
		p = data.NewCached(np)
	}
	slog.Info("context switch", "from", a.current, "to", name)
	old := a.current
	if !a.ctxActive(old) {
		delete(a.providers, old)
	}
	a.providers[name] = p
	a.provider = p
	a.current = name
	a.stack = nil
	a.queries = map[string]string{}
	a.detailRow = data.Row{}
	a.res = data.Resource{} // so switchResource doesn't push the ctx view
	a.flash("context → "+name, false)
	a.switchResource(data.Resources()[0])
	a.persistSession() // remember the new org (+ its reset-to-first view)
}

// persistSession writes the active org + view so a new session reopens here.
// Called at deliberate navigation points (org switch, :view switch), never for
// transient drill-downs. The ctx switcher and the empty resource are skipped.
func (a *App) persistSession() {
	if a.opts.PersistSession == nil {
		return
	}
	if a.res.Key == "" || a.res.Key == ctxResource.Key {
		return
	}
	if err := a.opts.PersistSession(a.current, a.res.Key); err != nil {
		slog.Warn("persist session failed", "context", a.current, "view", a.res.Key, "err", err)
	}
}

// authModeFor maps a stored Auth string to a dropdown index.
func authModeFor(auth string) int {
	switch auth {
	case "oauth":
		return authModeOAuth
	case "token":
		return authModeToken
	default:
		return authModeKeys
	}
}

// authModeName maps a dropdown index to the authMode string UpdateContext takes.
func authModeName(mode int) string {
	switch mode {
	case authModeOAuth:
		return "oauth"
	case authModeToken:
		return "token"
	default:
		return "keys"
	}
}

// openCtxForm shows the add-context form (:ctx → a): a new context, defaulting
// to OAuth. Secret fields go to the OS keychain, never the config file.
func (a *App) openCtxForm() {
	if a.opts.AddContext == nil {
		a.flash("adding contexts is not available in this mode", true)
		return
	}
	a.showCtxForm("", authModeOAuth, ContextInfo{})
}

// openEditForm shows the edit form for the selected context (:ctx → e),
// pre-filled and defaulting to its current auth type.
func (a *App) openEditForm() {
	if a.opts.UpdateContext == nil {
		a.flash("editing contexts is not available in this mode", true)
		return
	}
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	var info ContextInfo
	for _, c := range a.ctxInfos {
		if c.Name == r.ID {
			info = c
		}
	}
	if info.Name == "" {
		return
	}
	a.showCtxForm(info.Name, authModeFor(info.Auth), info)
}

// showCtxForm builds the shared add/edit context form. editing is "" when
// adding; otherwise the name being edited (its Name field is locked). mode is
// the initial Auth selection; v pre-fills the common fields.
func (a *App) showCtxForm(editing string, mode int, v ContextInfo) {
	a.pushNav()
	a.formErr.SetText("")
	a.editingCtx = editing
	a.ctxForm.Clear(true)
	a.ctxFormBuilding = true
	a.ctxForm.AddDropDown("Auth", ctxAuthOptions, mode, func(_ string, idx int) {
		if a.ctxFormBuilding {
			return
		}
		// Preserve the common fields the user already filled, then rebuild the
		// credential fields for the newly-chosen mode.
		cur := ContextInfo{
			Name:      a.ctxFieldText("Name"),
			Site:      a.ctxSelectedSite(),
			Subdomain: a.ctxFieldText("Subdomain (optional)"),
		}
		a.rebuildCtxBody(idx, cur)
		a.SetFocus(a.ctxForm)
	})
	if dd, ok := a.ctxForm.GetFormItemByLabel("Auth").(*tview.DropDown); ok {
		dropdownNoArrowOpen(dd)
	}
	a.rebuildCtxBody(mode, v)
	a.ctxFormBuilding = false
	if editing == "" {
		a.ctxForm.SetTitle(" Add context ")
	} else {
		a.ctxForm.SetTitle(" Edit context: " + editing + " ")
	}
	a.showPage("ctxform")
}

// rebuildCtxBody rebuilds the form's fields below the Auth dropdown for the
// given mode. The dropdown at index 0 is never touched, so this is safe to
// call from the dropdown's own selection callback.
func (a *App) rebuildCtxBody(mode int, v ContextInfo) {
	for a.ctxForm.GetFormItemCount() > 1 {
		a.ctxForm.RemoveFormItem(a.ctxForm.GetFormItemCount() - 1)
	}
	a.ctxForm.ClearButtons()

	labels := make([]string, len(config.Sites))
	for i, s := range config.Sites {
		labels[i] = fmt.Sprintf("%-17s (%s)", s, siteRegions[s])
	}
	siteIdx := 0
	for i, s := range config.Sites {
		if s == v.Site {
			siteIdx = i
		}
	}

	// When editing, the name is the keychain/config key (rename is out of
	// scope) so it isn't an editable field — it's in the form title instead.
	if a.editingCtx == "" {
		a.ctxForm.AddInputField("Name", v.Name, 30, nil, nil)
	}
	a.ctxForm.AddDropDown("Site", labels, siteIdx, nil)
	if dd, ok := a.ctxForm.GetFormItemByLabel("Site").(*tview.DropDown); ok {
		dropdownNoArrowOpen(dd)
	}
	switch mode {
	case authModeKeys:
		a.ctxForm.
			AddPasswordField("API key", "", 50, '*', nil).
			AddPasswordField("APP key", "", 50, '*', nil)
	case authModeToken:
		a.ctxForm.AddPasswordField("Access token", "", 50, '*', nil)
	}
	a.ctxForm.AddInputField("Subdomain (optional)", v.Subdomain, 30, nil, nil)

	save := "Save"
	if mode == authModeOAuth {
		if a.editingCtx == "" {
			save = "Sign in with browser"
		} else {
			save = "Save & sign in"
		}
	}
	a.ctxForm.AddButton(save, a.submitCtxForm).AddButton("Cancel", a.back)
}

// ctxDropdownOpen reports whether either dropdown in the context form has its
// list open — used so <esc> closes the list before it closes the form.
func (a *App) ctxDropdownOpen() bool {
	for _, label := range []string{"Auth", "Site"} {
		if dd, ok := a.ctxForm.GetFormItemByLabel(label).(*tview.DropDown); ok && dd.IsOpen() {
			return true
		}
	}
	return false
}

// dropdownNoArrowOpen keeps a closed dropdown from opening on Up/Down — those
// move between form fields like every other item instead (the list still opens
// on enter or space). Once the list is open, arrows pass through to navigate
// options as usual.
func dropdownNoArrowOpen(dd *tview.DropDown) {
	dd.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if dd.IsOpen() {
			return ev
		}
		switch ev.Key() {
		case tcell.KeyDown:
			return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
		case tcell.KeyUp:
			return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
		}
		return ev
	})
}

// ctxFieldText reads an input/password field by label ("" if the field is
// absent in the current mode).
func (a *App) ctxFieldText(label string) string {
	if it := a.ctxForm.GetFormItemByLabel(label); it != nil {
		if inp, ok := it.(*tview.InputField); ok {
			return inp.GetText()
		}
	}
	return ""
}

// ctxSelectedSite returns the site chosen in the Site dropdown.
func (a *App) ctxSelectedSite() string {
	if it := a.ctxForm.GetFormItemByLabel("Site"); it != nil {
		if dd, ok := it.(*tview.DropDown); ok {
			if idx, _ := dd.GetCurrentOption(); idx >= 0 && idx < len(config.Sites) {
				return config.Sites[idx]
			}
		}
	}
	return ""
}

// beginRowLogin runs the browser sign-in for the selected :ctx row ('O'). An
// OAuth context signs in (or refreshes) directly; a key/token context is a
// conversion, so it asks first before switching that context to OAuth.
func (a *App) beginRowLogin() {
	if a.opts.OAuthLogin == nil {
		a.flash("browser sign-in is not available in this mode", true)
		return
	}
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	name := r.ID
	if a.ctxAuth(name) == "oauth" {
		a.startLogin(name)
		return
	}
	using := "an API + APP key pair"
	if a.ctxAuth(name) == "token" {
		using = "an access token"
	}
	a.showConfirm(
		fmt.Sprintf("Context %q signs in with %s.\nBrowser sign-in will replace that with OAuth for this context.\nContinue?", name, using),
		[]string{"Cancel", "Sign in with browser"},
		func(label string) {
			if label == "Sign in with browser" {
				a.startLogin(name)
			}
		})
}

// startLogin kicks off the blocking browser flow for one context off the UI
// thread and folds the refreshed info back into :ctx. The flash names the host
// being opened so it's clear the org's subdomain (not app.<site>) is used.
func (a *App) startLogin(name string) {
	a.flash("browser opened for "+name+" → "+a.ctxAuthHost(name)+" — complete the sign-in there …", false)
	go func() {
		info, err := a.opts.OAuthLogin(name)
		a.QueueUpdateDraw(func() {
			if err != nil {
				a.flash("✗ sign-in: "+err.Error(), true)
				return
			}
			replaced := false
			for i, c := range a.ctxInfos {
				if c.Name == info.Name {
					a.ctxInfos[i] = info
					replaced = true
				}
			}
			if !replaced {
				a.ctxInfos = append(a.ctxInfos, info)
			}
			if a.res.Key == ctxResource.Key {
				a.load(false)
			}
			a.flash("signed in — context "+info.Name+" ready (enter to switch)", false)
		})
	}()
}

// ctxAuth returns a context's auth shape ("oauth" / "token" / "" for keys).
func (a *App) ctxAuth(name string) string {
	for _, c := range a.ctxInfos {
		if c.Name == name {
			return c.Auth
		}
	}
	return ""
}

// ctxAuthHost is the browser host the OAuth sign-in opens for a context: its
// custom subdomain when set, else app.<site>. Mirrors auth.EndpointsFor so the
// flash matches the URL actually opened.
func (a *App) ctxAuthHost(name string) string {
	for _, c := range a.ctxInfos {
		if c.Name == name {
			if c.Subdomain != "" {
				return c.Subdomain + "." + c.Site
			}
			return "app." + c.Site
		}
	}
	return name
}

// formError shows a validation error inside the form page (and logs it) —
// the bottom status bar alone is too easy to miss while filling fields.
func (a *App) formError(msg string) {
	slog.Warn("add-context form rejected", "reason", msg)
	a.formErr.SetText(" [red::b]✗ " + tview.Escape(msg))
}

// submitCtxForm validates and applies the :ctx form for both add and edit.
// Fields are read by label because which credential fields exist depends on
// the chosen auth mode.
func (a *App) submitCtxForm() {
	mode := authModeOAuth
	if it := a.ctxForm.GetFormItemByLabel("Auth"); it != nil {
		if dd, ok := it.(*tview.DropDown); ok {
			mode, _ = dd.GetCurrentOption()
		}
	}
	editing := a.editingCtx
	name := strings.TrimSpace(a.ctxFieldText("Name"))
	if editing != "" {
		name = editing // locked in edit mode
	}
	site := a.ctxSelectedSite()
	if site == "" {
		site = config.Sites[0]
	}
	apiKey := a.ctxFieldText("API key")
	appKey := a.ctxFieldText("APP key")
	token := a.ctxFieldText("Access token")
	subdomain := strings.TrimSpace(a.ctxFieldText("Subdomain (optional)"))

	if name == "" {
		a.formError("Name is required")
		return
	}
	if !config.ValidSubdomain(subdomain) {
		a.formError("subdomain must be a single DNS label, e.g. acme-stage (from https://acme-stage." + site + ")")
		return
	}
	if editing == "" {
		for _, c := range a.ctxInfos {
			if c.Name == name {
				a.formError("context " + name + " already exists")
				return
			}
		}
	}

	// Whether the credential fields must be filled: always for a new key/token
	// context; on edit, only when switching INTO a mode the context doesn't
	// already store in the keychain (empty means "keep the stored secret").
	credsRequired := true
	if editing != "" {
		for _, c := range a.ctxInfos {
			if c.Name == editing && authModeFor(c.Auth) == mode && strings.HasPrefix(c.Keys, "keychain") {
				credsRequired = false
			}
		}
	}
	if mode == authModeKeys && credsRequired && (apiKey == "" || appKey == "") {
		a.formError("API keys selected: fill BOTH the API key and the APP key")
		return
	}
	if mode == authModeToken && credsRequired && token == "" {
		a.formError("access token selected: fill the Access token field")
		return
	}
	if mode != authModeKeys {
		apiKey, appKey = "", ""
	}
	if mode != authModeToken {
		token = ""
	}

	f := ctxSubmit{
		mode: mode, name: name, site: site,
		apiKey: apiKey, appKey: appKey, token: token, subdomain: subdomain,
	}
	if editing != "" {
		a.submitEditCtx(f)
		return
	}
	a.submitAddCtx(f)
}

// ctxSubmit carries the validated add/edit form fields to the submit helpers.
// Named fields defuse the all-string parameter list (which was which?). It is
// built once in submitCtxForm and consumed by one of the two helpers below.
type ctxSubmit struct {
	mode                             int
	name, site                       string
	apiKey, appKey, token, subdomain string
}

// submitAddCtx handles the add path: OAuth creates a pending context and signs
// in from the form; keys/token persist to the keychain.
func (a *App) submitAddCtx(f ctxSubmit) {
	if f.mode == authModeOAuth {
		if a.opts.AddOAuthContext == nil {
			a.formError("browser sign-in is not available in this mode")
			return
		}
		info, err := a.opts.AddOAuthContext(f.name, f.site, f.subdomain)
		if err != nil {
			a.formError(err.Error())
			return
		}
		slog.Info("oauth context added", "name", f.name, "site", f.site)
		a.ctxInfos = append(a.ctxInfos, info)
		a.back()
		a.startLogin(f.name) // the form's button IS the sign-in
		return
	}
	info, err := a.opts.AddContext(f.name, f.site, f.apiKey, f.appKey, f.token, f.subdomain)
	if err != nil {
		a.formError(err.Error())
		return
	}
	slog.Info("context added", "name", f.name, "site", f.site, "auth", authModeName(f.mode))
	a.ctxInfos = append(a.ctxInfos, info)
	a.back()
	a.flash("context "+f.name+" added — enter on it to switch", false)
}

// submitEditCtx handles the edit path: UpdateContext persists the changes; an
// OAuth edit also (re-)runs the browser sign-in.
func (a *App) submitEditCtx(f ctxSubmit) {
	name := a.editingCtx
	info, err := a.opts.UpdateContext(name, authModeName(f.mode), f.site, f.apiKey, f.appKey, f.token, f.subdomain)
	if err != nil {
		a.formError(err.Error())
		return
	}
	slog.Info("context updated", "name", name, "site", f.site, "auth", authModeName(f.mode))
	for i, c := range a.ctxInfos {
		if c.Name == name {
			a.ctxInfos[i] = info
		}
	}
	a.back()
	if f.mode == authModeOAuth {
		a.startLogin(name)
		return
	}
	a.flash("context "+name+" updated", false)
}

// confirmDeleteContext asks before removing the selected context (ctrl-d).
func (a *App) confirmDeleteContext() {
	if a.opts.DeleteContext == nil {
		a.flash("deleting contexts is not available in this mode", true)
		return
	}
	r, ok := a.selectedRow()
	if !ok {
		return
	}
	name := r.ID
	if name == a.current {
		a.flash("✗ cannot delete the active context — switch away first", true)
		return
	}
	a.showConfirm(
		fmt.Sprintf("Delete context %q?\nIts credentials are removed from the OS keychain;\nthe Datadog org itself is untouched.", name),
		[]string{"Cancel", "Delete"},
		func(label string) {
			if label != "Delete" {
				return
			}
			if err := a.opts.DeleteContext(name); err != nil {
				slog.Error("context delete failed", "name", name, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			slog.Info("context deleted", "name", name)
			for i, c := range a.ctxInfos {
				if c.Name == name {
					a.ctxInfos = append(a.ctxInfos[:i], a.ctxInfos[i+1:]...)
					break
				}
			}
			a.load(false) // re-render the contexts table
			a.flash("context "+name+" deleted", false)
		})
}
