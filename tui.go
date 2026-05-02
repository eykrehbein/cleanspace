package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// -- Styles --

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	catStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("243"))
	selStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	unselStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	curStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	sizeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	timeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	footStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	helpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

// -- Phase --

type phase int

const (
	phaseScanning phase = iota
	phaseResults
	phaseDeleting
	phaseDone
)

// -- Messages --

type scanAgentDoneMsg struct {
	agent   string
	targets []Target
}
type delTickMsg struct {
	index int
	err   error
}

// -- Scan Agents --

type scanAgent struct {
	id    string
	label string
	done  bool
}

type visibleTargetRow struct {
	index int
	depth int
}

type selectionState int

const (
	selectionNone selectionState = iota
	selectionPartial
	selectionAll
	selectionLocked
)

// -- Model --

type model struct {
	phase         phase
	targets       []Target
	cursor        int
	scroll        int
	spinner       spinner.Model
	width         int
	height        int
	scanDir       string
	staleDays     int
	sizeThreshold int64
	confirm       bool

	scanAgents []scanAgent

	deleteQueue []int
	deletesDone int
	freedSpace  int64
	errCount    int
}

func newModel(scanDir string, staleDays int, sizeThreshold int64) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))

	var agents []scanAgent
	if scanDir != "" {
		agents = append(agents, scanAgent{"node_modules", "stale node_modules in inactive JS/TS projects", false})
		agents = append(agents, scanAgent{"project_artifacts", "stale build artifacts like target, dist, and .venv", false})
		if sizeThreshold > 0 {
			agents = append(agents, scanAgent{
				"large_files",
				fmt.Sprintf("files larger than %s", humanBytes(sizeThreshold)),
				false,
			})
		}
	}
	agents = append(agents,
		scanAgent{"docker", "Docker reclaimable data", false},
		scanAgent{"caches", "known caches", false},
		scanAgent{"generic_caches", "heavy cache directories", false},
		scanAgent{"runtime_data", "runtimes and toolchains", false},
		scanAgent{"mobile_data", "mobile dev data", false},
		scanAgent{"unused_apps", fmt.Sprintf("apps not used in %d+ days", unusedAppDays), false},
		scanAgent{"app_data", "app data hotspots", false},
		scanAgent{"user_data", "user data hotspots", false},
		scanAgent{"system_hotspots", "system hotspots", false},
	)

	return model{
		phase:         phaseScanning,
		spinner:       s,
		scanDir:       scanDir,
		staleDays:     staleDays,
		sizeThreshold: sizeThreshold,
		scanAgents:    agents,
		width:         80,
		height:        24,
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}

	if m.scanDir != "" {
		dir, days, threshold := m.scanDir, m.staleDays, m.sizeThreshold
		cmds = append(cmds, func() tea.Msg {
			return scanAgentDoneMsg{"node_modules", scanNodeModules(dir, days)}
		})
		cmds = append(cmds, func() tea.Msg {
			return scanAgentDoneMsg{"project_artifacts", scanProjectArtifacts(dir, days, threshold)}
		})
		if m.sizeThreshold > 0 {
			dir, threshold := m.scanDir, m.sizeThreshold
			cmds = append(cmds, func() tea.Msg {
				return scanAgentDoneMsg{"large_files", scanLargeFiles(dir, threshold)}
			})
		}
	}

	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"docker", scanDockerTargets()}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"caches", scanCaches()}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"generic_caches", scanGenericCaches(m.sizeThreshold)}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"runtime_data", scanRuntimeData(m.sizeThreshold)}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"mobile_data", scanMobileData(m.sizeThreshold)}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"unused_apps", scanUnusedApps(unusedAppDays)}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"app_data", scanAppData(m.sizeThreshold)}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"user_data", scanUserData(m.sizeThreshold)}
	})
	cmds = append(cmds, func() tea.Msg {
		return scanAgentDoneMsg{"system_hotspots", scanSystemHotspots(m.sizeThreshold)}
	})

	return tea.Batch(cmds...)
}

// -- Update --

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		if m.phase == phaseScanning || m.phase == phaseDeleting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case scanAgentDoneMsg:
		m.targets = append(m.targets, msg.targets...)
		for i := range m.scanAgents {
			if m.scanAgents[i].id == msg.agent {
				m.scanAgents[i].done = true
				break
			}
		}
		allDone := true
		for _, a := range m.scanAgents {
			if !a.done {
				allDone = false
				break
			}
		}
		if allDone {
			m.targets = dedupeTargets(m.targets)
			sort.Slice(m.targets, func(i, j int) bool {
				if m.targets[i].Category != m.targets[j].Category {
					return m.targets[i].Category < m.targets[j].Category
				}
				return m.targets[i].Size > m.targets[j].Size
			})
			if len(m.targets) == 0 {
				m.phase = phaseDone
			} else {
				m.phase = phaseResults
			}
		}
		return m, nil

	case delTickMsg:
		if msg.index >= 0 && msg.index < len(m.targets) {
			m.targets[msg.index].Deleted = true
			if msg.err != nil {
				m.targets[msg.index].Error = msg.err.Error()
				m.errCount++
			} else {
				m.freedSpace += m.targets[msg.index].Size
			}
		}
		m.deletesDone++
		if m.deletesDone >= len(m.deleteQueue) {
			m.phase = phaseDone
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()

	switch m.phase {
	case phaseScanning:
		if k == "q" || k == "ctrl+c" || k == "esc" {
			return m, tea.Quit
		}

	case phaseResults:
		// Any navigation/action key cancels the confirm prompt
		if m.confirm && k != "enter" && k != "esc" && k != "q" && k != "ctrl+c" {
			m.confirm = false
		}

		switch k {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.confirm {
				m.confirm = false
				return m, nil
			}
			return m, tea.Quit
		case "up", "k":
			visible := m.visibleTargets()
			pos := m.ensureVisibleCursor(visible)
			if pos > 0 {
				m.cursor = visible[pos-1].index
				m.fixScroll()
			}
		case "down", "j":
			visible := m.visibleTargets()
			pos := m.ensureVisibleCursor(visible)
			if pos >= 0 && pos < len(visible)-1 {
				m.cursor = visible[pos+1].index
				m.fixScroll()
			}
		case "right", "l":
			if m.cursor < len(m.targets) && m.targets[m.cursor].Expandable {
				m.targets[m.cursor].Expanded = true
				m.fixScroll()
			}
		case "left", "h":
			if m.cursor < len(m.targets) {
				if m.targets[m.cursor].Expanded {
					m.targets[m.cursor].Expanded = false
					m.fixScroll()
				} else if parent := m.parentIndex(m.cursor); parent >= 0 {
					m.cursor = parent
					m.fixScroll()
				}
			}
		case " ":
			if m.cursor < len(m.targets) {
				m.toggleTargetSelection(m.cursor)
				if !m.targets[m.cursor].Expandable {
					visible := m.visibleTargets()
					pos := m.ensureVisibleCursor(visible)
					if pos >= 0 && pos < len(visible)-1 {
						m.cursor = visible[pos+1].index
					}
					m.fixScroll()
				}
			}
		case "a":
			for i := range m.targets {
				if m.isActionTarget(i) {
					m.targets[i].Selected = true
				}
			}
		case "n":
			for i := range m.targets {
				if m.isActionTarget(i) {
					m.targets[i].Selected = false
				}
			}
		case "enter":
			sc := m.selectedCount()
			if sc == 0 {
				return m, nil
			}
			if !m.confirm {
				m.confirm = true
				return m, nil
			}
			m.confirm = false
			m.phase = phaseDeleting
			m.deleteQueue = nil
			for i, t := range m.targets {
				if t.Selected && m.isActionTarget(i) {
					m.deleteQueue = append(m.deleteQueue, i)
				}
			}
			m.deletesDone = 0
			return m, tea.Batch(m.spinner.Tick, m.startParallelDelete())
		}

	case phaseDeleting:
		if k == "ctrl+c" {
			return m, tea.Quit
		}

	case phaseDone:
		return m, tea.Quit
	}

	return m, nil
}

func (m model) startParallelDelete() tea.Cmd {
	sem := make(chan struct{}, runtime.NumCPU())
	cmds := make([]tea.Cmd, len(m.deleteQueue))
	for i, idx := range m.deleteQueue {
		idx := idx
		target := m.targets[idx]
		cmds[i] = func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			err := deleteTarget(target)
			return delTickMsg{index: idx, err: err}
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) fixScroll() {
	visible := m.visibleTargets()
	lineIdx := 0
	var lastCat Category = -1
	pos := m.ensureVisibleCursor(visible)
	for i := 0; i <= pos && i < len(visible); i++ {
		t := m.targets[visible[i].index]
		if visible[i].depth == 0 && t.Category != lastCat {
			if lastCat >= 0 {
				lineIdx++
			}
			lastCat = t.Category
			lineIdx++
		}
		if i == pos {
			break
		}
		lineIdx++
	}

	avail := m.height - 7
	if avail < 3 {
		avail = 3
	}
	if lineIdx < m.scroll {
		m.scroll = lineIdx
	}
	if lineIdx >= m.scroll+avail {
		m.scroll = lineIdx - avail + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m model) selectedCount() int {
	n := 0
	for i, t := range m.targets {
		if t.Selected && m.isActionTarget(i) {
			n++
		}
	}
	return n
}

func (m model) selectedSize() int64 {
	var s int64
	for i, t := range m.targets {
		if t.Selected && m.isActionTarget(i) {
			s += t.Size
		}
	}

	for i, t := range m.targets {
		if !t.Group {
			continue
		}

		children := m.childIndexes(targetKey(t))
		if len(children) == 0 || m.selectionStateForTarget(i) != selectionAll {
			continue
		}

		var childSize int64
		for _, child := range children {
			if m.targets[child].Selected && m.isActionTarget(child) {
				childSize += m.targets[child].Size
			}
		}
		if t.Size > childSize {
			s += t.Size - childSize
		}
	}
	return s
}

func (m model) visibleTargets() []visibleTargetRow {
	topLevel := make([]int, 0, len(m.targets))
	for i, target := range m.targets {
		if target.ParentID == "" {
			topLevel = append(topLevel, i)
		}
	}
	sort.Slice(topLevel, func(i, j int) bool {
		return m.lessTarget(topLevel[i], topLevel[j], true)
	})

	rows := make([]visibleTargetRow, 0, len(m.targets))
	for _, idx := range topLevel {
		m.appendVisibleTarget(&rows, idx, 0)
	}
	return rows
}

func (m model) appendVisibleTarget(rows *[]visibleTargetRow, idx, depth int) {
	*rows = append(*rows, visibleTargetRow{index: idx, depth: depth})
	if !m.targets[idx].Expandable || !m.targets[idx].Expanded {
		return
	}

	children := m.childIndexes(targetKey(m.targets[idx]))
	for _, childIdx := range children {
		m.appendVisibleTarget(rows, childIdx, depth+1)
	}
}

func (m model) childIndexes(parentID string) []int {
	var children []int
	for i, target := range m.targets {
		if target.ParentID == parentID {
			children = append(children, i)
		}
	}
	sort.Slice(children, func(i, j int) bool {
		return m.lessTarget(children[i], children[j], false)
	})
	return children
}

func (m model) parentIndex(idx int) int {
	parentID := m.targets[idx].ParentID
	if parentID == "" {
		return -1
	}
	for i, target := range m.targets {
		if targetKey(target) == parentID {
			return i
		}
	}
	return -1
}

func (m *model) ensureVisibleCursor(rows []visibleTargetRow) int {
	for idx := m.cursor; idx >= 0; idx = m.parentIndex(idx) {
		for pos, row := range rows {
			if row.index == idx {
				m.cursor = idx
				return pos
			}
		}
		if m.parentIndex(idx) < 0 {
			break
		}
	}

	if len(rows) > 0 {
		m.cursor = rows[0].index
		return 0
	}
	m.cursor = 0
	return -1
}

func (m model) lessTarget(i, j int, includeCategory bool) bool {
	a := m.targets[i]
	b := m.targets[j]
	if includeCategory && a.Category != b.Category {
		return a.Category < b.Category
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return strings.ToLower(a.Label) < strings.ToLower(b.Label)
}

func (m model) isActionTarget(idx int) bool {
	target := m.targets[idx]
	if target.Locked || target.Group {
		return false
	}
	if len(m.childIndexes(targetKey(target))) > 0 {
		return false
	}
	return target.Path != "" || len(target.Command) > 0
}

func (m model) selectionStateForTarget(idx int) selectionState {
	target := m.targets[idx]
	if target.Locked {
		return selectionLocked
	}

	children := m.childIndexes(targetKey(target))
	if len(children) == 0 {
		if target.Selected {
			return selectionAll
		}
		return selectionNone
	}

	anySelectable := false
	anySelected := false
	allSelected := true
	for _, child := range children {
		state := m.selectionStateForTarget(child)
		if state == selectionLocked {
			continue
		}
		anySelectable = true
		if state == selectionAll || state == selectionPartial {
			anySelected = true
		}
		if state != selectionAll {
			allSelected = false
		}
	}

	if !anySelectable {
		return selectionLocked
	}
	if allSelected {
		return selectionAll
	}
	if anySelected {
		return selectionPartial
	}
	return selectionNone
}

func (m *model) toggleTargetSelection(idx int) {
	if idx < 0 || idx >= len(m.targets) || m.targets[idx].Locked {
		return
	}

	children := m.childIndexes(targetKey(m.targets[idx]))
	if len(children) > 0 {
		m.setSelectionRecursive(idx, m.selectionStateForTarget(idx) != selectionAll)
		return
	}

	if m.isActionTarget(idx) {
		m.targets[idx].Selected = !m.targets[idx].Selected
	}
}

func (m *model) setSelectionRecursive(idx int, selected bool) {
	if idx < 0 || idx >= len(m.targets) || m.targets[idx].Locked {
		return
	}

	children := m.childIndexes(targetKey(m.targets[idx]))
	if len(children) == 0 {
		if m.isActionTarget(idx) {
			m.targets[idx].Selected = selected
		}
		return
	}

	for _, child := range children {
		m.setSelectionRecursive(child, selected)
	}
}

// -- Views --

func (m model) View() string {
	switch m.phase {
	case phaseScanning:
		return m.viewScanning()
	case phaseResults:
		return m.viewResults()
	case phaseDeleting:
		return m.viewDeleting()
	case phaseDone:
		return m.viewDone()
	}
	return ""
}

func (m model) viewScanning() string {
	var b strings.Builder
	done := 0
	for _, a := range m.scanAgents {
		if a.done {
			done++
		}
	}

	b.WriteString("\n  ")
	b.WriteString(titleStyle.Render("✦ CleanSpace"))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render(fmt.Sprintf("%d/%d checks complete", done, len(m.scanAgents))))
	b.WriteString("\n\n")

	b.WriteString("  ")
	b.WriteString(dimStyle.Render("Inspecting disk usage only. Nothing is deleted during this step."))
	b.WriteString("\n")
	b.WriteString("  ")
	if m.scanDir != "" {
		scanPath := shortenHome(m.scanDir)
		if scanPath == "~" {
			scanPath = "~/"
		}
		b.WriteString(dimStyle.Render("Scanning " + scanPath + " plus caches, toolchains, unused apps, app data, and system hotspots."))
	} else {
		b.WriteString(dimStyle.Render("Scanning caches, toolchains, unused apps, app data, and system hotspots."))
	}
	b.WriteString("\n\n")

	for _, a := range m.scanAgents {
		b.WriteString("  ")
		if a.done {
			b.WriteString(okStyle.Render("✓"))
			b.WriteString(" Checked ")
		} else {
			b.WriteString(m.spinner.View())
			b.WriteString(" Checking ")
		}
		b.WriteString(a.label)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	return b.String()
}

func (m model) viewResults() string {
	var b strings.Builder

	// Header
	var total int64
	for _, t := range m.targets {
		if t.ParentID == "" {
			total += t.Size
		}
	}
	b.WriteString("\n  ")
	b.WriteString(titleStyle.Render("✦ CleanSpace"))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render(humanBytes(total) + " found"))
	b.WriteString("\n")

	// Build display lines
	type dl struct {
		text string
		item int // -1 = non-selectable (header/blank)
	}
	var lines []dl
	var lastCat Category = -1
	visible := m.visibleTargets()

	for _, row := range visible {
		i := row.index
		t := m.targets[i]
		if row.depth == 0 && t.Category != lastCat {
			if lastCat >= 0 {
				lines = append(lines, dl{"", -1})
			}
			lastCat = t.Category
			lines = append(lines, dl{
				text: "  " + catStyle.Render(t.Category.String()),
				item: -1,
			})
		}

		cur := "  "
		if i == m.cursor {
			cur = curStyle.Render("›") + " "
		}
		chk := unselStyle.Render("○")
		switch m.selectionStateForTarget(i) {
		case selectionLocked:
			chk = dimStyle.Render("·")
		case selectionAll:
			chk = selStyle.Render("●")
		case selectionPartial:
			chk = warnStyle.Render("◐")
		}

		indent := strings.Repeat("  ", row.depth)
		disclosure := ""
		if t.Expandable {
			if t.Expanded {
				disclosure = "▾ "
			} else {
				disclosure = "▸ "
			}
		}

		leftLabel := t.Label
		if t.Locked {
			leftLabel += " [review]"
		}
		left := "  " + cur + chk + " " + indent + disclosure + leftLabel

		right := sizeStyle.Render(humanBytes(t.Size))
		if t.Size == 0 && t.Info != "" {
			right = timeStyle.Render(t.Info)
		} else if t.Info != "" {
			right += "  " + timeStyle.Render(t.Info)
		} else if (t.Category == CategoryNodeModules || t.Category == CategoryProjectArtifacts) && !t.LastMod.IsZero() {
			right += "  " + timeStyle.Render(timeAgo(t.LastMod))
		}

		lw := lipgloss.Width(left)
		rw := lipgloss.Width(right)
		gap := m.width - lw - rw - 1
		if gap < 2 {
			gap = 2
		}

		lines = append(lines, dl{
			text: left + strings.Repeat(" ", gap) + right,
			item: i,
		})
	}

	// Scrollable window
	avail := m.height - 7
	if avail < 3 {
		avail = 3
	}
	start := m.scroll
	if start > len(lines) {
		start = 0
	}
	end := start + avail
	if end > len(lines) {
		end = len(lines)
	}

	for i := start; i < end; i++ {
		b.WriteString(lines[i].text)
		b.WriteString("\n")
	}

	// Scroll indicators
	if len(lines) > avail {
		ind := "  "
		if start > 0 {
			ind += "↑ "
		}
		if end < len(lines) {
			ind += "↓ "
		}
		if len(strings.TrimSpace(ind)) > 0 {
			b.WriteString(dimStyle.Render(ind))
			b.WriteString("\n")
		}
	}

	// Footer
	sc := m.selectedCount()
	ss := m.selectedSize()
	b.WriteString("\n")
	b.WriteString(footStyle.Render(fmt.Sprintf("  Selected: %d/%d items · %s",
		sc, len(m.targets), humanBytes(ss))))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(
		"  · [review] = large or important data we found, but this app will not auto-delete it"))
	b.WriteString("\n")

	if m.confirm {
		b.WriteString("  ")
		b.WriteString(warnStyle.Render(fmt.Sprintf(
			"Delete %d items (%s)? Press enter to confirm, esc to cancel.",
			sc, humanBytes(ss))))
		b.WriteString("\n")
	} else {
		b.WriteString(helpStyle.Render(
			"  ↑↓ navigate  ←→ expand  ␣ toggle  a all  n none  ⏎ delete  q quit"))
		b.WriteString("\n")
	}

	return b.String()
}

func (m model) viewDeleting() string {
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(titleStyle.Render("✦ CleanSpace"))
	b.WriteString("\n\n  ")
	b.WriteString(m.spinner.View())
	b.WriteString(fmt.Sprintf(" Deleting... %d/%d\n\n", m.deletesDone, len(m.deleteQueue)))

	for _, idx := range m.deleteQueue {
		t := m.targets[idx]
		var mark string
		switch {
		case t.Deleted && t.Error != "":
			mark = errStyle.Render("✗")
		case t.Deleted:
			mark = okStyle.Render("✓")
		default:
			mark = m.spinner.View()
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, dimStyle.Render(t.Label)))
		if t.Error != "" {
			for _, line := range wrapText("Error: "+oneLineError(t.Error), max(40, m.width-8)) {
				b.WriteString("      ")
				b.WriteString(errStyle.Render(line))
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

func (m model) viewDone() string {
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(titleStyle.Render("✦ CleanSpace"))
	b.WriteString("\n\n")

	switch {
	case len(m.targets) == 0:
		b.WriteString("  ")
		b.WriteString(okStyle.Render("✓"))
		b.WriteString(" Nothing to clean — your system is tidy!\n")
	case m.freedSpace > 0:
		b.WriteString("  ")
		b.WriteString(okStyle.Render("✓"))
		b.WriteString(fmt.Sprintf(" Done! Freed %s", humanBytes(m.freedSpace)))
		if m.errCount > 0 {
			b.WriteString(errStyle.Render(fmt.Sprintf(" (%d errors)", m.errCount)))
		}
		b.WriteString("\n")
	case m.errCount > 0:
		b.WriteString("  ")
		b.WriteString(errStyle.Render("✗"))
		b.WriteString(fmt.Sprintf(" Failed to delete %d items.\n", m.errCount))
	}

	if m.errCount > 0 {
		b.WriteString("\n  ")
		b.WriteString(warnStyle.Render("Deletion errors"))
		b.WriteString("\n")
		for _, t := range m.targets {
			if t.Error == "" {
				continue
			}
			b.WriteString("  ")
			b.WriteString(errStyle.Render("✗"))
			b.WriteString(" ")
			b.WriteString(t.Label)
			b.WriteString("\n")
			for _, line := range wrapText(oneLineError(t.Error), max(40, m.width-6)) {
				b.WriteString("    ")
				b.WriteString(dimStyle.Render(line))
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("\n  ")
	b.WriteString(dimStyle.Render("Press any key to exit."))
	b.WriteString("\n")
	return b.String()
}

func oneLineError(err string) string {
	err = strings.TrimSpace(err)
	err = strings.ReplaceAll(err, "\n", " ")
	err = strings.Join(strings.Fields(err), " ")
	return err
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	lines := []string{words[0]}
	for _, word := range words[1:] {
		current := lines[len(lines)-1]
		if lipgloss.Width(current)+1+lipgloss.Width(word) <= width {
			lines[len(lines)-1] = current + " " + word
			continue
		}
		lines = append(lines, word)
	}
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
