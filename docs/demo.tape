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

# Contexts: the orgs you're connected to (auth type per row), then activate a
# second org with space — active rows highlight.
Type ":ctx"
Enter
Sleep 2s
Down
Sleep 500ms
Space
Sleep 2s

# With two orgs active, views merge them: CTX column + per-org rate budgets.
Type ":monitors"
Enter
Sleep 2500ms

# Cross-org triage: open incidents + alerting monitors, worst first.
Type ":overview"
Enter
Sleep 2500ms

# The incident war room: people, impacts, to-dos in one screen.
Type ":incidents"
Enter
Sleep 1500ms
Enter
Sleep 3s
Escape
Sleep 800ms

# Logs, then narrow them with a Datadog search query.
Type ":logs"
Enter
Sleep 1500ms
Type "/"
Sleep 400ms
Ctrl+U
Type "status:error"
Enter
Sleep 2500ms

# The debugging loop: traces -> trace waterfall -> that trace's logs.
Type ":traces"
Enter
Sleep 1500ms
Type "t"
Sleep 3s
Type "l"
Sleep 2500ms

# A dashboard rendered as a grid of sparklines.
Type ":dashboards"
Enter
Sleep 1s
Enter
Sleep 3s

# Out.
Type "q"
Sleep 1s
