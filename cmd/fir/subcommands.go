package main

// subcommand declares a top-level `fir <name>` subcommand. The registry below
// is the single source of truth for both dispatch (run() in app.go) and the
// usage section of `fir --help` (PrintHelp in args.go). Adding a subcommand
// is a one-line edit here — no help text drift, no missing dispatch case.
type subcommand struct {
	Name string       // first token after the binary, e.g. "observe"
	Run  func() error // handler; nil entries are help-only (e.g. extra usage lines)
	Help [][2]string  // each pair is {syntax, summary}; multiple rows allowed
}

// subcommands lists every `fir <verb>` in display order. Order here is the
// order shown in --help, so prefer logical grouping over alphabetical.
var subcommands = []subcommand{
	{
		Name: "update",
		Run:  runUpdate,
		Help: [][2]string{{"fir update", "Self-update to the latest release"}},
	},
	{
		Name: "skills",
		Run:  runSkills,
		Help: [][2]string{
			{"fir skills [list]", "List all loaded skills"},
			{"fir skills install <name>", "Install a builtin skill to project (.fir/skills/)"},
			{"", "  Options: --user (install to ~/.config/fir/skills/), --force"},
		},
	},
	{
		Name: "extensions",
		Run:  runExtensions,
		Help: [][2]string{
			{"fir extensions [list]", "List all builtin extensions"},
			{"fir extensions install <name>", "Install a builtin extension to project (.fir/extensions/)"},
			{"", "  Options: --user (install to ~/.config/fir/extensions/), --force"},
		},
	},
	{
		Name: "install",
		Run:  runInstall,
		Help: [][2]string{
			{"fir install <source> [--local]", "Install a package (git repo or local path)"},
			{"", "  --local installs to project scope (.fir/packages/)"},
		},
	},
	{
		Name: "uninstall",
		Run:  runUninstall,
		Help: [][2]string{{"fir uninstall <source> [--local]", "Remove an installed package"}},
	},
	{
		Name: "packages",
		Run:  runPackages,
		Help: [][2]string{
			{"fir packages [list]", "List installed packages"},
			{"fir packages update [source]", "Update one or all installed packages"},
		},
	},
	{
		Name: "pty",
		Run:  func() error { runPTY(); return nil },
		// pty is intentionally omitted from --help (internal/dev tool).
	},
	{
		Name: "sessions",
		Run:  runSessions,
		Help: [][2]string{{"fir sessions [list]", "List sessions associated with the current directory"}},
	},
	{
		Name: "login",
		Run:  runLoginSubcommand,
		Help: [][2]string{
			{"fir login <provider-id>", "OAuth login for a provider (auth extensions loaded)"},
			{"fir login list", "List available OAuth providers"},
		},
	},
	{
		Name: "completion",
		Run:  runCompletion,
		Help: [][2]string{{"fir completion <bash|zsh>", "Print shell completion script"}},
	},
}

// dispatchSubcommand returns the handler for os.Args[1] if it matches a
// registered subcommand, else nil.
func dispatchSubcommand(name string) func() error {
	for _, sc := range subcommands {
		if sc.Name == name {
			return sc.Run
		}
	}
	return nil
}
