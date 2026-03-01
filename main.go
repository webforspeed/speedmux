package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"code.selman.me/hauntty/libghostty"
	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

const (
	maxScrollbackLines = 2000
	mouseScrollStep    = 3
)

type splitOrientation int

const (
	splitVertical splitOrientation = iota
	splitHorizontal
)

type rect struct {
	x int
	y int
	w int
	h int
}

type pane struct {
	id int

	cmd         *exec.Cmd
	pty         *os.File
	ghosttyTerm *libghostty.Terminal
	mu          sync.Mutex
	lines       []string
	curr        []rune
	sawCR       bool
	onOutput    func(lines int)
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\a]*(?:\a|\x1b\\)|\x1b[@-Z\\-_]`)

func newPane(id int, redraw func(), ghosttyRuntime *libghostty.Runtime, onOutput func(lines int)) (*pane, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := newShellCommand(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	p := &pane{id: id, cmd: cmd, pty: ptmx, lines: make([]string, 0, 128), onOutput: onOutput}
	if ghosttyRuntime != nil {
		term, err := ghosttyRuntime.NewTerminal(context.Background(), 80, 24, maxScrollbackLines)
		if err == nil {
			p.ghosttyTerm = term
		}
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				added := p.appendOutputWithDelta(buf[:n])
				if added > 0 && p.onOutput != nil {
					p.onOutput(added)
				}
				redraw()
			}
			if readErr != nil {
				p.appendOutput([]byte("\n[session ended]\n"))
				redraw()
				return
			}
		}
	}()

	return p, nil
}

func newShellCommand(shell string) *exec.Cmd {
	// Login shells load the same profile files users expect from Terminal/iTerm.
	args := []string{}
	if shouldUseLoginShell(shell) {
		args = append(args, "-l")
	}
	return exec.Command(shell, args...)
}

func shouldUseLoginShell(shell string) bool {
	switch filepath.Base(shell) {
	case "sh", "bash", "zsh", "ksh", "mksh", "dash", "fish", "yash":
		return true
	default:
		return false
	}
}

func (p *pane) appendOutput(raw []byte) {
	_ = p.appendOutputWithDelta(raw)
}

func (p *pane) appendOutputWithDelta(raw []byte) int {
	if p.ghosttyTerm != nil {
		_ = p.ghosttyTerm.Feed(context.Background(), raw)
		return bytes.Count(raw, []byte{'\n'})
	}

	clean := ansiEscape.ReplaceAllString(string(raw), "")
	p.mu.Lock()
	defer p.mu.Unlock()
	linesAdded := 0

	for len(clean) > 0 {
		r, size := utf8.DecodeRuneInString(clean)
		clean = clean[size:]

		if p.sawCR {
			if r == '\n' {
				p.lines = append(p.lines, string(p.curr))
				linesAdded++
				p.curr = p.curr[:0]
				p.sawCR = false
				if len(p.lines) > maxScrollbackLines {
					drop := len(p.lines) - maxScrollbackLines
					p.lines = append([]string(nil), p.lines[drop:]...)
				}
				continue
			}
			// Carriage return without newline rewinds to column 0.
			p.curr = p.curr[:0]
			p.sawCR = false
		}

		switch r {
		case utf8.RuneError:
			continue
		case '\r':
			p.sawCR = true
		case '\n':
			p.lines = append(p.lines, string(p.curr))
			linesAdded++
			p.curr = p.curr[:0]
			if len(p.lines) > maxScrollbackLines {
				drop := len(p.lines) - maxScrollbackLines
				p.lines = append([]string(nil), p.lines[drop:]...)
			}
		case '\b':
			if len(p.curr) > 0 {
				p.curr = p.curr[:len(p.curr)-1]
			}
		case '\t':
			p.curr = append(p.curr, ' ', ' ', ' ', ' ')
		default:
			if r >= 32 {
				p.curr = append(p.curr, r)
			}
		}
	}
	return linesAdded
}

func (p *pane) visibleLines(width, height int) []string {
	lines, _ := p.visibleLinesWithScroll(width, height, 0)
	return lines
}

func (p *pane) visibleLinesWithScroll(width, height, scroll int) ([]string, int) {
	if width <= 0 || height <= 0 {
		return nil, 0
	}

	if p.ghosttyTerm != nil {
		return p.visibleLinesGhostty(width, height, scroll)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	all := make([]string, 0, len(p.lines)+1)
	all = append(all, p.lines...)
	all = append(all, string(p.curr))

	return windowLines(all, width, height, scroll)
}

func (p *pane) visibleLinesGhostty(width, height, scroll int) ([]string, int) {
	dump, err := p.ghosttyTerm.DumpScreen(context.Background(), libghostty.DumpPlain|libghostty.DumpFlagUnwrap|libghostty.DumpFlagScrollback)
	if err != nil {
		return []string{truncateRunes("[ghostty-vt dump error]", width)}, 0
	}

	normalized := strings.ReplaceAll(string(dump.VT), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	return windowLines(lines, width, height, scroll)
}

func windowLines(all []string, width, height, scroll int) ([]string, int) {
	if scroll < 0 {
		scroll = 0
	}

	maxScroll := 0
	if len(all) > height {
		maxScroll = len(all) - height
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	start := len(all) - height - scroll
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > len(all) {
		end = len(all)
	}

	out := make([]string, end-start)
	copy(out, all[start:end])
	for i := range out {
		out[i] = truncateRunes(out[i], width)
	}
	return out, scroll
}

func (p *pane) writeInput(data []byte) {
	if len(data) == 0 || p.pty == nil {
		return
	}
	_, _ = p.pty.Write(data)
}

func (p *pane) resize(cols, rows int) {
	if p.pty == nil || cols <= 0 || rows <= 0 {
		return
	}
	_ = pty.Setsize(p.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if p.ghosttyTerm != nil {
		_ = p.ghosttyTerm.Resize(context.Background(), uint32(cols), uint32(rows))
	}
}

func (p *pane) close() {
	if p.ghosttyTerm != nil {
		_ = p.ghosttyTerm.Close(context.Background())
	}
	if p.pty != nil {
		_ = p.pty.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
}

type node struct {
	parent      *node
	orientation splitOrientation
	first       *node
	second      *node
	pane        *pane
}

func (n *node) isLeaf() bool {
	return n != nil && n.pane != nil
}

func (n *node) split(orientation splitOrientation, newPane *pane) *node {
	if n == nil || n.pane == nil {
		return nil
	}
	oldPane := n.pane
	n.pane = nil
	n.orientation = orientation
	n.first = &node{parent: n, pane: oldPane}
	n.second = &node{parent: n, pane: newPane}
	return n.second
}

func (n *node) walkLeaves(dst *[]*node) {
	if n == nil {
		return
	}
	if n.isLeaf() {
		*dst = append(*dst, n)
		return
	}
	n.first.walkLeaves(dst)
	n.second.walkLeaves(dst)
}

func (n *node) firstLeaf() *node {
	if n == nil {
		return nil
	}
	if n.isLeaf() {
		return n
	}
	if leaf := n.first.firstLeaf(); leaf != nil {
		return leaf
	}
	return n.second.firstLeaf()
}

func (n *node) layout(r rect, fn func(*node, rect)) {
	if n == nil || r.w <= 0 || r.h <= 0 {
		return
	}
	if n.isLeaf() {
		fn(n, r)
		return
	}

	switch n.orientation {
	case splitVertical:
		leftW := r.w / 2
		rightW := r.w - leftW
		if leftW <= 0 {
			leftW = 1
			rightW = r.w - 1
		}
		if rightW <= 0 {
			rightW = 1
			leftW = r.w - 1
		}
		n.first.layout(rect{x: r.x, y: r.y, w: leftW, h: r.h}, fn)
		n.second.layout(rect{x: r.x + leftW, y: r.y, w: rightW, h: r.h}, fn)
	case splitHorizontal:
		topH := r.h / 2
		botH := r.h - topH
		if topH <= 0 {
			topH = 1
			botH = r.h - 1
		}
		if botH <= 0 {
			botH = 1
			topH = r.h - 1
		}
		n.first.layout(rect{x: r.x, y: r.y, w: r.w, h: topH}, fn)
		n.second.layout(rect{x: r.x, y: r.y + topH, w: r.w, h: botH}, fn)
	}
}

type app struct {
	screen tcell.Screen
	root   *node
	active *node
	nextID int

	ghosttyRuntime *libghostty.Runtime
	paneScroll     map[int]int
	outputCh       chan paneOutput
	redrawCh       chan struct{}
	quitting       bool
	statsEnabled   bool
	renderCount    uint64
	totalCells     uint64
	lastFrameCells int
	statsStartedAt time.Time
	lastFrameAt    time.Time
	smoothedFPS    float64
	keyEvents      uint64
	mouseEvents    uint64
	resizeEvents   uint64
	outputLines    uint64
}

type paneOutput struct {
	paneID int
	lines  int
}

func newApp() (*app, error) {
	s, err := tcell.NewScreen()
	if err != nil {
		return nil, fmt.Errorf("new screen: %w", err)
	}
	if err := s.Init(); err != nil {
		return nil, fmt.Errorf("init screen: %w", err)
	}
	s.EnableMouse()

	a := &app{
		screen:       s,
		redrawCh:     make(chan struct{}, 1),
		nextID:       1,
		paneScroll:   map[int]int{},
		outputCh:     make(chan paneOutput, 256),
		statsEnabled: shouldEnableStats(),
	}
	if shouldEnableGhosttyVT() {
		rt, err := libghostty.NewRuntime(context.Background())
		if err == nil {
			a.ghosttyRuntime = rt
		}
	}

	firstPaneID := a.nextID
	firstPane, err := newPane(firstPaneID, a.requestRedraw, a.ghosttyRuntime, func(lines int) {
		a.notifyPaneOutput(firstPaneID, lines)
	})
	if err != nil {
		s.Fini()
		if a.ghosttyRuntime != nil {
			_ = a.ghosttyRuntime.Close()
		}
		return nil, err
	}
	a.nextID++
	leaf := &node{pane: firstPane}
	a.root = leaf
	a.active = leaf
	a.paneScroll[firstPane.id] = 0
	return a, nil
}

func (a *app) requestRedraw() {
	select {
	case a.redrawCh <- struct{}{}:
	default:
	}
}

func (a *app) close() {
	var leaves []*node
	a.root.walkLeaves(&leaves)
	for _, n := range leaves {
		n.pane.close()
	}
	a.screen.Fini()
	if a.ghosttyRuntime != nil {
		_ = a.ghosttyRuntime.Close()
	}
}

func (a *app) run() {
	defer a.close()

	eventCh := make(chan tcell.Event, 32)
	go func() {
		for {
			ev := a.screen.PollEvent()
			if ev == nil {
				close(eventCh)
				return
			}
			eventCh <- ev
		}
	}()

	a.render()
	for !a.quitting {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				return
			}
			a.handleEvent(ev)
		case <-a.redrawCh:
			a.render()
		case out := <-a.outputCh:
			a.handlePaneOutput(out)
		}
	}
}

func (a *app) handleEvent(ev tcell.Event) {
	switch e := ev.(type) {
	case *tcell.EventResize:
		a.resizeEvents++
		a.screen.Sync()
		a.render()
	case *tcell.EventMouse:
		a.mouseEvents++
		if a.handleMouse(e) {
			a.render()
		}
	case *tcell.EventKey:
		a.keyEvents++
		if a.handleShortcut(e) {
			a.render()
			return
		}
		if a.active != nil && a.active.pane != nil {
			a.scrollActiveToBottom()
			a.active.pane.writeKey(e)
		}
	}
}

func (a *app) handleShortcut(k *tcell.EventKey) bool {
	if k.Modifiers()&tcell.ModShift != 0 {
		switch k.Key() {
		case tcell.KeyPgUp:
			return a.scrollActive(mouseScrollStep * 4)
		case tcell.KeyPgDn:
			return a.scrollActive(-mouseScrollStep * 4)
		case tcell.KeyHome:
			return a.scrollActiveToTop()
		case tcell.KeyEnd:
			return a.scrollActiveToBottom()
		}
	}

	if k.Modifiers()&tcell.ModAlt != 0 && k.Key() == tcell.KeyRune {
		switch k.Rune() {
		case 'q':
			a.quitting = true
			return true
		case 'w':
			a.closeActivePane()
			return true
		case 'v':
			a.splitActive(splitVertical)
			return true
		case 'h':
			a.splitActive(splitHorizontal)
			return true
		case 'n':
			a.cycle(1)
			return true
		case 'p':
			a.cycle(-1)
			return true
		default:
			return false
		}
	}

	return false
}

func (a *app) handleMouse(m *tcell.EventMouse) bool {
	btn := m.Buttons()
	x, y := m.Position()
	target := a.paneAt(x, y)
	changed := false

	if target != nil && btn&tcell.Button1 != 0 && target != a.active {
		a.active = target
		changed = true
	}

	if btn&tcell.WheelUp != 0 {
		if target != nil {
			a.active = target
		}
		if a.scrollActive(mouseScrollStep) {
			changed = true
		}
	}

	if btn&tcell.WheelDown != 0 {
		if target != nil {
			a.active = target
		}
		if a.scrollActive(-mouseScrollStep) {
			changed = true
		}
	}

	return changed
}

func (a *app) paneAt(x, y int) *node {
	sw, sh := a.screen.Size()
	topRows := a.topBarRows()
	if x < 0 || y < topRows || x >= sw || y >= sh || a.root == nil || sh <= topRows {
		return nil
	}
	layoutRegion := rect{x: 0, y: topRows, w: sw, h: sh - topRows}
	var hit *node
	a.root.layout(layoutRegion, func(n *node, r rect) {
		if hit != nil {
			return
		}
		if x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h {
			hit = n
		}
	})
	return hit
}

func (a *app) scrollActive(delta int) bool {
	if a.active == nil || a.active.pane == nil || delta == 0 {
		return false
	}

	a.ensurePaneScroll()
	id := a.active.pane.id
	next := a.paneScroll[id] + delta
	if next < 0 {
		next = 0
	}
	if next == a.paneScroll[id] {
		return false
	}
	a.paneScroll[id] = next
	return true
}

func (a *app) scrollActiveToTop() bool {
	if a.active == nil || a.active.pane == nil {
		return false
	}
	a.ensurePaneScroll()
	id := a.active.pane.id
	a.paneScroll[id] = maxScrollbackLines
	return true
}

func (a *app) scrollActiveToBottom() bool {
	if a.active == nil || a.active.pane == nil {
		return false
	}
	a.ensurePaneScroll()
	id := a.active.pane.id
	if a.paneScroll[id] == 0 {
		return false
	}
	a.paneScroll[id] = 0
	return true
}

func (a *app) splitActive(orientation splitOrientation) {
	if a.active == nil || a.active.pane == nil {
		return
	}

	newPaneID := a.nextID
	newPane, err := newPane(newPaneID, a.requestRedraw, a.ghosttyRuntime, func(lines int) {
		a.notifyPaneOutput(newPaneID, lines)
	})
	if err != nil {
		return
	}
	a.nextID++
	a.active = a.active.split(orientation, newPane)
	a.ensurePaneScroll()
	a.paneScroll[newPane.id] = 0
}

func (a *app) notifyPaneOutput(paneID, lines int) {
	if lines <= 0 {
		return
	}
	select {
	case a.outputCh <- paneOutput{paneID: paneID, lines: lines}:
	default:
	}
}

func (a *app) handlePaneOutput(out paneOutput) {
	if out.lines <= 0 {
		return
	}
	a.outputLines += uint64(out.lines)
	a.ensurePaneScroll()
	if a.paneScroll[out.paneID] > 0 {
		a.paneScroll[out.paneID] += out.lines
	}
}

func (a *app) cycle(dir int) {
	var leaves []*node
	a.root.walkLeaves(&leaves)
	if len(leaves) < 2 || a.active == nil {
		return
	}

	idx := 0
	for i, n := range leaves {
		if n == a.active {
			idx = i
			break
		}
	}

	next := (idx + dir + len(leaves)) % len(leaves)
	a.active = leaves[next]
}

func (a *app) closeActivePane() {
	if a.active == nil || a.active.pane == nil {
		return
	}

	// Keep at least one pane alive.
	if a.active.parent == nil {
		return
	}

	toClose := a.active
	parent := toClose.parent

	var sibling *node
	if parent.first == toClose {
		sibling = parent.second
	} else {
		sibling = parent.first
	}
	if sibling == nil {
		return
	}

	grand := parent.parent
	if grand == nil {
		a.root = sibling
		sibling.parent = nil
	} else {
		if grand.first == parent {
			grand.first = sibling
		} else {
			grand.second = sibling
		}
		sibling.parent = grand
	}

	toClose.pane.close()
	a.ensurePaneScroll()
	delete(a.paneScroll, toClose.pane.id)
	a.active = sibling.firstLeaf()
}

func (a *app) render() {
	style := tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite)
	a.screen.SetStyle(style)
	a.screen.Clear()

	sw, sh := a.screen.Size()
	if sw <= 0 || sh <= 0 {
		a.screen.Show()
		return
	}

	help := "speedmux - by webforspeed | Alt+v split vertical | Alt+h split horizontal | Alt+w close pane | Alt+n next pane | Alt+p prev pane | Shift+PgUp/PgDn scroll | Alt+q quit"
	drawText(a.screen, 0, 0, sw, help, style.Foreground(tcell.ColorAqua))

	topRows := a.topBarRows()
	if a.statsEnabled {
		a.noteFrame(sw, sh)
		if sh > 1 {
			drawText(a.screen, 0, 1, sw, a.statsLine(sw, sh), style.Foreground(tcell.ColorGreen))
		}
	}

	if sh <= topRows {
		a.screen.Show()
		return
	}

	layoutRegion := rect{x: 0, y: topRows, w: sw, h: sh - topRows}
	a.root.layout(layoutRegion, func(n *node, r rect) {
		a.drawPane(n, r)
	})

	a.screen.Show()
}

func (a *app) drawPane(n *node, r rect) {
	if n == nil || n.pane == nil || r.w <= 0 || r.h <= 0 {
		return
	}

	borderStyle := tcell.StyleDefault.Foreground(tcell.ColorSilver)
	if n == a.active {
		borderStyle = borderStyle.Foreground(tcell.ColorYellow)
	}

	drawBox(a.screen, r, borderStyle)
	title := fmt.Sprintf(" pane %d ", n.pane.id)
	drawText(a.screen, r.x+2, r.y, r.w-4, title, borderStyle)

	innerW := r.w - 2
	innerH := r.h - 2
	if innerW <= 0 || innerH <= 0 {
		return
	}

	n.pane.resize(innerW, innerH)
	a.ensurePaneScroll()
	scroll := a.paneScroll[n.pane.id]
	lines, clamped := n.pane.visibleLinesWithScroll(innerW, innerH, scroll)
	a.paneScroll[n.pane.id] = clamped

	for i := 0; i < innerH; i++ {
		y := r.y + 1 + i
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		drawText(a.screen, r.x+1, y, innerW, line, tcell.StyleDefault.Foreground(tcell.ColorWhite))
	}
}

func (a *app) ensurePaneScroll() {
	if a.paneScroll == nil {
		a.paneScroll = map[int]int{}
	}
}

func (a *app) topBarRows() int {
	if a.statsEnabled {
		return 2
	}
	return 1
}

func (a *app) noteFrame(sw, sh int) {
	now := time.Now()
	if a.statsStartedAt.IsZero() {
		a.statsStartedAt = now
	}
	if !a.lastFrameAt.IsZero() {
		dt := now.Sub(a.lastFrameAt).Seconds()
		if dt > 0 {
			fps := 1.0 / dt
			const alpha = 0.2
			if a.smoothedFPS == 0 {
				a.smoothedFPS = fps
			} else {
				a.smoothedFPS = a.smoothedFPS*(1-alpha) + fps*alpha
			}
		}
	}
	a.lastFrameAt = now
	a.renderCount++

	if sw < 0 {
		sw = 0
	}
	if sh < 0 {
		sh = 0
	}
	a.lastFrameCells = sw * sh
	a.totalCells += uint64(a.lastFrameCells)
}

func (a *app) averageFPS() float64 {
	if a.statsStartedAt.IsZero() || a.renderCount == 0 {
		return 0
	}
	seconds := time.Since(a.statsStartedAt).Seconds()
	if seconds <= 0 {
		return 0
	}
	return float64(a.renderCount) / seconds
}

func (a *app) treeStats() (panes, splits int) {
	var walk func(*node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.isLeaf() {
			panes++
			return
		}
		splits++
		walk(n.first)
		walk(n.second)
	}
	walk(a.root)
	return panes, splits
}

func (a *app) statsLine(sw, sh int) string {
	panes, splits := a.treeStats()
	activePane := 0
	activeScroll := 0
	if a.active != nil && a.active.pane != nil {
		activePane = a.active.pane.id
		a.ensurePaneScroll()
		activeScroll = a.paneScroll[activePane]
	}

	backend := "basic"
	if a.ghosttyRuntime != nil {
		backend = "ghostty-vt"
	}

	return fmt.Sprintf(
		"STATS ON | FPS %.1f avg %.1f | panes %d splits %d active %d scroll %d | frame %dx%d (%d cells) totalCells %d | events key:%d mouse:%d resize:%d | outLines %d | backend %s",
		a.smoothedFPS,
		a.averageFPS(),
		panes,
		splits,
		activePane,
		activeScroll,
		sw,
		sh,
		a.lastFrameCells,
		a.totalCells,
		a.keyEvents,
		a.mouseEvents,
		a.resizeEvents,
		a.outputLines,
		backend,
	)
}

func keyToBytes(k *tcell.EventKey) []byte {
	if k == nil {
		return nil
	}

	// Control and ASCII-special keys map directly to single-byte C0 control codes.
	if k.Key() < tcell.KeyRune {
		return applyMetaPrefix([]byte{byte(k.Key())}, k.Modifiers())
	}

	switch k.Key() {
	case tcell.KeyRune:
		if k.Modifiers()&tcell.ModCtrl != 0 {
			if b, ok := ctrlRune(k.Rune()); ok {
				return applyMetaPrefix([]byte{b}, k.Modifiers())
			}
		}
		return applyMetaPrefix([]byte(string(k.Rune())), k.Modifiers())
	case tcell.KeyUp:
		return applyMetaPrefix([]byte("\x1b[A"), k.Modifiers())
	case tcell.KeyDown:
		return applyMetaPrefix([]byte("\x1b[B"), k.Modifiers())
	case tcell.KeyRight:
		return applyMetaPrefix([]byte("\x1b[C"), k.Modifiers())
	case tcell.KeyLeft:
		return applyMetaPrefix([]byte("\x1b[D"), k.Modifiers())
	case tcell.KeyHome:
		return applyMetaPrefix([]byte("\x1b[H"), k.Modifiers())
	case tcell.KeyEnd:
		return applyMetaPrefix([]byte("\x1b[F"), k.Modifiers())
	case tcell.KeyInsert:
		return applyMetaPrefix([]byte("\x1b[2~"), k.Modifiers())
	case tcell.KeyPgUp:
		return applyMetaPrefix([]byte("\x1b[5~"), k.Modifiers())
	case tcell.KeyPgDn:
		return applyMetaPrefix([]byte("\x1b[6~"), k.Modifiers())
	case tcell.KeyDelete:
		return applyMetaPrefix([]byte("\x1b[3~"), k.Modifiers())
	case tcell.KeyBacktab:
		return applyMetaPrefix([]byte("\x1b[Z"), k.Modifiers())
	case tcell.KeyF1:
		return applyMetaPrefix([]byte("\x1bOP"), k.Modifiers())
	case tcell.KeyF2:
		return applyMetaPrefix([]byte("\x1bOQ"), k.Modifiers())
	case tcell.KeyF3:
		return applyMetaPrefix([]byte("\x1bOR"), k.Modifiers())
	case tcell.KeyF4:
		return applyMetaPrefix([]byte("\x1bOS"), k.Modifiers())
	case tcell.KeyF5:
		return applyMetaPrefix([]byte("\x1b[15~"), k.Modifiers())
	case tcell.KeyF6:
		return applyMetaPrefix([]byte("\x1b[17~"), k.Modifiers())
	case tcell.KeyF7:
		return applyMetaPrefix([]byte("\x1b[18~"), k.Modifiers())
	case tcell.KeyF8:
		return applyMetaPrefix([]byte("\x1b[19~"), k.Modifiers())
	case tcell.KeyF9:
		return applyMetaPrefix([]byte("\x1b[20~"), k.Modifiers())
	case tcell.KeyF10:
		return applyMetaPrefix([]byte("\x1b[21~"), k.Modifiers())
	case tcell.KeyF11:
		return applyMetaPrefix([]byte("\x1b[23~"), k.Modifiers())
	case tcell.KeyF12:
		return applyMetaPrefix([]byte("\x1b[24~"), k.Modifiers())
	default:
		return nil
	}
}

func shouldEnableGhosttyVT() bool {
	return strings.TrimSpace(os.Getenv("MULTIPLEXER_GHOSTTY_VT")) != "0"
}

func shouldEnableStats() bool {
	switch strings.ToUpper(strings.TrimSpace(os.Getenv("STATS"))) {
	case "1", "TRUE", "YES", "ON":
		return true
	default:
		return false
	}
}

func (p *pane) writeKey(k *tcell.EventKey) {
	if encoded, ok := p.encodeKeyGhostty(k); ok {
		p.writeInput(encoded)
		return
	}
	p.writeInput(keyToBytes(k))
}

func (p *pane) encodeKeyGhostty(k *tcell.EventKey) ([]byte, bool) {
	if p.ghosttyTerm == nil || k == nil {
		return nil, false
	}

	keyCode, mods, ok := tcellToGhosttyKey(k)
	if !ok {
		return nil, false
	}

	data, err := p.ghosttyTerm.EncodeKey(context.Background(), keyCode, mods)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

func tcellToGhosttyKey(k *tcell.EventKey) (libghostty.KeyCode, libghostty.Modifier, bool) {
	mods := toGhosttyMods(k.Modifiers())
	switch k.Key() {
	case tcell.KeyRune:
		return libghostty.KeyCode(k.Rune()), mods, true
	case tcell.KeyUp:
		return libghostty.KeyUp, mods, true
	case tcell.KeyDown:
		return libghostty.KeyDown, mods, true
	case tcell.KeyRight:
		return libghostty.KeyRight, mods, true
	case tcell.KeyLeft:
		return libghostty.KeyLeft, mods, true
	case tcell.KeyHome:
		return libghostty.KeyHome, mods, true
	case tcell.KeyEnd:
		return libghostty.KeyEnd, mods, true
	case tcell.KeyInsert:
		return libghostty.KeyInsert, mods, true
	case tcell.KeyDelete:
		return libghostty.KeyDelete, mods, true
	case tcell.KeyPgUp:
		return libghostty.KeyPageUp, mods, true
	case tcell.KeyPgDn:
		return libghostty.KeyPageDown, mods, true
	case tcell.KeyF1:
		return libghostty.KeyF1, mods, true
	case tcell.KeyF2:
		return libghostty.KeyF2, mods, true
	case tcell.KeyF3:
		return libghostty.KeyF3, mods, true
	case tcell.KeyF4:
		return libghostty.KeyF4, mods, true
	case tcell.KeyF5:
		return libghostty.KeyF5, mods, true
	case tcell.KeyF6:
		return libghostty.KeyF6, mods, true
	case tcell.KeyF7:
		return libghostty.KeyF7, mods, true
	case tcell.KeyF8:
		return libghostty.KeyF8, mods, true
	case tcell.KeyF9:
		return libghostty.KeyF9, mods, true
	case tcell.KeyF10:
		return libghostty.KeyF10, mods, true
	case tcell.KeyF11:
		return libghostty.KeyF11, mods, true
	case tcell.KeyF12:
		return libghostty.KeyF12, mods, true
	case tcell.KeyEnter:
		return libghostty.KeyEnter, mods, true
	case tcell.KeyEscape:
		return libghostty.KeyEscape, mods, true
	case tcell.KeyTab:
		return libghostty.KeyTab, mods, true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		return libghostty.KeyBackspace, mods, true
	default:
		return 0, 0, false
	}
}

func toGhosttyMods(mods tcell.ModMask) libghostty.Modifier {
	var out libghostty.Modifier
	if mods&tcell.ModShift != 0 {
		out |= libghostty.ModShift
	}
	if mods&tcell.ModCtrl != 0 {
		out |= libghostty.ModCtrl
	}
	if mods&tcell.ModAlt != 0 {
		out |= libghostty.ModAlt
	}
	// Most terminal stacks encode Meta as Alt+key.
	if mods&tcell.ModMeta != 0 {
		out |= libghostty.ModAlt
	}
	return out
}

func ctrlRune(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r - 'a' + 1), true
	case r >= 'A' && r <= 'Z':
		return byte(r - 'A' + 1), true
	}

	switch r {
	case ' ', '@', '`':
		return 0x00, true
	case '[':
		return 0x1b, true
	case '\\':
		return 0x1c, true
	case ']':
		return 0x1d, true
	case '^':
		return 0x1e, true
	case '_':
		return 0x1f, true
	case '?':
		return 0x7f, true
	default:
		return 0, false
	}
}

func applyMetaPrefix(data []byte, mods tcell.ModMask) []byte {
	if len(data) == 0 {
		return nil
	}
	if mods&(tcell.ModAlt|tcell.ModMeta) == 0 {
		return data
	}
	out := make([]byte, 0, len(data)+1)
	out = append(out, 0x1b)
	out = append(out, data...)
	return out
}

func drawBox(s tcell.Screen, r rect, style tcell.Style) {
	if r.w <= 1 || r.h <= 1 {
		return
	}

	for x := r.x + 1; x < r.x+r.w-1; x++ {
		s.SetContent(x, r.y, tcell.RuneHLine, nil, style)
		s.SetContent(x, r.y+r.h-1, tcell.RuneHLine, nil, style)
	}
	for y := r.y + 1; y < r.y+r.h-1; y++ {
		s.SetContent(r.x, y, tcell.RuneVLine, nil, style)
		s.SetContent(r.x+r.w-1, y, tcell.RuneVLine, nil, style)
	}
	s.SetContent(r.x, r.y, tcell.RuneULCorner, nil, style)
	s.SetContent(r.x+r.w-1, r.y, tcell.RuneURCorner, nil, style)
	s.SetContent(r.x, r.y+r.h-1, tcell.RuneLLCorner, nil, style)
	s.SetContent(r.x+r.w-1, r.y+r.h-1, tcell.RuneLRCorner, nil, style)
}

func drawText(s tcell.Screen, x, y, width int, text string, style tcell.Style) {
	if width <= 0 {
		return
	}

	trimmed := truncateRunes(text, width)
	runes := []rune(trimmed)
	for i := 0; i < width; i++ {
		ch := ' '
		if i < len(runes) {
			ch = runes[i]
		}
		s.SetContent(x+i, y, ch, nil, style)
	}
}

func truncateRunes(in string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(strings.ReplaceAll(in, "\x00", ""))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}

func main() {
	a, err := newApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start: %v\n", err)
		os.Exit(1)
	}
	a.run()
}
