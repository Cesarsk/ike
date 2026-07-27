# ike demo cast. Regenerate from the repo root with:
#   go build -ldflags "-X main.version=<version>" -o ./ike . && vhs docs/demo.tape
# Requires vhs (brew install vhs). Runs ./ike explicitly so a stale `ike` on
# PATH can never sneak into the recording.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1280
Set Height 800
Set Padding 16
Set TypingSpeed 90ms

# Launch demo mode: shows the splash, then the monitors view.
Type "./ike --demo"
Enter
Sleep 3s

# The command palette — ike's ':': a centered overlay, every command listed.
# Pause on the open catalog, then type to fuzzy-filter and run.
Type ":"
Sleep 2s
Type "overview"
Sleep 800ms
Enter
Sleep 2500ms

# Contexts: activate a second org with space — active rows highlight and
# views start merging (CTX column + per-org budgets).
Type ":"
Sleep 600ms
Type "ctx"
Sleep 400ms
Enter
Sleep 1500ms
Down
Sleep 400ms
Space
Sleep 1800ms

# Bulk actions: mark monitors with space, mute them all behind one confirm.
Type ":"
Sleep 600ms
Type "monitors"
Sleep 400ms
Enter
Sleep 1500ms
Space
Sleep 400ms
Down
Space
Sleep 600ms
Type "m"
Sleep 1500ms
Right
Enter
Sleep 2s

# Error Tracking triage: grouped issues, highest count first.
Type ":"
Sleep 600ms
Type "errors"
Sleep 400ms
Enter
Sleep 2500ms

# Infrastructure: hosts (problems first), then containers with the
# host-health join (a running container on a down box shows HOST-ST=down).
Type ":"
Sleep 600ms
Type "hosts"
Sleep 400ms
Enter
Sleep 2s
Type ":"
Sleep 600ms
Type "containers"
Sleep 400ms
Enter
Sleep 2500ms

# Datadog spend: estimated + projected, trend, anomalies.
Type ":"
Sleep 600ms
Type "cost"
Sleep 400ms
Enter
Sleep 2500ms

# On-call: who's paged now + the escalation ladder.
Type ":"
Sleep 600ms
Type "oncall"
Sleep 400ms
Enter
Sleep 1500ms
Enter
Sleep 2500ms
Escape
Sleep 600ms

# The debugging loop: logs, narrowed by a Datadog search query.
Type ":"
Sleep 600ms
Type "logs"
Sleep 400ms
Enter
Sleep 1500ms
Type "/"
Sleep 300ms
Ctrl+U
Type "status:error"
Enter
Sleep 2s

# Watch mode: leave a view refreshing hands-off for a wall display.
Type ":"
Sleep 600ms
Type "overview"
Sleep 400ms
Enter
Sleep 1200ms
Type ":"
Sleep 600ms
Type "watch"
Sleep 400ms
Enter
Sleep 2500ms

# Out.
Type ":"
Sleep 400ms
Type "watch"
Enter
Sleep 500ms
Type "q"
Sleep 1s
