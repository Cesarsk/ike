package ui

import (
	"context"
	"strings"

	"github.com/rivo/tview"

	"github.com/Cesarsk/ike/internal/data"
)

// openTodoPanel opens the to-do panel for an incident: it lists the incident's
// to-dos and hosts add ('a'), toggle-complete ('c'/space/enter) and delete
// ('d'). Pushes the nav stack so esc returns to the incidents table.
func (a *App) openTodoPanel(incidentID string) {
	a.todoIncidentID = incidentID
	a.pushNav()
	a.todoItems = nil
	a.todoList.Clear()
	a.todoList.SetTitle(" To-dos · " + incidentID + "   <a>add  <c/space>done  <d>delete  <esc>back ")
	a.todoList.AddItem(tview.Escape("loading…"), "", 0, nil)
	a.showPage("todos")
	a.refreshTodos()
}

// refreshTodos re-fetches the current incident's to-dos and re-renders. Called
// on open and after every write, so the panel always reflects the API.
func (a *App) refreshTodos() {
	inc := a.todoIncidentID
	go func() {
		todos, err := a.provider.IncidentTodos(context.Background(), inc)
		a.QueueUpdateDraw(func() {
			if a.page != "todos" || a.todoIncidentID != inc {
				return // navigated away / switched incident
			}
			if err != nil {
				a.flash("✗ to-dos: "+err.Error(), true)
				a.todoItems = nil
				a.renderTodos()
				return
			}
			a.todoItems = todos
			a.renderTodos()
		})
	}()
}

// renderTodos populates the list from a.todoItems, keeping the backing slice
// index-aligned with the rows so selectedTodo maps cleanly.
func (a *App) renderTodos() {
	cur := a.todoList.GetCurrentItem()
	a.todoList.Clear()
	if len(a.todoItems) == 0 {
		a.todoList.AddItem(tview.Escape("(no to-dos — press 'a' to add one)"), "", 0, nil)
		return
	}
	for _, t := range a.todoItems {
		mark := "[ ]"
		if t.Completed {
			mark = "[x]"
		}
		sec := ""
		if len(t.Assignees) > 0 {
			sec = "    @" + strings.Join(t.Assignees, " @")
		}
		// Escape: tview.List parses "[x]"/"[ ]" as colour tags otherwise.
		a.todoList.AddItem(tview.Escape(mark+" "+t.Content), tview.Escape(sec), 0, nil)
	}
	if n := a.todoList.GetItemCount(); cur >= n {
		cur = n - 1
	}
	if cur < 0 {
		cur = 0
	}
	a.todoList.SetCurrentItem(cur)
}

// selectedTodo returns the highlighted to-do (false on the placeholder row).
func (a *App) selectedTodo() (data.Todo, bool) {
	i := a.todoList.GetCurrentItem()
	if i < 0 || i >= len(a.todoItems) {
		return data.Todo{}, false
	}
	return a.todoItems[i], true
}

// addTodoFlow prompts for the to-do content; on submit, promptDone opens the
// assignee picker (see the promptTodo case) and then creates the to-do.
func (a *App) addTodoFlow() {
	a.openPrompt(promptTodo) // todoIncidentID is already the panel's incident
}

// toggleTodoComplete flips the highlighted to-do's done state, then refreshes.
func (a *App) toggleTodoComplete() {
	t, ok := a.selectedTodo()
	if !ok {
		return
	}
	inc := a.todoIncidentID
	done := !t.Completed
	a.flash("updating to-do …", false)
	go func() {
		err := a.provider.SetIncidentTodoCompleted(context.Background(), inc, t, done)
		a.QueueUpdateDraw(func() {
			if err != nil {
				a.flash("✗ "+err.Error(), true)
				return
			}
			if done {
				a.flash("to-do completed", false)
			} else {
				a.flash("to-do reopened", false)
			}
			a.refreshTodos()
		})
	}()
}

// deleteTodoFlow deletes the highlighted to-do behind a confirmation modal.
func (a *App) deleteTodoFlow() {
	t, ok := a.selectedTodo()
	if !ok {
		return
	}
	inc := a.todoIncidentID
	a.showConfirm(
		"Delete this to-do?\n"+t.Content+"\nThis writes to Datadog.",
		[]string{"Cancel", "Delete"},
		func(label string) {
			if label != "Delete" {
				return
			}
			a.flash("deleting to-do …", false)
			go func() {
				err := a.provider.DeleteIncidentTodo(context.Background(), inc, t.ID)
				a.QueueUpdateDraw(func() {
					if err != nil {
						a.flash("✗ "+err.Error(), true)
						return
					}
					a.flash("to-do deleted", false)
					a.refreshTodos()
				})
			}()
		})
}
