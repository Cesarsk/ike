package ui

import (
	"context"
	"strings"
	"time"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// openUserPicker shows the searchable user picker and calls onPick with the
// chosen user. The acting user is pinned at the top of an empty search, so the
// commander/assignee fast path stays one Enter away. Results come live from
// the API (data.Provider.ListUsers) as the search text changes.
func (a *App) openUserPicker(title string, onPick func(data.User)) {
	a.userPickOnPick = onPick
	a.userPickReturn = a.page
	a.userPickItems = a.userPickItems[:0]
	a.userPick.Clear()
	a.userPickFlex.SetTitle(" " + title + " ")
	a.userSearch.SetText("") // may or may not fire the changed func; we load explicitly below
	a.showPage("userpick")
	a.scheduleUserSearch() // initial (empty-query) load
}

// scheduleUserSearch debounces the search: typing waits 250ms so a burst of
// keystrokes is one request, but an empty query (open/clear) loads at once.
// A sequence number drops results from a superseded query.
func (a *App) scheduleUserSearch() {
	a.userPickSeq++
	seq := a.userPickSeq
	q := a.userSearch.GetText()
	if a.userSearchTimer != nil {
		a.userSearchTimer.Stop()
	}
	if strings.TrimSpace(q) == "" {
		go a.doUserSearch(q, seq)
		return
	}
	a.userSearchTimer = time.AfterFunc(250*time.Millisecond, func() { a.doUserSearch(q, seq) })
}

// doUserSearch runs off the UI thread: it resolves the acting user once (for
// the pinned row) and queries the API, then renders on the main thread if the
// result is still current.
func (a *App) doUserSearch(query string, seq int) {
	var self *data.User
	if a.pickSelf == nil {
		if u, err := a.provider.CurrentUser(context.Background()); err == nil {
			self = &u
		}
	}
	users, err := a.provider.ListUsers(context.Background(), query)
	a.QueueUpdateDraw(func() {
		if seq != a.userPickSeq || a.page != "userpick" {
			return // superseded query, or the user navigated away
		}
		if self != nil && a.pickSelf == nil {
			a.pickSelf = self // set on the UI thread
		}
		if err != nil {
			a.flash("✗ users: "+err.Error(), true)
			return
		}
		a.renderUserPick(query, users)
	})
}

// renderUserPick populates the results list: the acting user pinned first on an
// empty search, then the API results (deduped against the pin).
func (a *App) renderUserPick(query string, users []data.User) {
	cur := a.userPick.GetCurrentItem()
	a.userPick.Clear()
	a.userPickItems = a.userPickItems[:0]

	empty := strings.TrimSpace(query) == ""
	if empty && a.pickSelf != nil {
		a.appendUserRow(*a.pickSelf, true)
	}
	for _, u := range users {
		if empty && a.pickSelf != nil && u.ID == a.pickSelf.ID {
			continue // already pinned
		}
		a.appendUserRow(u, false)
	}
	if a.userPick.GetItemCount() == 0 {
		a.userPick.AddItem(tview.Escape("(no matching users)"), "", 0, nil)
	}
	if n := a.userPick.GetItemCount(); cur >= n {
		cur = n - 1
	}
	if cur < 0 {
		cur = 0
	}
	a.userPick.SetCurrentItem(cur)
}

// appendUserRow adds one user to the list and its backing slice in lockstep, so
// the highlighted index maps straight to a data.User in userPickChoose.
func (a *App) appendUserRow(u data.User, pinned bool) {
	label := u.Handle
	if u.Name != "" {
		label += "  ·  " + u.Name
	}
	if pinned {
		label += "   (you)"
	}
	a.userPick.AddItem(tview.Escape(label), "", 0, nil)
	a.userPickItems = append(a.userPickItems, u)
}

// userPickMove shifts the highlighted result (arrow keys; the search field has
// focus so it can't move the list itself).
func (a *App) userPickMove(delta int) {
	n := a.userPick.GetItemCount()
	if n == 0 {
		return
	}
	i := a.userPick.GetCurrentItem() + delta
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	a.userPick.SetCurrentItem(i)
}

// userPickChoose fires the pick callback for the highlighted user, after
// returning to the page the picker was opened over.
func (a *App) userPickChoose() {
	i := a.userPick.GetCurrentItem()
	if i < 0 || i >= len(a.userPickItems) {
		return // the "(no matching users)" placeholder row
	}
	u := a.userPickItems[i]
	onPick := a.userPickOnPick
	a.closeUserPick()
	if onPick != nil {
		onPick(u)
	}
}

// closeUserPick returns to the page the picker was opened over (without
// touching the nav stack — the picker is a transient modal, not a view).
func (a *App) closeUserPick() {
	ret := a.userPickReturn
	if ret == "" {
		ret = "table"
	}
	a.showPage(ret)
}
