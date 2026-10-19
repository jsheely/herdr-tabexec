package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// launchDebounce is how long after typing the command into a pane we refuse
// to type it again. The program needs a moment before its terminal title and
// process show up, and tab.created + tab.renamed can arrive back to back.
const launchDebounce = 10 * time.Second

// toolRunningIn reports whether the tool's command already appears to be
// running in the pane. It checks Herdr's foreground process list first
// (Linux/macOS), then the terminal title (the only signal available on
// Windows, where Herdr reports just the shell), against tool.detectNames().
func toolRunningIn(tool Tool, pane *PaneInfo) bool {
	names := tool.detectNames()
	matches := func(s string) bool {
		s = strings.ToLower(s)
		for _, n := range names {
			if n != "" && strings.Contains(s, n) {
				return true
			}
		}
		return false
	}
	if procs, err := paneForegroundProcesses(pane.PaneID); err == nil {
		for _, p := range procs {
			if matches(filepath.Base(p)) {
				return true
			}
		}
	}
	if pane.TerminalTitle != nil && matches(*pane.TerminalTitle) {
		return true
	}
	return false
}

// ensureTool launches tool.Command in the pane unless it is already running,
// hosts an agent, or was launched by us within the debounce window.
// Returns true when a launch was issued.
func ensureTool(tool Tool, pane *PaneInfo, reason string) (bool, error) {
	if pane.Agent != nil && *pane.Agent != "" {
		logf("%s: pane %s hosts agent %q; not launching command", reason, pane.PaneID, *pane.Agent)
		return false, nil
	}
	if recentlyLaunched(pane.PaneID) {
		logf("%s: pane %s launched within the last %s; skipping", reason, pane.PaneID, launchDebounce)
		return false, nil
	}
	if toolRunningIn(tool, pane) {
		logf("%s: command already running in pane %s", reason, pane.PaneID)
		return false, nil
	}
	if err := paneRun(pane.PaneID, tool.Command); err != nil {
		return false, fmt.Errorf("%s: launch in pane %s: %w", reason, pane.PaneID, err)
	}
	markLaunched(pane.PaneID)
	logf("%s: launched %q in pane %s", reason, tool.Command, pane.PaneID)
	return true, nil
}

// ensureToolForTab launches the command in the tab's first agent-free pane.
func ensureToolForTab(tool Tool, tab TabInfo, panes []PaneInfo, reason string) error {
	for i := range panes {
		p := &panes[i]
		if p.TabID != tab.TabID {
			continue
		}
		if p.Agent != nil && *p.Agent != "" {
			continue
		}
		_, err := ensureTool(tool, p, reason)
		return err
	}
	logf("%s: tab %s (%q) has no agent-free pane", reason, tab.TabID, tab.Label)
	return nil
}

// openToolPopup is the `popup` action: open the popup pane in the focused
// pane's folder, running the tool for the focused tab (or the first tool).
func openToolPopup(cfg Config) error {
	ctx := loadContext()
	tool, ok := cfg.toolForContext(ctx)
	if !ok {
		notify("popup failed", "no [[tools]] configured")
		return errors.New("popup: no tools configured")
	}
	cwd, err := resolveActionCwd(ctx)
	if err != nil {
		notify("popup failed", err.Error())
		return err
	}
	return openPluginPane("tool", cwd, "HERDR_TABEXEC_CMD="+tool.Command)
}

// runToolPane is the body of the popup pane: run HERDR_TABEXEC_CMD (falling
// back to the first tool's command) with the terminal attached, in
// HERDR_TABEXEC_CWD. The popup closes when the command exits.
func runToolPane(cfg Config) error {
	cwd := cleanPath(os.Getenv("HERDR_TABEXEC_CWD"))
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	command := strings.TrimSpace(os.Getenv("HERDR_TABEXEC_CMD"))
	if command == "" && len(cfg.Tools) > 0 {
		command = cfg.Tools[0].Command
	}
	argv, err := splitCommand(command)
	if err == nil && len(argv) == 0 {
		err = errors.New("no command configured")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		waitForEnter()
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			// the program.s own non-zero exits (e.g. declining to init a repo)
			// are not plugin errors; just let the popup close.
			return nil
		}
		fmt.Fprintf(os.Stderr, "\n%s: %v\n", command, err)
		waitForEnter()
		return err
	}
	return nil
}

// --- launch debounce markers (one file per pane in the plugin state dir) ---

func launchMarkerDir() string {
	dir := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "launched")
}

func launchMarkerPath(paneID string) string {
	// Pane IDs look like "w4:p12"; ':' is not a legal filename char on Windows.
	return filepath.Join(launchMarkerDir(), strings.ReplaceAll(paneID, ":", "_"))
}

func recentlyLaunched(paneID string) bool {
	data, err := os.ReadFile(launchMarkerPath(paneID))
	if err != nil {
		return false
	}
	unix, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(unix, 0)) < launchDebounce
}

func markLaunched(paneID string) {
	if err := os.MkdirAll(launchMarkerDir(), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(launchMarkerPath(paneID), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
	pruneLaunchMarkers()
}

// pruneLaunchMarkers deletes stale markers so the state dir does not grow.
func pruneLaunchMarkers() {
	entries, err := os.ReadDir(launchMarkerDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(launchMarkerDir(), e.Name()))
		}
	}
}
