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
//	    --version  print build version (YYYYMMDD.nn when built via make)
//	    --state    path to state file (default: ~/.local/share/stay-go/state.json)
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	rcontainers "github.com/rayben/stay-go/internal/resource/containers"
	rdistrobox "github.com/rayben/stay-go/internal/resource/distrobox"
	rfiles "github.com/rayben/stay-go/internal/resource/files"
	"github.com/rayben/stay-go/internal/resource/groups"
	rjson "github.com/rayben/stay-go/internal/resource/json"
	"github.com/rayben/stay-go/internal/resource/packages"
	"github.com/rayben/stay-go/internal/resource/scripts"
	rsecrets "github.com/rayben/stay-go/internal/resource/secrets"
	"github.com/rayben/stay-go/internal/resource/services"
	"github.com/rayben/stay-go/internal/resource/users"
	"github.com/rayben/stay-go/internal/state"
)

// version is set at link time by the Makefile (YYYYMMDD.nn). Default "dev" when
// built with plain `go build` without -ldflags.
var version = "dev"

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
		configDir      string
		statePath      string
		debug          bool
		verbose        bool
		dryRun         bool
		autoYes        bool
		quietPlan      bool
		guestKnowledge bool
		show           showFlag
		showVersion    bool
	)

	flag.StringVar(&configDir, "config", "config", "path to config directory")
	flag.StringVar(&configDir, "c", "config", "path to config directory (shorthand)")
	flag.StringVar(&statePath, "state", state.DefaultPath(), "path to state JSON file")
	flag.BoolVar(&debug, "debug", false, "stream all command output to the terminal (truncates >5 lines)")
	flag.BoolVar(&debug, "d", false, "stream all command output (shorthand)")
	flag.BoolVar(&verbose, "verbose", false, "with --debug, stream all output without truncation")
	flag.BoolVar(&verbose, "v", false, "with --debug, stream all output without truncation (shorthand)")
	flag.Var(&show, "show", "print tracked state without executing: --show (all), --show=packages|groups|users|services|variables")
	flag.BoolVar(&dryRun, "dry-run", false, "show plan without executing")
	flag.BoolVar(&dryRun, "n", false, "show plan without executing (shorthand)")
	flag.BoolVar(&autoYes, "yes", false, "auto-confirm execution plan without prompting")
	flag.BoolVar(&autoYes, "y", false, "auto-confirm (shorthand)")
	flag.BoolVar(&quietPlan, "quiet-plan", false, "suppress plan table, summary, and final report (used internally by distrobox resource)")
	flag.BoolVar(&guestKnowledge, "guest-knowledge", false, "gather installed-package knowledge and output as JSON (internal: used by distrobox resource)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")

	flag.Usage = usage
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Load and merge config. Host/user layers are declared as !include directives
	// in default.yaml using ${env:USER} and $(hostname) expressions.
	cfg, err := config.LoadAll(configDir)
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
	exec := &executor.Executor{Debug: debug, Verbose: verbose}

	// --guest-knowledge: gather what packages are installed (runs inside a distrobox).
	// Outputs a JSON map of nodeID → true and exits. Used internally by the distrobox resource.
	if guestKnowledge {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		entries, err := packages.New(cfg, exec).GatherKnowledge(ctx)
		if err != nil {
			fatalf("guest-knowledge: %v", err)
		}
		knowledge := make(map[string]bool, len(entries))
		for _, e := range entries {
			knowledge[e.ID] = true
		}
		if err := json.NewEncoder(os.Stdout).Encode(knowledge); err != nil {
			fatalf("guest-knowledge encode: %v", err)
		}
		return
	}

	// Build engine and register resources in canonical order.
	// The registration order controls the display grouping in the plan.
	opts := engine.Options{
		ConfigPath: configDir,
		StatePath:  statePath,
		Debug:      debug,
		DryRun:     dryRun,
		AutoYes:    autoYes,
		QuietPlan:  quietPlan,
	}
	eng := engine.New(opts, exec)
	eng.Register(packages.New(cfg, exec))
	eng.Register(groups.New(cfg, exec))
	eng.Register(users.New(cfg, exec))
	eng.Register(services.New(cfg, exec))
	eng.Register(scripts.New(cfg, exec))
	eng.Register(rfiles.New(cfg, exec))
	eng.Register(rsecrets.New(cfg))
	eng.Register(commands.New(cfg, exec))
	eng.Register(rcontainers.New(cfg, exec))
	eng.Register(rdistrobox.New(cfg, exec))
	eng.Register(rjson.New(cfg, exec))

	// Respect Ctrl-C / SIGTERM for clean shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := eng.Run(ctx, st); err != nil {
		if !errors.Is(err, engine.ErrExecutionFailed) {
			fatalf("%v", err)
		}
		os.Exit(1) // failures already reported by the engine's final report
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `stay-go %s — declarative system configuration manager

Usage:
  stay-go [flags]

Flags:
  -c, --config string   path to config directory (default "config")
      --state  string   path to state file (default %q)
  -d, --debug           stream command output; outputs >5 lines are truncated
      --verbose, -v     with --debug: stream all output without truncation
  -n, --dry-run         show plan without executing
      --version         print build version and exit

Config is loaded from three layers inside the config directory:
  config/default.yaml           common (all hosts and users)
  config/hosts/<hostname>.yaml  host-specific overrides
  config/users/<username>.yaml  user-specific overrides

Higher-specificity layers override common entries with the same name.
Each entry is tagged with its source level and tracked accordingly.
`, version, state.DefaultPath())
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
