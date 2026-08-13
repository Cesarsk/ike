# ike hero cast (~22s) — the one that sits at the top of the README.
# Regenerate from the repo root with:
#   go build -ldflags "-X main.version=<version>" -o ./ike . && vhs docs/demo.tape
# Requires vhs (brew install vhs). Runs ./ike explicitly so a stale `ike` on
# PATH can never sneak into the recording.
#
# Two deliberate choices, both learned the hard way:
#
#  1. Everything before the first visible frame happens inside Hide/Show. A
#     viewer watches roughly five seconds, and the old cast spent three of
#     them on the startup splash: a logo, not the product.
#  2. The setup activates the second demo org, so the very first frame is
#     monitors spanning two Datadog orgs with a CTX column. That is the one
#     thing the web UI structurally cannot do, so it leads.
#
# It also fills the frame. 18 monitors per org over two orgs is ~36 rows,
# which is what 800px at FontSize 14 holds, so there is no dead space.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1280
Set Height 800
Set Padding 16
Set TypingSpeed 60ms

Hide
Type "./ike --demo"
Enter
Sleep 3s

# Activate the second org: space on its row in :ctx makes every view span it.
Type ":"
Sleep 400ms
Type "ctx"
Enter
Sleep 1500ms
Down
Sleep 200ms
Space
Sleep 1200ms
Escape
Sleep 1500ms
Show

# ── Frame one: monitors across two orgs, worst first, CTX says which org.
Sleep 2500ms

# ── The claim: two keystrokes from an alert to its cause.
# 'l' on the firing monitor opens the logs for that service.
Type "l"
Sleep 2500ms

# Narrow to errors. The prompt is prefilled with the query the drill-down
# built, so this appends to it and shows how queries compose.
Type "/"
Sleep 400ms
Type " status:error"
Sleep 300ms
Enter
Sleep 2000ms

# 't' on an error line opens the distributed trace it belongs to.
# (Only error logs carry a trace id in demo data, hence the filter above.)
Type "t"
Sleep 3000ms

# esc walks back out the way k9s does: the nav stack remembers each view.
Escape
Sleep 700ms
Escape
Sleep 700ms

# ── Cross-org triage: open incidents and alerting monitors, every org at once.
Type ":"
Sleep 300ms
Type "ov"
Enter
Sleep 3000ms

# ── The service dependency map, then 'g' for the whole call graph.
Type ":"
Sleep 300ms
Type "deps"
Enter
Sleep 1800ms
Type "g"
Sleep 3000ms

Sleep 800ms
