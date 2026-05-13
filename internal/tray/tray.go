package tray

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"runtime"
	"time"

	"github.com/getlantern/systray"

	"github.com/aralde/operatorlm/internal/config"
	"github.com/aralde/operatorlm/internal/update"
)

func OnReady(cfg *config.Config, updMgr *update.Manager) {
	systray.SetIcon(buildIcon())
	systray.SetTitle("OperatorLM")
	systray.SetTooltip("OperatorLM · local LLM proxy")

	mOpen := systray.AddMenuItem("Open config panel", "Open config in browser")
	mUpdate := systray.AddMenuItem("Check for updates", "Download and install the latest release")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop the proxy")

	// Reflect updater state into the menu label. A separate ticker keeps the
	// label honest while CheckAndUpdate is running, since that call is
	// asynchronous from the click.
	go watchUpdateState(updMgr, mUpdate)

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(fmt.Sprintf("http://%s/", cfg.ListenAddr()))
			case <-mUpdate.ClickedCh:
				go updMgr.CheckAndUpdate(context.Background())
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func watchUpdateState(m *update.Manager, item *systray.MenuItem) {
	const defaultLabel = "Check for updates"
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	last := ""
	for range tick.C {
		s := m.Snapshot()
		var label string
		switch s.Status {
		case "checking":
			label = "Checking for updates…"
		case "downloading":
			label = "Downloading update…"
		case "verifying":
			label = "Verifying update…"
		case "applying":
			label = "Installing update…"
		case "restarting":
			label = "Restarting…"
		case "uptodate":
			label = "Up to date (" + s.Current + ")"
		case "error":
			label = "Update failed — click to retry"
		default:
			label = defaultLabel
		}
		if label != last {
			item.SetTitle(label)
			last = label
		}
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// buildIcon returns a small icon: PNG on Linux/macOS, ICO-wrapped PNG on Windows.
func buildIcon() []byte {
	pngBytes := makePNG()
	if runtime.GOOS == "windows" {
		return wrapICO(pngBytes, 16)
	}
	return pngBytes
}

// makePNG renders a 16x16 hub-and-spoke node graph (central hub with four
// connected outer nodes) in OperatorLM blue on a transparent background.
// Communicates the core idea: one local proxy multiplexing many LLM providers.
func makePNG() []byte {
	const size = 16
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	primary := color.RGBA{R: 0x4f, G: 0x8a, B: 0xff, A: 0xff}
	pattern := []string{
		"................",
		".##..........##.",
		".##..........##.",
		"...#........#...",
		"....#......#....",
		".....#....#.....",
		"......####......",
		"......####......",
		"......####......",
		"......####......",
		".....#....#.....",
		"....#......#....",
		"...#........#...",
		".##..........##.",
		".##..........##.",
		"................",
	}
	for y, row := range pattern {
		for x, ch := range row {
			if ch == '#' {
				img.Set(x, y, primary)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// wrapICO wraps a PNG inside a single-image ICO container (Vista+ supports PNG payloads).
func wrapICO(pngData []byte, dim int) []byte {
	var buf bytes.Buffer
	// ICONDIR
	binary.Write(&buf, binary.LittleEndian, uint16(0))    // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))    // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(1))    // count

	d := byte(dim)
	if dim >= 256 {
		d = 0
	}
	// ICONDIRENTRY (16 bytes)
	buf.WriteByte(d)                                                 // width
	buf.WriteByte(d)                                                 // height
	buf.WriteByte(0)                                                 // colors in palette
	buf.WriteByte(0)                                                 // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))               // color planes
	binary.Write(&buf, binary.LittleEndian, uint16(32))              // bits per pixel
	binary.Write(&buf, binary.LittleEndian, uint32(len(pngData)))    // image size
	binary.Write(&buf, binary.LittleEndian, uint32(6+16))            // offset of image data

	buf.Write(pngData)
	return buf.Bytes()
}
