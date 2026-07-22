package ui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// confirmMuteMonitor asks before muting/unmuting the selected monitor. Mute
// state comes from Row.Muted (the monitor's silenced option), which is
// independent of overall_state.
func (a *App) confirmMuteMonitor(r data.Row) {
	verb := "Mute"
	if r.Muted {
		verb = "Unmute"
	}
	name := ""
	if len(r.Cells) > 2 {
		name = r.Cells[2] // NAME column (after STATE, MUTED)
	}
	a.showConfirm(
		fmt.Sprintf("%s monitor in [%s]?\n\n%s\n\nMuting stops notifications (unmute resumes them); the monitor definition itself is unchanged.",
			verb, a.current, name),
		[]string{"Cancel", verb},
		func(label string) {
			if label != verb {
				return
			}
			go func() {
				err := a.providerFor(r).SetMonitorMute(context.Background(), r.ID, !r.Muted)
				a.QueueUpdateDraw(func() {
					if err != nil {
						a.flash("✗ "+err.Error(), true)
						return
					}
					a.flash(verb+"d "+name, false)
					a.load(true)
				})
			}()
		})
}

// bulkOp names a fan-out write for the confirm prompt, action button and
// success flash. button must differ from "Cancel" so it can't be confused
// with the modal's dismiss button.
type bulkOp struct {
	verb   string // sentence + flash: "Mute" → "Mute 3 monitors?"
	button string // confirm action button (≠ "Cancel")
	noun   string
}

// bulkWrite confirms a fan-out write over the marked rows, runs it off the UI
// thread with a partial-failure summary, clears the marks and reloads. Each
// row routes to its own org via providerFor, so a spanned selection writes to
// the right place.
func (a *App) bulkWrite(op bulkOp, do func(data.Row) error) {
	rows := a.markedRows()
	if len(rows) == 0 {
		return
	}
	a.showConfirm(
		fmt.Sprintf("%s %d %s?\nThis writes to Datadog.", op.verb, len(rows), op.noun),
		[]string{"Cancel", op.button},
		func(label string) {
			if label != op.button {
				return
			}
			a.flash(fmt.Sprintf("%s %d %s …", op.verb, len(rows), op.noun), false)
			go func() {
				ok, fail := 0, 0
				for _, r := range rows {
					if err := do(r); err != nil {
						fail++
						slog.Warn("bulk write failed", "op", op.verb, "id", r.ID, "err", err)
					} else {
						ok++
					}
				}
				a.QueueUpdateDraw(func() {
					msg := fmt.Sprintf("%s: %d %s", op.verb, ok, op.noun)
					if fail > 0 {
						msg += fmt.Sprintf(", %d failed", fail)
					}
					a.flash(msg, fail > 0)
					a.marks = nil
					a.load(true) // caches dropped by the writes; re-fetch
				})
			}()
		})
}

// bulkMute mutes every marked monitor.
func (a *App) bulkMute() {
	a.bulkWrite(bulkOp{verb: "Mute", button: "Mute", noun: "monitors"}, func(r data.Row) error {
		return a.providerFor(r).SetMonitorMute(context.Background(), r.ID, true)
	})
}

// bulkResolveIncidents resolves every marked incident.
func (a *App) bulkResolveIncidents() {
	a.bulkWrite(bulkOp{verb: "Resolve", button: "Resolve", noun: "incidents"}, func(r data.Row) error {
		return a.providerFor(r).SetIncidentField(context.Background(), r.ID, "state", "resolved")
	})
}

// bulkCancelDowntimes cancels every marked downtime.
func (a *App) bulkCancelDowntimes() {
	a.bulkWrite(bulkOp{verb: "Cancel", button: "Cancel downtimes", noun: "downtimes"}, func(r data.Row) error {
		return a.providerFor(r).CancelDowntime(context.Background(), r.ID)
	})
}

func (a *App) closeConfirm() {
	a.content.HidePage("confirm")
	ret := a.confirmReturn
	if ret == "" {
		ret = "table"
	}
	a.showPage(ret) // restore the page the modal was opened over (e.g. the to-do panel)
}

// showConfirm displays a confirmation modal with the given buttons and calls
// onDone(label) with the chosen button after closing. A FRESH modal is built
// each time: a reused tview.Modal retains stale button focus across
// ClearButtons/AddButtons, which silently lands Enter on the wrong button.
func (a *App) showConfirm(text string, buttons []string, onDone func(label string)) {
	m := tview.NewModal().SetText(text).AddButtons(buttons)
	m.SetDoneFunc(func(_ int, label string) {
		a.closeConfirm()
		onDone(label)
	})
	a.confirm = m
	a.confirmReturn = a.page // restore this page (not always "table") when the modal closes
	a.content.RemovePage("confirm").AddPage("confirm", m, true, false)
	a.page = "confirm"
	a.content.ShowPage("confirm")
	a.SetFocus(m)
	a.setHints()
}

// confirmIncidentAction offers to move the selected incident to another
// state, behind a confirmation modal (a write path).
func (a *App) confirmIncidentAction(r data.Row) {
	cur := ""
	if len(r.Cells) > 2 {
		cur = strings.ToLower(r.Cells[2])
	}
	var targets []string
	for _, s := range data.IncidentStates {
		if s != cur {
			targets = append(targets, s)
		}
	}
	buttons := append([]string{"Cancel"}, targetLabels(targets)...)
	a.showConfirm(
		fmt.Sprintf("Change %s state (currently %s) to:\nThis writes to Datadog.", r.ID, cur),
		buttons,
		func(label string) {
			state := strings.TrimPrefix(label, "→ ")
			if label == "Cancel" || state == "" {
				return
			}
			a.applyIncidentField(r, "state", state, r.ID+" → "+state)
		})
}

// confirmIncidentSeverity offers to change the selected incident's severity,
// behind a confirmation modal (a write path).
func (a *App) confirmIncidentSeverity(r data.Row) {
	cur := ""
	if len(r.Cells) > 1 {
		cur = strings.ToUpper(r.Cells[1])
	}
	var targets []string
	for _, s := range data.IncidentSeverities {
		if s != cur {
			targets = append(targets, s)
		}
	}
	buttons := append([]string{"Cancel"}, targetLabels(targets)...)
	a.showConfirm(
		fmt.Sprintf("Change %s severity (currently %s) to:\nThis writes to Datadog.", r.ID, cur),
		buttons,
		func(label string) {
			sev := strings.TrimPrefix(label, "→ ")
			if label == "Cancel" || sev == "" {
				return
			}
			a.applyIncidentField(r, "severity", sev, r.ID+" → "+sev)
		})
}

func targetLabels(vals []string) []string {
	out := make([]string, len(vals))
	for i, s := range vals {
		out[i] = "→ " + s
	}
	return out
}

// applyIncidentField performs a confirmed incident write (state or severity)
// off the UI thread; ok is the success flash message.
func (a *App) applyIncidentField(r data.Row, field, value, ok string) {
	a.flash("setting "+r.ID+" "+field+" → "+value+" …", false)
	go func() {
		err := a.providerFor(r).SetIncidentField(context.Background(), r.ID, field, value)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("incident field change failed", "id", r.ID, "field", field, "value", value, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash(ok, false)
			if a.res.Key == "incidents" && a.page == "table" {
				a.load(true) // cache was dropped; re-fetch to show the change
			}
		})
	}()
}

// assignCommanderFlow opens the user picker (current user pinned on top, so
// Enter still means "take command") and, on a pick, confirms before writing.
func (a *App) assignCommanderFlow(r data.Row) {
	a.userPickCtx = r.Ctx
	a.openUserPicker("Commander · "+r.ID, func(u data.User) {
		a.showConfirm(
			fmt.Sprintf("Assign %s commander to %s?\nThis writes to Datadog.", r.ID, u.Handle),
			[]string{"Cancel", "Assign"},
			func(label string) {
				if label != "Assign" {
					return
				}
				a.applyAssignCommander(r, u)
			})
	})
}

func (a *App) applyAssignCommander(r data.Row, u data.User) {
	a.flash("assigning "+r.ID+" commander …", false)
	go func() {
		err := a.providerFor(r).SetIncidentCommander(context.Background(), r.ID, u.ID)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("assign commander failed", "id", r.ID, "user", u.ID, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			// No commander column in the list, so nothing to reload — the
			// cache drop (Cached) keeps the detail view fresh; leave the flash.
			a.flash(r.ID+" commander → "+u.Handle, false)
		})
	}()
}

// addTodoAssigned creates an incident to-do with the picked assignee handle,
// refreshing the panel if it's still open on the same incident.
func (a *App) addTodoAssigned(incidentID, content, handle string) {
	a.flash("adding to-do to "+incidentID+" …", false)
	go func() {
		err := a.todoProv().AddIncidentTodo(context.Background(), incidentID, content, handle)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("add to-do failed", "id", incidentID, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash("to-do added → @"+handle, false)
			if a.page == "todos" && a.todoIncidentID == incidentID {
				a.refreshTodos()
			}
		})
	}()
}

// confirmCancelDowntime offers to cancel the selected downtime, behind a
// confirmation modal (a write path).
func (a *App) confirmCancelDowntime(r data.Row) {
	scope := ""
	if len(r.Cells) > 1 {
		scope = r.Cells[1]
	}
	a.showConfirm(
		fmt.Sprintf("Cancel downtime %s (scope %s)?\nThis writes to Datadog.", r.ID, scope),
		[]string{"Cancel", "Cancel downtime"},
		func(label string) {
			if label != "Cancel downtime" {
				return
			}
			a.applyCancelDowntime(r)
		})
}

func (a *App) applyCancelDowntime(r data.Row) {
	a.flash("cancelling downtime "+r.ID+" …", false)
	go func() {
		err := a.providerFor(r).CancelDowntime(context.Background(), r.ID)
		a.QueueUpdateDraw(func() {
			if err != nil {
				slog.Error("downtime cancel failed", "id", r.ID, "err", err)
				a.flash("✗ "+err.Error(), true)
				return
			}
			a.flash(r.ID+" canceled", false)
			if a.res.Key == "downtimes" && a.page == "table" {
				a.load(true) // cache was dropped; re-fetch to show the change
			}
		})
	}()
}

// ---- data flow -------------------------------------------------------------
