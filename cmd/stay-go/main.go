// stay-go is a declarative system configuration manager.
// It reads a YAML desired-state config, compares it against the live system
// and persisted state, then presents a plan of changes for the user to approve.
//
// Usage:
//
//	stay-go [flags]
//
// Flags:
//
//	-c, --config   path to config file (default: config/default.yaml)
//	-d, --debug    stream all command output to the terminal
//	-n, --dry-run  print the plan without executing
//	    --state    path to state file (default: ~/.local/share/stay-go/state.json)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/resource/commands"
	"github.com/rayben/stay-go/internal/resource/groups"
	"github.com/rayben/stay-go/internal/resource/packages"
	rsecrets "github.com/rayben/stay-go/internal/resource/secrets"
	"github.com/rayben/stay-go/internal/resource/scripts"
	"github.com/rayben/stay-go/internal/resource/services"
	"github.com/rayben/stay-go/internal/resource/users"
	"github.com/rayben/stay-go/internal/state"
)

// showFlag is a custom flag.Value that accepts --show (all) or --show=scope.
// IsBoolFlag lets the flag package accept --show without a value; Set receives
// "true" in that case, which we normalise to "all".
type showFlag struct{ scope string }

func (s *showFlag) String() string   { return s.scope }
func (s *showFlag) IsBoolFlag() bool { return true }
func (s *showFlag) Set(v string) error {
	if v == "true" {
		s.scope = "all"
	} else {
		s.scope = v
	}
	return nil
}

func main() {
	// Flags.
	var (
		configDir string
		statePath string
		debug     bool
		dryRun    bool
		show      showFlag
	)

	flag.StringVar(&configDir, "config", "config", "path to config directory")
	flag.StringVar(&configDir, "c", "config", "path to config directory (shorthand)")
	flag.StringVar(&statePath, "state", state.DefaultPath(), "path to state JSON file")
	flag.BoolVar(&debug, "debug", false, "stream all command output to the terminal")
	flag.BoolVar(&debug, "d", false, "stream all command output (shorthand)")
	flag.Var(&show, "show", "print tracked state without executing: --show (all), --show=packages|groups|users|services|variables")
	flag.BoolVar(&dryRun, "dry-run", false, "show plan without executing")
	flag.BoolVar(&dryRun, "n", false, "show plan without executing (shorthand)")

	flag.Usage = usage
	flag.Parse()

	// Resolve current user and hostname for config layer discovery.
	username := currentUsername()
	hostname, _ := os.Hostname()

	// Load and merge config from all applicable layers.
	cfg, err := config.LoadAll(configDir, username, hostname)
	if err != nil {
		fatalf("config: %v", err)
	}

	// Load (or initialise) state.
	st, err := state.Load(statePath)
	if err != nil {
		fatalf("state: %v", err)
	}

	// --show: print tracking info and exit without executing.
	if show.scope != "" {
		engine.DisplayShow(os.Stdout, st, cfg.Vars, show.scope)
		return
	}

	// Shared process executor.
	exec := &executor.Executor{Debug: debug}

	// Build engine and register resources in canonical order.
	// The registration order controls the display grouping in the plan.
	opts := engine.Options{
		ConfigPath: configDir,
		StatePath:  statePath,
		Debug:      debug,
		DryRun:     dryRun,
	}
	eng := engine.New(opts, exec)
	eng.Register(packages.New(cfg, exec))
	eng.Register(groups.New(cfg, exec))
	eng.Register(users.New(cfg, exec))
	eng.Register(services.New(cfg, exec))
	eng.Register(scripts.New(cfg, exec))
	eng.Register(rsecrets.New(cfg))
	eng.Register(commands.New(cfg, exec))

	// Respect Ctrl-C / SIGTERM for clean shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := eng.Run(ctx, st); err != nil {
		fatalf("%v", err)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `stay-go — declarative system configuration manager

Usage:
  stay-go [flags]

Flags:
  -c, --config string   path to config directory (default "config")
      --state  string   path to state file (default %q)
  -d, --debug [scope]   stream command output; scope: all (default), users,
                        packages, groups, services, variables
  -n, --dry-run         show plan without executing

Config is loaded from three layers inside the config directory:
  config/default.yaml           common (all hosts and users)
  config/hosts/<hostname>.yaml  host-specific overrides
  config/users/<username>.yaml  user-specific overrides

Higher-specificity layers override common entries with the same name.
Each entry is tagged with its source level and tracked accordingly.
`, state.DefaultPath())
}

// currentUsername returns the current user's login name, falling back to the
// USER environment variable, then "unknown".
func currentUsername() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "stay-go: "+format+"\n", args...)
	os.Exit(1)
}
