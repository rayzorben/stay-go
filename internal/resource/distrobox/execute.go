package distrobox

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/engine"
	"github.com/rayzorben/stay-go/internal/executor"
	"github.com/rayzorben/stay-go/internal/state"
)

// Execute creates, removes, or applies in-box changes for a distrobox node.
// Implements engine.NodeExecutor.
func (r *Resource) Execute(ctx context.Context, node *engine.PlanNode, st *state.State) error {
	entry, ok := r.nodeConfigs[node.ID]
	if !ok {
		return fmt.Errorf("no config found for distrobox node %q", node.DisplayName)
	}

	if node.Action == engine.ActionUpgrade {
		return r.executeUpgrade(ctx, entry.Name)
	}
	if isApplyNode(node.ID) {
		return r.executeApply(ctx, node, entry, st)
	}
	return r.executeBox(ctx, node, entry, st)
}

// executeBox manages the host-level container lifecycle.
func (r *Resource) executeBox(ctx context.Context, node *engine.PlanNode, entry *config.DistroboxEntry, st *state.State) error {
	switch node.Action {
	case engine.ActionAdd, engine.ActionUpdate:
		// For UPDATE: the box config changed (e.g. image). Remove the old container
		// first so distrobox create starts fresh.
		if node.Action == engine.ActionUpdate {
			r.exec.Run(ctx, executor.Options{}, "distrobox", "rm", "--yes", entry.Name) //nolint:errcheck
		}

		// home_sudo: if the custom home directory is outside the user's home
		// directory, create it with sudo and chown it to the current user.
		// distrobox create itself always runs as the current user.
		if entry.HomeSudo && entry.Home != "" {
			if err := r.prepareExternalHome(ctx, entry.Home); err != nil {
				return fmt.Errorf("preparing home dir for %q: %w", entry.Name, err)
			}
		}

		args := buildCreateArgs(entry)
		result, err := r.exec.Run(ctx, executor.Options{}, "distrobox", args...)
		if err != nil {
			return fmt.Errorf("creating distrobox %q: %w\nstderr: %s", entry.Name, err, result.Stderr)
		}
		// A new or recreated container has a fresh root; drop any per-box state
		// from a prior container (e.g. manual distrobox rm) so guest stay-go does
		// not skip packages/commands that were recorded on the host only.
		os.RemoveAll(r.boxStateDir(entry.Name)) //nolint:errcheck
		st.Set(node.ID, node.ConfigHash, node.SourceFile, node.StateData)

	case engine.ActionRemove:
		result, err := r.exec.Run(ctx, executor.Options{}, "distrobox", "rm", "--yes", entry.Name)
		if err != nil {
			return fmt.Errorf("removing distrobox %q: %w\nstderr: %s", entry.Name, err, result.Stderr)
		}
		st.Delete(node.ID)
		// Remove the apply sub-node from state too (if it was ever tracked).
		st.Delete(applyNodeID(entry.Name))
		// Clean up per-box state directory (sub-config + state JSON).
		os.RemoveAll(r.boxStateDir(entry.Name)) //nolint:errcheck
	}
	return nil
}

// prepareExternalHome creates a home directory outside the user's home using
// sudo, then chowns it to the current user so distrobox can use it without
// elevated privileges.
func (r *Resource) prepareExternalHome(ctx context.Context, home string) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}
	// Resolve ~ in home path (already done by vars, but be defensive).
	if home == "~" || home == "" {
		return nil
	}
	result, err := r.exec.Run(ctx, executor.Options{Sudo: true}, "mkdir", "-p", home)
	if err != nil {
		return fmt.Errorf("mkdir -p %q: %w\nstderr: %s", home, err, result.Stderr)
	}
	owner := u.Username
	if u.Gid != "" {
		owner = u.Username + ":" + u.Username
	}
	result, err = r.exec.Run(ctx, executor.Options{Sudo: true}, "chown", owner, home)
	if err != nil {
		return fmt.Errorf("chown %q %q: %w\nstderr: %s", owner, home, err, result.Stderr)
	}
	// Also ensure the parent path up to the root is accessible.
	parent := filepath.Dir(home)
	if parent != "." && parent != "/" {
		r.exec.Run(ctx, executor.Options{Sudo: true}, "chmod", "a+x", parent) //nolint:errcheck
	}
	return nil
}

// executeApply runs stay-go inside the box to apply in-box packages and
// commands, then exports any declared desktop apps to the host.
func (r *Resource) executeApply(ctx context.Context, node *engine.PlanNode, entry *config.DistroboxEntry, st *state.State) error {
	// TRACK/ADOPT/LEVEL nodes require no execution — state is updated by the engine.
	if node.Action == engine.ActionTrack || node.Action == engine.ActionAdopt || node.Action == engine.ActionLevel {
		return nil
	}

	// 1. Inject the host binary into the shared home so the box can run it.
	guestBin, err := r.ensureGuestBinary()
	if err != nil {
		return fmt.Errorf("injecting guest binary: %w", err)
	}

	// 2. Write the sub-config that the guest stay-go will load.
	if err := r.writeBoxConfig(entry); err != nil {
		return fmt.Errorf("writing in-box config: %w", err)
	}

	// 3. Print a section header so the user knows which box is being applied.
	//    Clear the progress line (written with \r by the engine) first.
	const sepWidth = 76
	header := fmt.Sprintf("── applying in [%s] ", entry.Name)
	if len(header) < sepWidth {
		header += strings.Repeat("─", sepWidth-len(header))
	}
	fmt.Fprintf(os.Stdout, "\r\033[K%s\n", header)

	// 4. Run stay-go inside the box with --yes and --quiet-plan so it applies
	//    without an interactive prompt and streams execution rows only.
	//    Forward --debug so in-box command output is visible when debugging.
	guestArgs := []string{"--yes", "--quiet-plan",
		"-c", r.boxConfigDir(entry.Name),
		"--state", r.boxStatePath(entry.Name),
	}
	if r.exec.Debug {
		guestArgs = append(guestArgs, "--debug")
	}
	if r.exec.Verbose {
		guestArgs = append(guestArgs, "--verbose")
	}
	inBoxArgs := append([]string{"enter", "-n", entry.Name, "--", guestBin}, guestArgs...)
	// AllowInteractive allocates a PTY for the distrobox process so that sudo
	// inside the container can open /dev/tty and prompt for a password.
	// Without a PTY, the subprocess has no controlling terminal and sudo fails
	// with "a terminal is required". Falls back to Stream when stdin is not a
	// TTY (e.g. CI), which is the correct behaviour in non-interactive contexts.
	result, err := r.exec.Run(ctx, executor.Options{Stream: true, AllowInteractive: true}, "distrobox", inBoxArgs...)
	fmt.Fprintln(os.Stdout, strings.Repeat("─", sepWidth))
	if err != nil {
		return fmt.Errorf("applying in-box config for %q: %w\nstderr: %s", entry.Name, err, result.Stderr)
	}

	// 5. Export declared apps from inside the box to the host desktop.
	for _, app := range entry.Exports {
		r.exec.Run(ctx, executor.Options{}, //nolint:errcheck
			"distrobox", "enter", "-n", entry.Name, "--",
			"distrobox-export", "--app", app,
		)
	}

	data := map[string]interface{}{
		"name": entry.Name,
	}
	if len(entry.Exports) > 0 {
		data["exports_snapshot"] = sortedStringsCopy(entry.Exports)
	}
	st.Set(node.ID, node.ConfigHash, node.SourceFile, data)
	return nil
}
