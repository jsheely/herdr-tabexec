// herdr-tabexec is a Herdr plugin binary. Herdr invokes it with one of:
//
//	startup   [[startup]] hook: relaunch the command in every matching tab
//	event     [[events]] hook for tab.created / tab.renamed
//	action        [[actions]] hook; HERDR_PLUGIN_ACTION_ID selects pull/push/open/restore
//	git-pane      body of the popup pane that shows git pull/push output
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: herdr-tabexec startup|event|action|git-pane")
		os.Exit(2)
	}
	cfg := loadConfig()

	var err error
	switch os.Args[1] {
	case "startup":
		err = restoreAll(cfg, "startup")
	case "event":
		err = handleEvent(cfg)
	case "action":
		err = handleAction(cfg)
	case "git-pane":
		err = runGitPane(cfg)
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		logf("%v", err)
		os.Exit(1)
	}
}

// restoreAll scans the whole session and launches the command in every tab whose
// label matches. Used by the startup hook and the `restore` action.
func restoreAll(cfg Config, reason string) error {
	tabs, panes, err := sessionSnapshot()
	if err != nil {
		return err
	}
	var errs []error
	matched := 0
	for _, tab := range tabs {
		tool, ok := cfg.toolForTab(tab.Label)
		if !ok {
			continue
		}
		matched++
		if err := ensureToolForTab(tool, tab, panes, reason); err != nil {
			errs = append(errs, err)
		}
	}
	logf("%s: %d matching tab(s) out of %d", reason, matched, len(tabs))
	return errors.Join(errs...)
}

// handleEvent reacts to tab.created and tab.renamed.
func handleEvent(cfg Config) error {
	ev, err := loadEvent()
	if err != nil {
		return err
	}
	label, tabID := ev.Data.Label, ev.Data.TabID
	if ev.Data.Tab != nil {
		label, tabID = ev.Data.Tab.Label, ev.Data.Tab.TabID
	}
	tool, ok := cfg.toolForTab(label)
	if !ok {
		return nil
	}
	reason := os.Getenv("HERDR_PLUGIN_EVENT")
	if reason == "" {
		reason = ev.Event
	}

	// Herdr hands us the tab's focused pane directly; prefer it unless it
	// hosts an agent, in which case fall through and pick another pane.
	ctx := loadContext()
	if ctx.FocusedPaneID != "" && (ctx.TabID == "" || ctx.TabID == tabID) {
		pane, err := paneGet(ctx.FocusedPaneID)
		if err == nil && pane.TabID == tabID && (pane.Agent == nil || *pane.Agent == "") {
			_, err = ensureTool(tool, pane, reason)
			return err
		}
	}
	// Otherwise look the tab up and use its first agent-free pane.
	tabs, panes, err := sessionSnapshot()
	if err != nil {
		return err
	}
	for _, tab := range tabs {
		if tab.TabID == tabID {
			return ensureToolForTab(tool, tab, panes, reason)
		}
	}
	return fmt.Errorf("%s: tab %s not found", reason, tabID)
}

// handleAction dispatches on HERDR_PLUGIN_ACTION_ID.
func handleAction(cfg Config) error {
	action := strings.TrimSpace(os.Getenv("HERDR_PLUGIN_ACTION_ID"))
	switch action {
	case "pull", "push":
		return runGitAction(cfg, action)
	case "open":
		ctx := loadContext()
		if ctx.FocusedPaneID == "" {
			return errors.New("open: no focused pane in context")
		}
		tool, ok := cfg.toolForContext(ctx)
		if !ok {
			return errors.New("open: no [[tools]] configured")
		}
		pane, err := paneGet(ctx.FocusedPaneID)
		if err != nil {
			return err
		}
		_, err = ensureTool(tool, pane, "action:open")
		return err
	case "restore":
		return restoreAll(cfg, "action:restore")
	}
	return fmt.Errorf("unknown action %q", action)
}
