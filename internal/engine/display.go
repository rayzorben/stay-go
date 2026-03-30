package engine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/rayben/stay-go/internal/state"
)

// ─── ANSI escape codes ────────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// ─── Display metrics ──────────────────────────────────────────────────────────

// displayMetrics holds column widths computed by DisplayPlan and reused by the
// execution display functions for visual consistency. Defaults handle the
// QuietPlan case where DisplayPlan is never called.
var displayMetrics = struct{ nameW, typeW int }{nameW: 40, typeW: 9}

// execNameMaxCol caps the resource name column on execution rows so more terminal
// width remains for streaming output (e.g. package manager progress).
const execNameMaxCol = 50

// ─── Colour and symbol helpers ────────────────────────────────────────────────

// ColoredPlanDelta renders a single in-plan detail token with the same colours
// as the action column: + green, ~ yellow, - red, = cyan, ! dim (skip).
// Use for resource-specific continuation lines; plain TRACK nodes should not emit tokens.
func ColoredPlanDelta(action ActionType, name string) string {
	var prefix string
	switch action {
	case ActionAdd, ActionAdopt:
		prefix = "+"
	case ActionUpdate:
		prefix = "~"
	case ActionRemove:
		prefix = "-"
	case ActionLevel:
		prefix = "="
	case ActionSkip:
		return actionColor(ActionSkip)("!" + name)
	default:
		return name
	}
	return actionColor(action)(prefix + name)
}

// actionColor wraps text in the ANSI colour for the given action.
func actionColor(a ActionType) func(string) string {
	switch a {
	case ActionAdd, ActionAdopt:
		return func(s string) string { return ansiGreen + s + ansiReset }
	case ActionRemove:
		return func(s string) string { return ansiRed + s + ansiReset }
	case ActionUpdate:
		return func(s string) string { return ansiYellow + s + ansiReset }
	case ActionLevel:
		return func(s string) string { return ansiCyan + s + ansiReset }
	case ActionSkip:
		return func(s string) string { return ansiDim + s + ansiReset }
	default:
		return func(s string) string { return s }
	}
}

// ─── Resource type labels ─────────────────────────────────────────────────────

var resourceLabel = map[string]string{
	"packages":   "package",
	"groups":     "group",
	"users":      "user",
	"services":   "service",
	"scripts":    "script",
	"files":      "file",
	"commands":   "command",
	"secrets":    "secret",
	"containers": "container",
	"distrobox":  "distrobox",
	"json":       "json",
}

func typeLabel(resourceType string) string {
	if l, ok := resourceLabel[resourceType]; ok {
		return l
	}
	return resourceType
}

// ─── Canonical resource order ─────────────────────────────────────────────────

// canonicalOrder defines the display order for resource types.
var canonicalOrder = []string{
	"packages", "groups", "users", "services", "scripts",
	"files", "commands", "secrets", "containers", "distrobox", "json",
}

var canonicalIndex = func() map[string]int {
	m := make(map[string]int, len(canonicalOrder))
	for i, t := range canonicalOrder {
		m[t] = i
	}
	return m
}()

// ─── Action groups ────────────────────────────────────────────────────────────

// groupID classifies actions for stable display sort order.
type groupID int

const (
	grpAdding   groupID = 0
	grpUpdating groupID = 1
	grpMoved    groupID = 2
	grpRemoving groupID = 3
	grpSkipped  groupID = 4
)

// nodeGroupID maps an ActionType to its sort group (display order only).
// Returns (group, true) or (0, false) for hidden ActionTrack nodes.
func nodeGroupID(a ActionType) (groupID, bool) {
	switch a {
	case ActionAdd, ActionAdopt:
		return grpAdding, true
	case ActionUpdate:
		return grpUpdating, true
	case ActionLevel:
		return grpMoved, true
	case ActionRemove:
		return grpRemoving, true
	case ActionSkip:
		return grpSkipped, true
	default:
		return 0, false
	}
}

// ─── Low-level rendering primitives ──────────────────────────────────────────

// planTableHeader writes dim column titles and a light rule beneath them.
func planTableHeader(w io.Writer, lw, nw, typW, aw int) {
	hdrCell := func(label string, width int) string {
		p := fmt.Sprintf("%-*s", width, label)
		return ansiBold + ansiDim + p + ansiReset
	}
	fmt.Fprintf(w, "  %s  %s  %s  %s\n",
		hdrCell("level", lw), hdrCell("resource", nw), hdrCell("type", typW), hdrCell("action", aw))
	// ASCII '-' so column rules match header/text width on terminals that treat
	// U+2500 (─) as double-width (ambiguous East Asian width).
	fmt.Fprintf(w, "  %s%s%s  %s%s%s  %s%s%s  %s%s%s\n",
		ansiDim, strings.Repeat("-", lw), ansiReset,
		ansiDim, strings.Repeat("-", nw), ansiReset,
		ansiDim, strings.Repeat("-", typW), ansiReset,
		ansiDim, strings.Repeat("-", aw), ansiReset,
	)
}

// compactActionLabel returns a short verb for the action column (plain ASCII width).
func compactActionLabel(a ActionType) string {
	switch a {
	case ActionAdd, ActionAdopt:
		return "+ add"
	case ActionUpdate:
		return "~ update"
	case ActionLevel:
		return "= level"
	case ActionRemove:
		return "- remove"
	case ActionSkip:
		return "! skip"
	default:
		return "· …"
	}
}

// actionColumnWidth is the fixed display width reserved for compactActionLabel.
func actionColumnWidth() int {
	w := 0
	for _, a := range []ActionType{
		ActionAdd, ActionAdopt, ActionUpdate, ActionLevel, ActionRemove, ActionSkip,
	} {
		if l := len(compactActionLabel(a)); l > w {
			w = l
		}
	}
	if l := len("action"); l > w {
		w = l
	}
	return w
}

// visibleLen returns the number of terminal cells for s, excluding ANSI CSI sequences.
func visibleLen(s string) int {
	var n int
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		n++
		i += sz
	}
	return n
}

// truncateVisible truncates s so visibleLen is at most maxVis, appending "…" if trimmed.
func truncateVisible(s string, maxVis int) string {
	if maxVis < 1 {
		return ""
	}
	if visibleLen(s) <= maxVis {
		return s
	}
	target := maxVis - 1 // room for …
	var b strings.Builder
	v := 0
	for i := 0; i < len(s) && v < target; {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				b.WriteString(s[i : j+1])
				i = j + 1
				continue
			}
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		if v+1 > target {
			break
		}
		b.WriteString(s[i : i+sz])
		v++
		i += sz
	}
	b.WriteString("…")
	return b.String()
}

// planDetailLine writes a continuation aligned under the resource column.
func planDetailLine(w io.Writer, levelColW int, text string, tw int) {
	// "  " + level + "  " + "│ "
	const pipe = "│ "
	indent := 2 + levelColW + 2
	prefixVis := indent + len(pipe)
	maxText := tw - prefixVis
	if maxText < 8 {
		maxText = 8
	}
	colored := strings.Contains(text, "\x1b[")
	if colored {
		if visibleLen(text) > maxText {
			text = truncateVisible(text, maxText)
		}
		fmt.Fprintf(w, "%s%s%s%s%s\n",
			strings.Repeat(" ", indent),
			ansiDim, pipe, ansiReset,
			text,
		)
		return
	}
	if len(text) > maxText {
		text = text[:maxText-1] + "…"
	}
	fmt.Fprintf(w, "%s%s%s%s%s%s%s\n",
		strings.Repeat(" ", indent),
		ansiDim, pipe, ansiReset,
		ansiDim, text, ansiReset,
	)
}

// divider writes a full-width dim horizontal rule.
func divider(w io.Writer, tw int) {
	if tw > 2 {
		fmt.Fprintf(w, "  %s%s%s\n", ansiDim, strings.Repeat("─", tw-2), ansiReset)
	}
}

// noteLine writes dim continuation lines beneath execution errors (and plan items).
// Each line is limited to (tw − 7) runes after the 7-column prefix so a full
// 80-column terminal shows 73 characters of message; wider terminals get more.
// Multi-line messages are split on "\n"; each line is truncated rune-safe.
func noteLine(w io.Writer, text string, tw int) {
	const firstPrefix = "     └ "
	const contPrefix = "       " // 7 cols, align with text after "└ "
	const prefixCols = 7
	maxRunes := noteMaxRunes(tw, prefixCols)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		p := firstPrefix
		if i > 0 {
			p = contPrefix
		}
		line = truncateStringVisual(line, maxRunes)
		fmt.Fprintf(w, "%s%s%s%s\n", ansiDim, p, line, ansiReset)
	}
}

// noteMaxRunes returns the maximum rune count per line for text after the prefix.
func noteMaxRunes(tw, prefixCols int) int {
	maxRunes := tw - prefixCols
	if maxRunes < 1 {
		maxRunes = 1
	}
	return maxRunes
}

func truncateStringVisual(s string, maxRunes int) string {
	if maxRunes < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

// writeSummaryLine writes all action counts — including zeros — so the user can
// confirm at a glance that nothing unexpected is being added or removed.
func writeSummaryLine(w io.Writer, nodes []*PlanNode) {
	counts := make(map[ActionType]int, 8)
	for _, n := range nodes {
		counts[n.Action]++
	}
	add := counts[ActionAdd] + counts[ActionAdopt]
	upd := counts[ActionUpdate]
	mov := counts[ActionLevel]
	rem := counts[ActionRemove]
	skp := counts[ActionSkip]
	managed := counts[ActionTrack]

	fmt.Fprintf(w, "  %s+%d added%s  %s~%d updated%s  %s=%d moved%s  %s-%d removed%s  %s!%d skipped%s  %s·  %d managed%s\n",
		ansiGreen, add, ansiReset,
		ansiYellow, upd, ansiReset,
		ansiCyan, mov, ansiReset,
		ansiRed, rem, ansiReset,
		ansiDim, skp, ansiReset,
		ansiDim, managed, ansiReset,
	)
}

// formatDur formats a duration as a compact decimal-second string, e.g. "0.3s".
func formatDur(d time.Duration) string {
	s := d.Seconds()
	if s < 10 {
		return fmt.Sprintf("%.1fs", s)
	}
	return fmt.Sprintf("%.0fs", s)
}

// ─── Plan display ─────────────────────────────────────────────────────────────

// DisplayPlan writes the plan table to w and updates displayMetrics.
//
// Rows are ordered by action kind (add → update → level → remove → skip), then
// resource type and name. Columns are level | resource | type | action; extra
// detail (description, skip reason, notes) follows on indented lines under
// resource. Summary counts are printed at the bottom before the confirm prompt.
func DisplayPlan(w io.Writer, nodes []*PlanNode) {
	if len(nodes) == 0 {
		return
	}

	tw := termWidth()

	// Partition visible nodes into groups (TRACK is hidden).
	type vnode struct {
		n     *PlanNode
		grp   groupID
		typ   string
		level string
	}
	var visible []vnode
	for _, n := range nodes {
		g, ok := nodeGroupID(n.Action)
		if !ok {
			continue
		}
		lv := n.Level
		if lv == "" {
			lv = "common"
		}
		visible = append(visible, vnode{n: n, grp: g, typ: typeLabel(n.ResourceType), level: lv})
	}
	if len(visible) == 0 {
		return
	}

	// Type column width (shared with execution display for alignment).
	typColW := len("type")
	for _, v := range visible {
		if l := len(v.typ); l > typColW {
			typColW = l
		}
	}
	const maxTypeCol = 12
	if typColW > maxTypeCol {
		typColW = maxTypeCol
	}

	aw := actionColumnWidth()
	levelW := len("level")
	for _, v := range visible {
		if l := len(v.level); l > levelW {
			levelW = l
		}
	}
	const maxLevelCol = 22
	if levelW > maxLevelCol {
		levelW = maxLevelCol
	}
	// Row: "  " + level + "  " + name + "  " + type + "  " + action
	// → 8 + levelW + nw + typColW + aw
	const minNW = 12
	nw := tw - 8 - levelW - typColW - aw
	if nw < minNW {
		nw = minNW
		room := tw - 8 - nw - aw
		for levelW+typColW > room {
			if levelW > 5 {
				levelW--
				continue
			}
			if typColW > len("type") {
				typColW--
				continue
			}
			break
		}
		nw = tw - 8 - levelW - typColW - aw
		if nw < 8 {
			nw = 8
		}
	}
	displayMetrics.typeW = typColW
	displayMetrics.nameW = nw

	// Sort: by group (fixed order), then canonical resource type, then name.
	sort.SliceStable(visible, func(i, j int) bool {
		vi, vj := visible[i], visible[j]
		if vi.grp != vj.grp {
			return vi.grp < vj.grp
		}
		oi := canonicalIndex[vi.n.ResourceType]
		oj := canonicalIndex[vj.n.ResourceType]
		if oi != oj {
			return oi < oj
		}
		return vi.n.DisplayName < vj.n.DisplayName
	})

	fmt.Fprintln(w)

	planTableHeader(w, levelW, nw, typColW, aw)

	curGrp := groupID(-1)
	for _, v := range visible {
		if v.grp != curGrp {
			if curGrp != -1 {
				fmt.Fprintln(w)
			}
			curGrp = v.grp
		}

		n := v.n
		aColor := actionColor(n.Action)

		levelPlain := v.level
		if len(levelPlain) > levelW {
			levelPlain = levelPlain[:levelW-1] + "…"
		}
		levelField := fmt.Sprintf("%-*s", levelW, levelPlain)
		if strings.HasPrefix(v.level, "user") {
			levelField = ansiYellow + levelField + ansiReset
		} else {
			levelField = ansiDim + levelField + ansiReset
		}

		name := truncatePath(n.DisplayName, nw)
		nameField := fmt.Sprintf("%-*s", nw, name)

		typePlain := v.typ
		if len(typePlain) > typColW {
			typePlain = typePlain[:typColW-1] + "…"
		}
		typeField := ansiCyan + fmt.Sprintf("%-*s", typColW, typePlain) + ansiReset

		actPlain := fmt.Sprintf("%-*s", aw, compactActionLabel(n.Action))
		act := aColor(actPlain)

		fmt.Fprintf(w, "  %s  %s  %s  %s\n", levelField, nameField, typeField, act)

		switch {
		case n.Action == ActionSkip && n.SkipReason != "":
			if n.Description != "" {
				planDetailLine(w, levelW, n.Description, tw)
			}
			for _, part := range strings.Split(n.SkipReason, "; ") {
				part = strings.TrimSpace(part)
				if part != "" {
					planDetailLine(w, levelW, part, tw)
				}
			}
		case n.Description != "":
			planDetailLine(w, levelW, n.Description, tw)
		}
		for _, note := range n.Notes {
			planDetailLine(w, levelW, note, tw)
		}
	}

	fmt.Fprintln(w)
	divider(w, tw)
	fmt.Fprintln(w)
	writeSummaryLine(w, nodes)
	fmt.Fprintln(w)
}

// ─── Execution display ────────────────────────────────────────────────────────

// execNameW returns the name column width for execution rows.
// displayMetrics.nameW is set by DisplayPlan using the real terminal size.
// In guest mode (QuietPlan), DisplayPlan is never called, so it stays at the
// conservative default (40) which fits safely in an 80-column terminal.
func execNameW() int {
	return displayMetrics.nameW
}

// execNameDisplayW caps the execution name column so streaming hints keep more room.
func execNameDisplayW() int {
	nw := execNameW()
	if nw > execNameMaxCol {
		return execNameMaxCol
	}
	return nw
}

// DisplayExecutionProgress prints a pending-action line ending with \r so that
// DisplayExecutionResult can overwrite it in place. lastLine, if non-empty,
// is shown as a dim hint after the ellipsis so long-running commands look alive.
func DisplayExecutionProgress(w io.Writer, node *PlanNode, lastLine string) {
	tw := termWidth()
	nw := execNameDisplayW()
	name := truncatePath(node.DisplayName, nw)
	// Fixed portion: "  ·  " (5) + name (nw) + "  " (2) + type (typeW) + "  …  " (5)
	fixedLen := 5 + nw + 2 + displayMetrics.typeW + 5
	hint := ""
	if lastLine != "" {
		budget := tw - fixedLen - 1
		if budget < 1 {
			budget = 1
		}
		lastLine = truncateStringVisual(lastLine, budget)
		hint = ansiDim + lastLine + ansiReset
	}
	fmt.Fprintf(w, "\r\033[K  %s  %-*s  %-*s  %s…%s  %s",
		ansiDim+"·"+ansiReset,
		nw, name,
		displayMetrics.typeW, typeLabel(node.ResourceType),
		ansiDim, ansiReset,
		hint,
	)
}

// DisplayExecutionResult overwrites the progress line with the final outcome.
// Pass dur=0 for TRACK/ADOPT/LEVEL nodes and skipped-by-dependency nodes.
func DisplayExecutionResult(w io.Writer, node *PlanNode, err error, dur time.Duration) {
	nw := execNameDisplayW()
	name := truncatePath(node.DisplayName, nw)
	typ := typeLabel(node.ResourceType)

	var sym, status string
	switch {
	case err != nil:
		sym = ansiRed + "✗" + ansiReset
		status = ansiRed + "failed" + ansiReset
	case node.Action == ActionSkip:
		sym = ansiDim + "!" + ansiReset
		status = ansiDim + "skipped" + ansiReset
	default:
		sym = ansiGreen + "✓" + ansiReset
		if dur > 0 {
			status = ansiDim + formatDur(dur) + ansiReset
		}
	}

	fmt.Fprintf(w, "\r\033[K  %s  %-*s  %-*s  %s\n",
		sym,
		nw, name,
		displayMetrics.typeW, typ,
		status,
	)

	if err != nil {
		noteLine(w, err.Error(), termWidth())
	}
}

// ─── Confirmation prompt ──────────────────────────────────────────────────────

// Confirm prints the prompt and reads a Y/n response.
// Returns true if the user presses Enter or types y/Y/yes.
func Confirm(w io.Writer, r io.Reader) (bool, error) {
	fmt.Fprint(w, "  Proceed? [Y/n]: ")
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil // EOF
	}
	resp := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return resp == "" || resp == "y" || resp == "yes", nil
}

// ─── Terminal helpers ─────────────────────────────────────────────────────────

// termWidth returns the current terminal width, or 120 as a fallback.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return 120
	}
	return w
}

// truncatePath shortens a path to at most width characters, preserving the
// start of the path and the last two segments (parent/file), joined by "/.../".
// If even the last two segments exceed the budget, the filename is truncated.
func truncatePath(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	tail := s
	if i := strings.LastIndex(s, "/"); i > 0 {
		if j := strings.LastIndex(s[:i], "/"); j >= 0 {
			tail = s[j+1:]
		}
	}
	const ellipsis = "/.../"
	tailRoom := width - len(ellipsis)
	if tailRoom < 3 {
		if len(s) > width-1 {
			return s[:width-1] + "…"
		}
		return s
	}
	if len(tail) > tailRoom {
		tail = tail[:tailRoom-3] + "..."
	}
	frontRoom := width - len(ellipsis) - len(tail)
	if frontRoom <= 0 {
		return tail
	}
	return s[:frontRoom] + ellipsis + tail
}

// padCenter centers s within a field of the given width.
// Used by DisplayShow.
func padCenter(s string, width int) string {
	if len(s) >= width {
		return s
	}
	total := width - len(s)
	left := total / 2
	right := total - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

// ─── Show command ─────────────────────────────────────────────────────────────

// DisplayShow writes a read-only table of all currently-tracked nodes to w.
// scope controls what is printed:
//
//	"all"       — variables section + all resource types
//	"variables" — only the resolved variables map
//	any other   — only nodes whose resource type matches the scope
func DisplayShow(w io.Writer, st *state.State, vars map[string]string, scope string) {
	// ── Variables section ────────────────────────────────────────────────────
	if scope == "all" || scope == "variables" {
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "%sVariables%s\n", ansiBold, ansiReset)
		w2 := 0
		for _, k := range keys {
			if l := len(k) + 3; l > w2 { // "${" + key + "}"
				w2 = l
			}
		}
		for _, k := range keys {
			token := "${" + k + "}"
			fmt.Fprintf(w, "  %-*s  %s\n", w2, token, vars[k])
		}
		fmt.Fprintln(w)
		if scope == "variables" {
			return
		}
	}

	// ── Tracked nodes table ──────────────────────────────────────────────────
	type showRow struct {
		resourceType string
		level        string
		item         string
		hash         string
		tracked      string
	}

	orderIdx := make(map[string]int, len(canonicalOrder))
	for i, t := range canonicalOrder {
		orderIdx[t] = i
	}

	var rows []showRow
	for id, entry := range st.Nodes {
		slash := strings.Index(id, "/")
		if slash < 0 {
			continue
		}
		resType := id[:slash]
		item := id[slash+1:]

		if scope != "all" && resType != scope {
			continue
		}

		level := entry.Level
		if level == "" {
			level = "common"
		}
		rows = append(rows, showRow{
			resourceType: typeLabel(resType),
			level:        level,
			item:         item,
			hash:         entry.Hash,
			tracked:      entry.TrackedAt.Format("2006-01-02"),
		})
	}

	if len(rows) == 0 {
		fmt.Fprintln(w, ansiDim+"(nothing tracked)"+ansiReset)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		oi := orderIdx[rows[i].resourceType]
		oj := orderIdx[rows[j].resourceType]
		if oi != oj {
			return oi < oj
		}
		return rows[i].item < rows[j].item
	})

	// Compute column widths.
	w1, w2, w3, w4, w5 := len("RESOURCE"), len("LEVEL"), len("ITEM"), len("HASH"), len("TRACKED")
	for _, r := range rows {
		if l := len(r.resourceType); l > w1 {
			w1 = l
		}
		if l := len(r.level); l > w2 {
			w2 = l
		}
		if l := len(r.item); l > w3 {
			w3 = l
		}
		if l := len(r.hash); l > w4 {
			w4 = l
		}
	}

	// Header.
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s%s%s  %s%s%s  %s%s%s  %s%s%s  %s%s%s\n",
		ansiBold, padCenter("RESOURCE", w1), ansiReset,
		ansiBold, padCenter("LEVEL", w2), ansiReset,
		ansiBold, padCenter("ITEM", w3), ansiReset,
		ansiBold, padCenter("HASH", w4), ansiReset,
		ansiBold, padCenter("TRACKED", w5), ansiReset,
	)
	sep := fmt.Sprintf("  %s  %s  %s  %s  %s",
		strings.Repeat("-", w1), strings.Repeat("-", w2),
		strings.Repeat("-", w3), strings.Repeat("-", w4),
		strings.Repeat("-", w5),
	)
	fmt.Fprintln(w, ansiDim+sep+ansiReset)

	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %-*s  %-*s  %s%s%s  %s\n",
			w1, r.resourceType,
			w2, r.level,
			w3, r.item,
			ansiDim, r.hash, ansiReset,
			r.tracked,
		)
	}
	fmt.Fprintln(w)
}
