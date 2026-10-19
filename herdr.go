package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// herdrBin returns the Herdr CLI to call back into. Herdr injects
// HERDR_BIN_PATH into every plugin command; fall back to PATH lookup.
func herdrBin() string {
	if p := strings.TrimSpace(os.Getenv("HERDR_BIN_PATH")); p != "" {
		return p
	}
	return "herdr"
}

// herdrCall runs a Herdr CLI command and returns the `result` object of its
// JSON envelope. Server errors arrive as JSON on stderr with exit status 1.
func herdrCall(args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, herdrBin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	for _, out := range [][]byte{stdout.Bytes(), stderr.Bytes()} {
		out = bytes.TrimSpace(out)
		if len(out) == 0 {
			continue
		}
		if json.Unmarshal(out, &env) == nil {
			if env.Error != nil {
				return nil, fmt.Errorf("herdr %s: %s: %s", strings.Join(args, " "), env.Error.Code, env.Error.Message)
			}
			if env.Result != nil {
				return env.Result, nil
			}
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), runErr, strings.TrimSpace(stderr.String()))
	}
	return json.RawMessage(bytes.TrimSpace(stdout.Bytes())), nil
}

// TabInfo mirrors Herdr's TabInfo response object.
type TabInfo struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	PaneCount   int    `json:"pane_count"`
}

// PaneInfo mirrors the subset of Herdr's PaneInfo we need.
type PaneInfo struct {
	PaneID        string  `json:"pane_id"`
	TabID         string  `json:"tab_id"`
	WorkspaceID   string  `json:"workspace_id"`
	Agent         *string `json:"agent"`
	Cwd           *string `json:"cwd"`
	ForegroundCwd *string `json:"foreground_cwd"`
	TerminalTitle *string `json:"terminal_title"`
	Focused       bool    `json:"focused"`
}

// sessionSnapshot returns every tab and pane in one atomic read.
func sessionSnapshot() (tabs []TabInfo, panes []PaneInfo, err error) {
	raw, err := herdrCall("api", "snapshot")
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Snapshot struct {
			Tabs  []TabInfo  `json:"tabs"`
			Panes []PaneInfo `json:"panes"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, nil, fmt.Errorf("parse snapshot: %w", err)
	}
	return res.Snapshot.Tabs, res.Snapshot.Panes, nil
}

func paneGet(paneID string) (*PaneInfo, error) {
	raw, err := herdrCall("pane", "get", paneID)
	if err != nil {
		return nil, err
	}
	var res struct {
		Pane PaneInfo `json:"pane"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse pane: %w", err)
	}
	return &res.Pane, nil
}

// paneForegroundProcesses returns the names/argv of the pane's foreground
// processes. On Windows Herdr only reports the shell itself, so callers must
// also fall back to the terminal title.
func paneForegroundProcesses(paneID string) ([]string, error) {
	raw, err := herdrCall("pane", "process-info", "--pane", paneID)
	if err != nil {
		return nil, err
	}
	var res struct {
		ProcessInfo struct {
			Foreground []struct {
				Name  string   `json:"name"`
				Argv0 *string  `json:"argv0"`
				Argv  []string `json:"argv"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("parse process-info: %w", err)
	}
	var out []string
	for _, p := range res.ProcessInfo.Foreground {
		out = append(out, p.Name)
		if p.Argv0 != nil {
			out = append(out, *p.Argv0)
		}
		out = append(out, p.Argv...)
	}
	return out, nil
}

// paneRun types a command into the pane's shell and presses Enter.
func paneRun(paneID, command string) error {
	_, err := herdrCall("pane", "run", paneID, command)
	return err
}

func notify(title, body string) {
	args := []string{"notification", "show", title}
	if body = strings.TrimSpace(body); body != "" {
		args = append(args, "--body", body)
	}
	if _, err := herdrCall(args...); err != nil {
		logf("notification failed: %v", err)
	}
}

// openPluginPane opens one of this plugin's [[panes]] entrypoints, focused,
// with the given cwd and extra KEY=VALUE environment entries.
func openPluginPane(entrypoint, cwd string, env ...string) error {
	pluginID := os.Getenv("HERDR_PLUGIN_ID")
	if pluginID == "" {
		return errors.New("HERDR_PLUGIN_ID is not set; run this through Herdr")
	}
	args := []string{
		"plugin", "pane", "open",
		"--plugin", pluginID,
		"--entrypoint", entrypoint,
		"--cwd", cwd,
		"--env", "HERDR_TABEXEC_CWD=" + cwd,
	}
	for _, kv := range env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--focus")
	_, err := herdrCall(args...)
	return err
}

// openGitPane opens the popup pane that runs a git pull/push.
func openGitPane(op, cwd string) error {
	return openPluginPane("git-op", cwd, "HERDR_TABEXEC_OP="+op)
}

// PluginContext mirrors HERDR_PLUGIN_CONTEXT_JSON.
type PluginContext struct {
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceLabel string `json:"workspace_label"`
	WorkspaceCwd   string `json:"workspace_cwd"`
	TabID          string `json:"tab_id"`
	TabLabel       string `json:"tab_label"`
	FocusedPaneID  string `json:"focused_pane_id"`
	FocusedPaneCwd string `json:"focused_pane_cwd"`
	FocusedAgent   string `json:"focused_pane_agent"`
	Source         string `json:"invocation_source"`
}

func loadContext() PluginContext {
	var c PluginContext
	if raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			logf("invalid HERDR_PLUGIN_CONTEXT_JSON: %v", err)
		}
	}
	if c.TabID == "" {
		c.TabID = os.Getenv("HERDR_TAB_ID")
	}
	if c.FocusedPaneID == "" {
		c.FocusedPaneID = os.Getenv("HERDR_PANE_ID")
	}
	if c.WorkspaceID == "" {
		c.WorkspaceID = os.Getenv("HERDR_WORKSPACE_ID")
	}
	return c
}

// PluginEvent mirrors HERDR_PLUGIN_EVENT_JSON for tab.created / tab.renamed.
type PluginEvent struct {
	Event string `json:"event"`
	Data  struct {
		Type  string   `json:"type"`
		TabID string   `json:"tab_id"`
		Label string   `json:"label"`
		Tab   *TabInfo `json:"tab"`
	} `json:"data"`
}

func loadEvent() (PluginEvent, error) {
	var e PluginEvent
	raw := os.Getenv("HERDR_PLUGIN_EVENT_JSON")
	if raw == "" {
		return e, errors.New("HERDR_PLUGIN_EVENT_JSON is not set")
	}
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return e, fmt.Errorf("invalid HERDR_PLUGIN_EVENT_JSON: %w", err)
	}
	return e, nil
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
