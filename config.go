package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Tool maps a set of tab labels to the executable launched in those tabs.
type Tool struct {
	// Tab labels (case-insensitive, trimmed) that auto-launch Command.
	TabNames []string
	// Command typed into the tab's shell (and run by the popup action).
	Command string
	// Substrings (case-insensitive) that, found in a pane's foreground process
	// names or terminal title, mean the command is already running. Empty
	// means "the command's executable name".
	Detect []string
}

// Config is the user-editable plugin configuration, read from
// $HERDR_PLUGIN_CONFIG_DIR/config.toml. A commented default file is written
// there on first run.
type Config struct {
	// Tab label -> executable mappings, checked in order; first match wins.
	Tools []Tool
	// Commands run by the pull / push actions.
	PullCommand string
	PushCommand string
	// "popup" shows output in a Herdr popup pane; "notification" runs git in
	// the background and shows a Herdr notification with the result.
	Output string
	// Seconds the popup stays open after a successful pull/push. 0 waits for
	// Enter. Failures always wait for Enter so the error can be read.
	PopupCloseDelay float64
}

const defaultConfigTOML = `# herdr-tabexec configuration

# Commands run by the pull / push actions, in the focused pane's folder.
pull_command = "git pull"
push_command = "git push"

# Where pull/push output goes: "popup" (a Herdr popup pane) or "notification".
output = "popup"

# Seconds the popup stays open after a successful pull/push (0 = wait for Enter).
popup_close_delay = 2

# One [[tools]] block per executable. A tab whose label matches any of
# tab_names (case-insensitive) gets command typed into its shell. Blocks are
# checked in order; the first match wins.
#
# detect (optional): text that marks the command as already running when found
# in the pane's foreground process names or terminal title (case-insensitive).
# On Windows only the title is available. Defaults to the executable's name.

# No tools are configured by default. For example:
#
# [[tools]]
# tab_names = ["git"]
# command = "lazygit"
# detect = ["lazygit"]
#
# [[tools]]
# tab_names = ["mon", "top"]
# command = "btop"
`

func defaultConfig() Config {
	return Config{
		PullCommand:     "git pull",
		PushCommand:     "git push",
		Output:          "popup",
		PopupCloseDelay: 2,
	}
}

func configPath() string {
	dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// loadConfig reads the config file, creating it with defaults when missing.
// Any parse problem falls back to defaults and is reported on stderr (which
// lands in `herdr plugin log`).
func loadConfig() Config {
	cfg := defaultConfig()
	path := configPath()
	if path == "" {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, []byte(defaultConfigTOML), 0o644)
		} else {
			logf("read %s: %v", path, err)
		}
		return cfg
	}
	values, tables, err := parseSimpleTOML(string(data))
	if err != nil {
		logf("parse %s: %v (using defaults)", path, err)
		return cfg
	}
	if blocks, ok := tables["tools"]; ok {
		cfg.Tools = nil
		for i, t := range blocks {
			var tool Tool
			if v, ok := t["tab_names"].([]string); ok {
				tool.TabNames = v
			}
			if v, ok := t["command"].(string); ok {
				tool.Command = strings.TrimSpace(v)
			}
			if v, ok := t["detect"].([]string); ok {
				tool.Detect = v
			}
			if len(tool.TabNames) == 0 || tool.Command == "" {
				logf("%s: [[tools]] #%d needs tab_names and command; skipping", path, i+1)
				continue
			}
			cfg.Tools = append(cfg.Tools, tool)
		}
	}
	if v, ok := values["pull_command"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.PullCommand = v
	}
	if v, ok := values["push_command"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.PushCommand = v
	}
	if v, ok := values["output"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "popup", "notification":
			cfg.Output = strings.ToLower(strings.TrimSpace(v))
		default:
			logf("%s: unknown output %q (using %q)", path, v, cfg.Output)
		}
	}
	if v, ok := values["popup_close_delay"].(float64); ok && v >= 0 {
		cfg.PopupCloseDelay = v
	}
	return cfg
}

// toolForTab returns the first tool whose tab_names match the label.
func (c Config) toolForTab(label string) (Tool, bool) {
	l := strings.ToLower(strings.TrimSpace(label))
	if l == "" {
		return Tool{}, false
	}
	for _, t := range c.Tools {
		for _, n := range t.TabNames {
			if l == strings.ToLower(strings.TrimSpace(n)) {
				return t, true
			}
		}
	}
	return Tool{}, false
}

// toolForContext picks the tool for actions: the one matching the focused
// tab's label, else the first configured tool.
func (c Config) toolForContext(ctx PluginContext) (Tool, bool) {
	if t, ok := c.toolForTab(ctx.TabLabel); ok {
		return t, true
	}
	if len(c.Tools) > 0 {
		return c.Tools[0], true
	}
	return Tool{}, false
}

// detectNames returns the lowercase substrings that identify the running
// command: the configured `detect` list, or the command's executable name
// (without a .exe suffix).
func (t Tool) detectNames() []string {
	var names []string
	for _, n := range t.Detect {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		return names
	}
	if argv, err := splitCommand(t.Command); err == nil && len(argv) > 0 {
		exe := strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe")
		if exe != "" {
			names = append(names, exe)
		}
	}
	return names
}

// parseSimpleTOML handles the subset of TOML this plugin uses:
// `key = "string"`, `key = ["a", "b"]`, `key = 12`, `key = 1.5`, `key = true`,
// with `#` comments and blank lines, plus `[[name]]` arrays of tables whose
// entries are returned in tables[name]. Plain `[table]` headers are rejected
// so that a typo does not silently misconfigure the plugin.
func parseSimpleTOML(src string) (map[string]any, map[string][]map[string]any, error) {
	out := map[string]any{}
	tables := map[string][]map[string]any{}
	cur := out
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			name := strings.TrimSpace(line[2 : len(line)-2])
			if name == "" {
				return nil, nil, fmt.Errorf("line %d: empty table name", i+1)
			}
			cur = map[string]any{}
			tables[name] = append(tables[name], cur)
			continue
		}
		if strings.HasPrefix(line, "[") {
			return nil, nil, fmt.Errorf("line %d: only [[array]] tables are supported", i+1)
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, nil, fmt.Errorf("line %d: expected key = value", i+1)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		v, err := parseTOMLValue(val)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d (%s): %w", i+1, key, err)
		}
		cur[key] = v
	}
	return out, tables, nil
}

func stripComment(line string) string {
	inStr := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			if inStr {
				i++
			}
		case '"':
			inStr = !inStr
		case '#':
			if !inStr {
				return line[:i]
			}
		}
	}
	return line
}

func parseTOMLValue(val string) (any, error) {
	switch {
	case val == "":
		return nil, fmt.Errorf("missing value")
	case val[0] == '"':
		s, rest, err := parseTOMLString(val)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rest) != "" {
			return nil, fmt.Errorf("unexpected trailing %q", rest)
		}
		return s, nil
	case val[0] == '[':
		body := strings.TrimSpace(val[1:])
		items := []string{}
		for {
			body = strings.TrimSpace(body)
			if strings.HasPrefix(body, "]") {
				if strings.TrimSpace(body[1:]) != "" {
					return nil, fmt.Errorf("unexpected trailing %q", body[1:])
				}
				return items, nil
			}
			if body == "" {
				return nil, fmt.Errorf("unterminated array")
			}
			if body[0] != '"' {
				return nil, fmt.Errorf("only string arrays are supported")
			}
			s, rest, err := parseTOMLString(body)
			if err != nil {
				return nil, err
			}
			items = append(items, s)
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
			} else if !strings.HasPrefix(rest, "]") {
				return nil, fmt.Errorf("expected , or ] after string")
			}
			body = rest
		}
	case val == "true":
		return true, nil
	case val == "false":
		return false, nil
	default:
		f, err := strconv.ParseFloat(strings.ReplaceAll(val, "_", ""), 64)
		if err != nil {
			return nil, fmt.Errorf("unsupported value %q", val)
		}
		return f, nil
	}
}

// parseTOMLString parses a basic "..." string at the start of s and returns
// the decoded value plus the remainder.
func parseTOMLString(s string) (string, string, error) {
	if s == "" || s[0] != '"' {
		return "", "", fmt.Errorf("expected string")
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			i++
			if i >= len(s) {
				return "", "", fmt.Errorf("unterminated escape")
			}
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(s[i])
			default:
				return "", "", fmt.Errorf("unsupported escape \\%c", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("unterminated string")
}
