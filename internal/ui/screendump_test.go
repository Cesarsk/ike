package ui

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestScreenDump renders the demo TUI headlessly and prints the screens to
// stdout. It only runs when IKE_DUMP=1 — used to regenerate the README
// screenshots:
//
//	IKE_DUMP=1 go test -run TestScreenDump ./internal/ui -v
func TestScreenDump(t *testing.T) {
	if os.Getenv("IKE_DUMP") != "1" {
		t.Skip("set IKE_DUMP=1 to dump screens")
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(110, 28)

	app := newDemoApp(t)
	app.SetScreen(sim)
	go func() { _ = app.Run() }()

	waitFor(t, sim, "Kong data plane 5xx")
	fmt.Println("=== monitors ===")
	fmt.Println(screenText(sim))

	typeCmd(sim, ":logs")
	waitFor(t, sim, "Logs(")
	time.Sleep(200 * time.Millisecond)
	fmt.Println("=== logs ===")
	fmt.Println(screenText(sim))

	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Monitor/")
	fmt.Println("=== detail ===")
	fmt.Println(screenText(sim))

	app.Stop()
}
