# ike demo cast. Regenerate with:  vhs docs/demo.tape
# Requires: vhs (brew install vhs) and ike on PATH (brew install cesarsk/tap/ike).

Output docs/demo.gif

Require ike

Set Shell "bash"
Set FontSize 14
Set Width 1280
Set Height 800
Set Padding 16
Set TypingSpeed 90ms

# Launch demo mode: shows the splash, then the monitors view.
Type "ike --demo"
Enter
Sleep 3s

# Incidents view.
Type ":incidents"
Enter
Sleep 2s

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
