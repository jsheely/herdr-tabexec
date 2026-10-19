# herdr-tabexec

A [Herdr](https://herdr.dev) plugin, written in Go, that maps tab labels to
executables:

- **Name a tab and its program starts in it.** Configure any number of
  `[[tools]]` blocks, each with its own tab labels and command (for example
  `git` → `lazygit`, `mon` → `btop`). Works when you rename an existing tab or
  create one with a matching label (`herdr tab create --label git`).
- **Survives a Herdr restart.** When Herdr restores your session, every
  matching tab gets its program relaunched, the same way Herdr relaunches
  agents.
- **Pull / push from wherever you are.** Two actions, `pull` and `push`, run
  `git pull` / `git push` in the folder of the focused pane and show the output
  in a popup. Bind them to `prefix+p` / `prefix+shift+p` (see below).
- **Quick popup.** The `popup` action opens the focused tab's program (or the
  first configured one) in a popup pane in the focused pane's folder. Quit the
  program and the popup closes. Bind it to `prefix+ctrl+g`.

## Requirements

- Herdr 0.8.0 or newer (Linux, macOS, Windows).
- The executables you configure, and `git`, on your `PATH`.
- Go 1.22 or newer at install time (the plugin is compiled during install;
  it has no third-party dependencies so the build works offline).

## Install

```sh
herdr plugin install jsheely/herdr-tabexec
```

For local development, build once and link the checkout in place:

```sh
go build -o bin/herdr-tabexec.exe .   # Windows
go build -o bin/herdr-tabexec .       # Linux / macOS
herdr plugin link /path/to/herdr-tabexec
```

## Keybindings

Herdr plugins cannot register keys themselves, so add these to your Herdr
config (`herdr --help` prints its path; on Windows it is
`%APPDATA%\herdr\config.toml`). By default Herdr already uses `prefix+p` for
`previous_tab` and `prefix+shift+p` for `rename_pane`, so free them first or
pick other keys. `prefix+ctrl+g` is unused by default.

```toml
[keys]
previous_tab = ""   # was prefix+p
rename_pane = ""    # was prefix+shift+p

[[keys.command]]
key = "prefix+p"
type = "plugin_action"
command = "jsheely.tabexec.pull"

[[keys.command]]
key = "prefix+shift+p"
type = "plugin_action"
command = "jsheely.tabexec.push"

[[keys.command]]
key = "prefix+ctrl+g"
type = "plugin_action"
command = "jsheely.tabexec.popup"
```

Then run `herdr server reload-config` (or press the reload binding).

Optional extras:

```toml
[[keys.command]]
key = "prefix+alt+g"
type = "plugin_action"
command = "jsheely.tabexec.open"      # start the tab's tool in the focused pane

[[keys.command]]
key = "prefix+alt+shift+g"
type = "plugin_action"
command = "jsheely.tabexec.restore"   # relaunch tools in every matching tab
```

## Configuration

`herdr plugin config-dir jsheely.tabexec` prints the config folder. A
`config.toml` with these defaults is created there on first run:

```toml
# Commands run by the pull / push actions, in the focused pane's folder.
pull_command = "git pull"
push_command = "git push"

# Where pull/push output goes: "popup" (a Herdr popup pane) or "notification".
output = "popup"

# Seconds the popup stays open after a successful pull/push (0 = wait for Enter).
popup_close_delay = 2

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
```

Nothing launches until you add at least one `[[tools]]` block. Each `[[tools]]` block needs `tab_names` and `command`. Blocks are checked in
order and the first one whose `tab_names` contains the tab label
(case-insensitive) wins.

`detect` (optional) lists text that marks the program as already running when
found in the pane's foreground process names or terminal title
(case-insensitive). It defaults to the executable's name. On Windows Herdr
reports only the shell as the foreground process, so detection relies on the
terminal title there: if your program does not put its name in the title, set
`detect` to something it does show, or rely on the ten-second launch debounce.

Pull/push failures always keep the popup open until you press Enter so you can
read the error. Commands are split shell-style (quotes supported) and executed
directly, without a shell, so aliases and shell functions will not resolve.

## How it works

| Manifest hook | What the binary does |
| --- | --- |
| `[[events]] on = "tab.created"` / `"tab.renamed"` | If the new label matches a `[[tools]]` block, types its `command` + Enter into the tab's pane via `herdr pane run`. |
| `[[startup]]` | After Herdr restores the session, takes one `herdr api snapshot`, finds every matching tab, and launches its tool in the tab's first agent-free pane. |
| `[[actions]] pull` / `push` | Reads the focused pane's cwd from `HERDR_PLUGIN_CONTEXT_JSON`, then opens the plugin's `git-op` popup pane which runs the git command in that folder. |
| `[[panes]] git-op` | The popup body. Runs the command with the terminal attached so progress output and credential prompts work. |
| `[[actions]] popup` + `[[panes]] tool` | Opens a 90% popup pane and runs the focused tab's tool (or the first one) in the focused pane's folder. The popup closes when the program exits. |

Before launching, the plugin skips panes that host a detected agent (so it
never types into Claude or Codex), panes where the program is already running,
and panes it launched within the last ten seconds.

The plugin talks to Herdr exclusively through the `herdr` CLI at
`HERDR_BIN_PATH`, so it works identically over the Unix socket and the Windows
named pipe.

## Troubleshooting

```sh
herdr plugin log list --plugin jsheely.tabexec     # stdout/stderr of every hook run
herdr plugin action invoke restore --plugin jsheely.tabexec
```

If the program never appears after renaming a tab, check that the tab's pane is
a plain shell prompt: `herdr pane run` types into whatever is in the
foreground.

## License

MIT
