package distrobox

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/rayben/stay-go/internal/config"
	"github.com/rayben/stay-go/internal/engine"
	"github.com/rayben/stay-go/internal/executor"
	"github.com/rayben/stay-go/internal/resource/commands"
	"github.com/rayben/stay-go/internal/state"
)

// guestPkgKnowledge runs stay-go inside the box with --guest-knowledge and
// returns (map, true) on success. On failure the map is nil and ok is false.
func (r *Resource) guestPkgKnowledge(ctx context.Context, entry *config.DistroboxEntry, guestBin string, guestBinErr error) (map[string]bool, bool) {
	if len(entry.Packages) == 0 {
		return nil, true
	}
	if guestBinErr != nil || guestBin == "" {
		return nil, false
	}
	if err := r.writeBoxConfig(entry); err != nil {
		return nil, false
	}
	result, err := r.exec.Run(ctx, executor.Options{},
		"distrobox", "enter", "-n", entry.Name, "--",
		guestBin, "--guest-knowledge", "-c", r.boxConfigDir(entry.Name),
	)
	if err != nil || result.ExitCode != 0 {
		return nil, false
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			var m map[string]bool
			if json.Unmarshal([]byte(line), &m) == nil {
				return m, true
			}
		}
	}
	return nil, false
}

type guestDelta struct {
	kind   string // "packages" or "commands"
	name   string
	action engine.ActionType
}

func deltaOrder(a engine.ActionType) int {
	switch a {
	case engine.ActionAdd, engine.ActionAdopt:
		return 0
	case engine.ActionUpdate:
		return 1
	case engine.ActionLevel:
		return 2
	case engine.ActionRemove:
		return 3
	case engine.ActionSkip:
		return 4
	default:
		return 9
	}
}

func sortGuestDeltas(d []guestDelta) {
	sort.Slice(d, func(i, j int) bool {
		if d[i].kind != d[j].kind {
			return d[i].kind < d[j].kind
		}
		oi, oj := deltaOrder(d[i].action), deltaOrder(d[j].action)
		if oi != oj {
			return oi < oj
		}
		return d[i].name < d[j].name
	})
}

func commandConfigHash(c *config.CommandEntry) string {
	return config.Hash(struct {
		Name     string
		Sudo     bool
		Command  string
		Rollback string
	}{c.Name, c.Sudo, c.Command, c.Rollback})
}

// guestWorkDeltas returns per-item actions as if stay-go planned inside the box
// (packages use guest inventory when ok; commands use per-box state only).
func (r *Resource) guestWorkDeltas(entry *config.DistroboxEntry, guestPkg map[string]bool, guestPkgOK bool, boxSt *state.State) []guestDelta {
	var out []guestDelta

	pendingPkgRem := make(map[string]bool)
	for _, p := range entry.Packages {
		if !p.Remove {
			continue
		}
		id := config.NodeID("packages", p.Name)
		if guestPkgOK && guestPkg[id] {
			pendingPkgRem[p.Name] = true
			out = append(out, guestDelta{kind: "packages", name: p.Name, action: engine.ActionRemove})
		}
	}

	installNames := make(map[string]bool)
	for _, p := range entry.Packages {
		if !p.Remove {
			installNames[p.Name] = true
		}
	}

	for _, p := range entry.Packages {
		if p.Remove {
			continue
		}
		id := config.NodeID("packages", p.Name)
		hash := config.Hash(p.Name)
		inK := true
		if guestPkgOK {
			inK = guestPkg[id]
		}
		act := engine.DetermineAction(id, inK, hash, boxSt)
		level := p.Level
		if level == "" {
			level = "common"
		}
		act, _ = engine.CheckLevelChange(id, level, act, boxSt)
		if act == engine.ActionTrack {
			continue
		}
		out = append(out, guestDelta{kind: "packages", name: p.Name, action: act})
	}

	for _, n := range engine.StateRemovals("packages", installNames, boxSt) {
		if pendingPkgRem[n.DisplayName] {
			continue
		}
		out = append(out, guestDelta{kind: "packages", name: n.DisplayName, action: engine.ActionRemove})
	}

	cmdNames := make(map[string]bool, len(entry.Commands))
	for i := range entry.Commands {
		c := &entry.Commands[i]
		cmdNames[c.Name] = true
		id := config.NodeID("commands", c.Name)
		if reason := commands.FileConditionSkipReason(c); reason != "" {
			out = append(out, guestDelta{kind: "commands", name: c.Name, action: engine.ActionSkip})
			continue
		}
		hash := commandConfigHash(c)
		act := engine.DetermineAction(id, true, hash, boxSt)
		if act == engine.ActionAdopt {
			act = engine.ActionAdd
		}
		level := c.Level
		if level == "" {
			level = "common"
		}
		act, _ = engine.CheckLevelChange(id, level, act, boxSt)
		if act == engine.ActionTrack {
			continue
		}
		out = append(out, guestDelta{kind: "commands", name: c.Name, action: act})
	}
	for _, n := range engine.StateRemovals("commands", cmdNames, boxSt) {
		out = append(out, guestDelta{kind: "commands", name: n.DisplayName, action: engine.ActionRemove})
	}

	sortGuestDeltas(out)
	return out
}

// guestWorkPending reports whether any package or command still needs applying
// inside the box (ignores skip tokens).
func guestWorkPending(deltas []guestDelta) bool {
	for _, d := range deltas {
		switch d.action {
		case engine.ActionAdd, engine.ActionAdopt, engine.ActionUpdate, engine.ActionRemove, engine.ActionLevel:
			return true
		}
	}
	return false
}

// formatGuestWorkNotes turns deltas into coloured continuation lines.
// includeExports is true when the apply node will run guest stay-go (ADD/UPDATE).
// exportAction is ActionAdd for first-time apply and ActionUpdate when re-applying.
func formatGuestWorkNotes(entry *config.DistroboxEntry, deltas []guestDelta, guestPkgOK bool, includeExports bool, exportAction engine.ActionType) []string {
	var notes []string
	if len(entry.Packages) > 0 && !guestPkgOK {
		notes = append(notes, "packages: guest inventory unavailable (distrobox enter or guest binary failed)")
	}
	var pkgTok, cmdTok []string
	for _, d := range deltas {
		tok := engine.ColoredPlanDelta(d.action, d.name)
		switch d.kind {
		case "commands":
			cmdTok = append(cmdTok, tok)
		default:
			pkgTok = append(pkgTok, tok)
		}
	}
	if len(pkgTok) > 0 {
		notes = append(notes, "packages: "+strings.Join(pkgTok, " "))
	}
	if len(cmdTok) > 0 {
		notes = append(notes, "commands: "+strings.Join(cmdTok, " "))
	}
	if includeExports && len(entry.Exports) > 0 {
		var expTok []string
		for _, app := range entry.Exports {
			expTok = append(expTok, engine.ColoredPlanDelta(exportAction, app))
		}
		notes = append(notes, "exports: "+strings.Join(expTok, " "))
	}
	if len(notes) == 0 {
		return nil
	}
	return notes
}
