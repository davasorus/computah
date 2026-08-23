// Notifications — because at local token rates a turn can take minutes, and
// the worst UX in this agent is alt-tabbing away and missing a y/N prompt.
//
// Two layers: a terminal BEL (\a — Windows Terminal flashes/badges the tab)
// and a Windows toast via powershell.exe interop, WSL's bridge to the host.
// The toast uses a NotifyIcon balloon — no modules to install, works on a
// stock Windows box. It's fired async and best-effort: a notification is
// never worth blocking the agent for, and never worth an error message.
//
// Config: "notify_sec" — a turn longer than this notifies on completion
// (default 10; 0 disables everything including approval pings). Quick turns
// stay silent: you're still looking at the terminal anyway.
package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var notifySec = 10 // config: notify_sec (0 = off)

var (
	psPathOnce sync.Once
	psPath     string // powershell.exe if reachable (WSL interop), else ""
)

func powershellPath() string {
	psPathOnce.Do(func() {
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			psPath = p
		}
	})
	return psPath
}

// bell rings the terminal (tab badge/flash in Windows Terminal).
func bell() {
	if notifySec > 0 && useTTY() {
		fmt.Print("\a")
	}
}

// toast shows a Windows balloon notification, async and best-effort.
func toast(title, body string) {
	if notifySec <= 0 || !useTTY() || powershellPath() == "" {
		return
	}
	sanitize := func(s string) string {
		s = strings.ReplaceAll(s, "'", "’")
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 120 {
			s = s[:120] + "…"
		}
		return s
	}
	script := fmt.Sprintf(`[void][System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms');`+
		`[void][System.Reflection.Assembly]::LoadWithPartialName('System.Drawing');`+
		`$n=New-Object System.Windows.Forms.NotifyIcon;`+
		`$n.Icon=[System.Drawing.SystemIcons]::Information;$n.Visible=$true;`+
		`$n.ShowBalloonTip(5000,'%s','%s',[System.Windows.Forms.ToolTipIcon]::Info);`+
		`Start-Sleep -Seconds 6;$n.Dispose()`, sanitize(title), sanitize(body))
	go func() {
		cmd := exec.Command(powershellPath(), "-NoProfile", "-NonInteractive", "-Command", script)
		done := make(chan struct{})
		go func() { _ = cmd.Run(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}()
}

// notifyApproval pings when the agent is waiting on a y/N answer.
func notifyApproval(what string) {
	bell()
	toast("agent: approval needed", what)
}

// notifyTurnDone pings when a long turn finishes.
func notifyTurnDone(elapsed time.Duration) {
	if notifySec <= 0 || elapsed < time.Duration(notifySec)*time.Second {
		return
	}
	bell()
	toast("agent: turn finished", fmt.Sprintf("took %s — ready for input", elapsed.Round(time.Second)))
}
