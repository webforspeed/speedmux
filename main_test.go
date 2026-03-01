package main

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"code.selman.me/hauntty/libghostty"
	"github.com/gdamore/tcell/v2"
)

func leaf(id int) *node {
	return &node{pane: &pane{id: id}}
}

func collectLeafRects(root *node, bounds rect) map[int]rect {
	out := make(map[int]rect)
	root.layout(bounds, func(n *node, r rect) {
		out[n.pane.id] = r
	})
	return out
}

func rectsOverlap(a, b rect) bool {
	return a.x < b.x+b.w && b.x < a.x+a.w && a.y < b.y+b.h && b.y < a.y+a.h
}

func assertRectPackCovers(t *testing.T, bounds rect, rects map[int]rect, ids []int) {
	t.Helper()

	totalArea := 0
	for _, id := range ids {
		r, ok := rects[id]
		if !ok {
			t.Fatalf("missing rect for pane %d", id)
		}
		if r.w <= 0 || r.h <= 0 {
			t.Fatalf("pane %d has non-positive size: %+v", id, r)
		}
		if r.x < bounds.x || r.y < bounds.y || r.x+r.w > bounds.x+bounds.w || r.y+r.h > bounds.y+bounds.h {
			t.Fatalf("pane %d is out of bounds: %+v bounds=%+v", id, r, bounds)
		}
		totalArea += r.w * r.h
	}

	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a := rects[ids[i]]
			b := rects[ids[j]]
			if rectsOverlap(a, b) {
				t.Fatalf("rects overlap for panes %d and %d: %+v %+v", ids[i], ids[j], a, b)
			}
		}
	}

	if got, want := totalArea, bounds.w*bounds.h; got != want {
		t.Fatalf("packed area mismatch: got %d want %d", got, want)
	}
}

func TestAppendOutput_CRLFPreservesLines(t *testing.T) {
	p := &pane{}
	p.appendOutput([]byte("echo hello\r\nhello\r\n"))

	if got, want := len(p.lines), 2; got != want {
		t.Fatalf("lines len: got %d want %d", got, want)
	}
	if got, want := p.lines[0], "echo hello"; got != want {
		t.Fatalf("first line: got %q want %q", got, want)
	}
	if got, want := p.lines[1], "hello"; got != want {
		t.Fatalf("second line: got %q want %q", got, want)
	}
	if got := string(p.curr); got != "" {
		t.Fatalf("curr not cleared: %q", got)
	}
	if p.sawCR {
		t.Fatal("expected sawCR=false after CRLF handling")
	}
}

func TestAppendOutput_StandaloneCRRewindsLine(t *testing.T) {
	p := &pane{}
	p.appendOutput([]byte("hello\rworld\n"))

	if got, want := len(p.lines), 1; got != want {
		t.Fatalf("lines len: got %d want %d", got, want)
	}
	if got, want := p.lines[0], "world"; got != want {
		t.Fatalf("line: got %q want %q", got, want)
	}
}

func TestAppendOutput_StripsANSI(t *testing.T) {
	p := &pane{}
	p.appendOutput([]byte("\x1b[31mred\x1b[0m\n"))

	if got, want := len(p.lines), 1; got != want {
		t.Fatalf("lines len: got %d want %d", got, want)
	}
	if got, want := p.lines[0], "red"; got != want {
		t.Fatalf("line: got %q want %q", got, want)
	}
}

func TestNodeSplitAndWalkLeaves(t *testing.T) {
	root := leaf(1)
	newLeaf := leaf(2)

	focused := root.split(splitVertical, newLeaf.pane)
	if focused == nil {
		t.Fatal("split returned nil")
	}
	if root.pane != nil {
		t.Fatal("root should be internal node after split")
	}
	if root.first == nil || root.second == nil {
		t.Fatal("split children not set")
	}
	if got, want := root.first.pane.id, 1; got != want {
		t.Fatalf("first leaf id: got %d want %d", got, want)
	}
	if got, want := root.second.pane.id, 2; got != want {
		t.Fatalf("second leaf id: got %d want %d", got, want)
	}

	var leaves []*node
	root.walkLeaves(&leaves)
	if got, want := len(leaves), 2; got != want {
		t.Fatalf("leaves len: got %d want %d", got, want)
	}
	if leaves[0].pane.id != 1 || leaves[1].pane.id != 2 {
		t.Fatalf("leaf order mismatch: got [%d,%d] want [1,2]", leaves[0].pane.id, leaves[1].pane.id)
	}
}

func TestNodeLayout_MixedSplitGeometry(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	root.first.split(splitHorizontal, &pane{id: 3})

	bounds := rect{x: 0, y: 0, w: 80, h: 24}
	rects := collectLeafRects(root, bounds)
	assertRectPackCovers(t, bounds, rects, []int{1, 2, 3})

	leftTop := rects[1]
	leftBottom := rects[3]
	right := rects[2]

	if got, want := right.h, bounds.h; got != want {
		t.Fatalf("right pane height: got %d want %d", got, want)
	}
	if got, want := leftTop.x, bounds.x; got != want {
		t.Fatalf("left top x: got %d want %d", got, want)
	}
	if got, want := leftBottom.x, bounds.x; got != want {
		t.Fatalf("left bottom x: got %d want %d", got, want)
	}
	if leftTop.w != leftBottom.w {
		t.Fatalf("left column width mismatch: top=%d bottom=%d", leftTop.w, leftBottom.w)
	}
	if got, want := right.x, bounds.x+leftTop.w; got != want {
		t.Fatalf("right pane x: got %d want %d", got, want)
	}
	if got, want := leftTop.y+leftTop.h, leftBottom.y; got != want {
		t.Fatalf("left panes are not vertically adjacent: got %d want %d", got, want)
	}
}

func TestNodeLayout_HorizontalThenVerticalGeometry(t *testing.T) {
	root := leaf(1)
	root.split(splitHorizontal, &pane{id: 2})
	root.first.split(splitVertical, &pane{id: 3})

	bounds := rect{x: 5, y: 7, w: 81, h: 25}
	rects := collectLeafRects(root, bounds)
	assertRectPackCovers(t, bounds, rects, []int{1, 2, 3})

	topLeft := rects[1]
	topRight := rects[3]
	bottom := rects[2]

	if got, want := bottom.w, bounds.w; got != want {
		t.Fatalf("bottom pane width: got %d want %d", got, want)
	}
	if got, want := bottom.x, bounds.x; got != want {
		t.Fatalf("bottom pane x: got %d want %d", got, want)
	}
	if got, want := topLeft.h, topRight.h; got != want {
		t.Fatalf("top row heights differ: left=%d right=%d", got, want)
	}
	if got, want := topLeft.y, bounds.y; got != want {
		t.Fatalf("top left y: got %d want %d", got, want)
	}
	if got, want := topRight.y, bounds.y; got != want {
		t.Fatalf("top right y: got %d want %d", got, want)
	}
	if got, want := bounds.y+topLeft.h, bottom.y; got != want {
		t.Fatalf("bottom pane y: got %d want %d", got, want)
	}
}

func TestAppCycleWraps(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	root.second.split(splitHorizontal, &pane{id: 3})

	a := &app{root: root, active: root.first}
	a.cycle(1)
	if got, want := a.active.pane.id, 2; got != want {
		t.Fatalf("after first cycle: got %d want %d", got, want)
	}
	a.cycle(1)
	if got, want := a.active.pane.id, 3; got != want {
		t.Fatalf("after second cycle: got %d want %d", got, want)
	}
	a.cycle(1)
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("after wrap cycle: got %d want %d", got, want)
	}
}

func TestAppCyclePrevInverseAndWrap(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	root.first.split(splitHorizontal, &pane{id: 3})
	root.second.split(splitHorizontal, &pane{id: 4})

	a := &app{root: root, active: root.first.second}
	start := a.active

	a.cycle(1)
	a.cycle(-1)
	if a.active != start {
		t.Fatalf("cycle inverse failed: got pane %d want pane %d", a.active.pane.id, start.pane.id)
	}

	a.active = root.first.first
	a.cycle(-1)
	if got, want := a.active.pane.id, 4; got != want {
		t.Fatalf("reverse wrap: got %d want %d", got, want)
	}
	a.cycle(1)
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("forward wrap: got %d want %d", got, want)
	}
}

func TestCloseActivePane_CollapsesTree(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	a := &app{root: root, active: root.second}

	a.closeActivePane()

	if a.root == nil || a.root.pane == nil {
		t.Fatal("root should remain a leaf")
	}
	if got, want := a.root.pane.id, 1; got != want {
		t.Fatalf("root pane id: got %d want %d", got, want)
	}
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("active pane id: got %d want %d", got, want)
	}
	if a.root.parent != nil {
		t.Fatal("root parent should be nil")
	}
}

func TestCloseActivePane_CollapsesNestedBranch(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	root.first.split(splitHorizontal, &pane{id: 3})

	a := &app{root: root, active: root.first.second}
	a.closeActivePane()

	if a.root == nil || a.root.isLeaf() {
		t.Fatal("root should remain an internal split with two leaves")
	}
	if got, want := a.root.orientation, splitVertical; got != want {
		t.Fatalf("root orientation: got %v want %v", got, want)
	}
	if a.root.first == nil || a.root.first.pane == nil || a.root.second == nil || a.root.second.pane == nil {
		t.Fatal("expected both split children to be leaf panes")
	}
	if got, want := a.root.first.pane.id, 1; got != want {
		t.Fatalf("first pane id: got %d want %d", got, want)
	}
	if got, want := a.root.second.pane.id, 2; got != want {
		t.Fatalf("second pane id: got %d want %d", got, want)
	}
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("active pane id: got %d want %d", got, want)
	}
}

func TestCloseActivePane_OnlyPaneNoop(t *testing.T) {
	only := leaf(1)
	a := &app{root: only, active: only}

	a.closeActivePane()

	if got, want := a.root.pane.id, 1; got != want {
		t.Fatalf("root pane id: got %d want %d", got, want)
	}
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("active pane id: got %d want %d", got, want)
	}
}

func TestHandleShortcut_ClosePaneAltW(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	a := &app{root: root, active: root.second}

	used := a.handleShortcut(tcell.NewEventKey(tcell.KeyRune, 'w', tcell.ModAlt))
	if !used {
		t.Fatal("expected Alt+w to be handled")
	}
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("active pane id: got %d want %d", got, want)
	}
}

func TestHandleShortcut_NavigationQuitAndUnknown(t *testing.T) {
	root := leaf(1)
	root.split(splitVertical, &pane{id: 2})
	a := &app{root: root, active: root.first}

	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModAlt)); !used {
		t.Fatal("expected Alt+n to be handled")
	}
	if got, want := a.active.pane.id, 2; got != want {
		t.Fatalf("after Alt+n: got %d want %d", got, want)
	}
	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModAlt)); !used {
		t.Fatal("expected Alt+p to be handled")
	}
	if got, want := a.active.pane.id, 1; got != want {
		t.Fatalf("after Alt+p: got %d want %d", got, want)
	}
	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModAlt)); !used {
		t.Fatal("expected Alt+q to be handled")
	}
	if !a.quitting {
		t.Fatal("expected quitting=true after Alt+q")
	}
	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModAlt)); used {
		t.Fatal("expected unknown Alt+key to be unhandled")
	}
	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyRune, 'w', tcell.ModNone)); used {
		t.Fatal("expected plain 'w' to be unhandled")
	}
}

func TestHandleShortcut_ScrollBindings(t *testing.T) {
	root := leaf(1)
	a := &app{root: root, active: root, paneScroll: map[int]int{}}

	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyPgUp, 0, tcell.ModShift)); !used {
		t.Fatal("expected Shift+PgUp to be handled")
	}
	if got, want := a.paneScroll[1], mouseScrollStep*4; got != want {
		t.Fatalf("after Shift+PgUp: got %d want %d", got, want)
	}

	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModShift)); !used {
		t.Fatal("expected Shift+PgDn to be handled")
	}
	if got, want := a.paneScroll[1], 0; got != want {
		t.Fatalf("after Shift+PgDn: got %d want %d", got, want)
	}

	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModShift)); !used {
		t.Fatal("expected Shift+Home to be handled")
	}
	if got, want := a.paneScroll[1], maxScrollbackLines; got != want {
		t.Fatalf("after Shift+Home: got %d want %d", got, want)
	}

	if used := a.handleShortcut(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModShift)); !used {
		t.Fatal("expected Shift+End to be handled")
	}
	if got, want := a.paneScroll[1], 0; got != want {
		t.Fatalf("after Shift+End: got %d want %d", got, want)
	}
}

func TestHandlePaneOutput_KeepsScrolledViewportAnchored(t *testing.T) {
	a := &app{paneScroll: map[int]int{1: 5, 2: 0}}

	a.handlePaneOutput(paneOutput{paneID: 1, lines: 3})
	if got, want := a.paneScroll[1], 8; got != want {
		t.Fatalf("scrolled pane offset: got %d want %d", got, want)
	}

	a.handlePaneOutput(paneOutput{paneID: 2, lines: 3})
	if got, want := a.paneScroll[2], 0; got != want {
		t.Fatalf("bottom-follow pane offset: got %d want %d", got, want)
	}
}

func TestAppendOutput_TabsBackspaceAndScrollbackLimit(t *testing.T) {
	p := &pane{}
	p.appendOutput([]byte("ab\b\tc\n"))
	if got, want := p.lines[0], "a    c"; got != want {
		t.Fatalf("line edit handling: got %q want %q", got, want)
	}

	scroll := &pane{}
	for i := 0; i < maxScrollbackLines+5; i++ {
		scroll.appendOutput([]byte(fmt.Sprintf("line-%d\n", i)))
	}
	if got, want := len(scroll.lines), maxScrollbackLines; got != want {
		t.Fatalf("scrollback size: got %d want %d", got, want)
	}
	if got, want := scroll.lines[0], "line-5"; got != want {
		t.Fatalf("scrollback first line: got %q want %q", got, want)
	}
	if got, want := scroll.lines[len(scroll.lines)-1], fmt.Sprintf("line-%d", maxScrollbackLines+4); got != want {
		t.Fatalf("scrollback last line: got %q want %q", got, want)
	}
}

func TestVisibleLines_RespectsWidthAndHeight(t *testing.T) {
	p := &pane{
		lines: []string{"12345", "abc", "xyz"},
		curr:  []rune("tail"),
	}
	got := p.visibleLines(3, 2)
	if len(got) != 2 {
		t.Fatalf("visible lines len: got %d want 2", len(got))
	}
	if got[0] != "xyz" || got[1] != "tai" {
		t.Fatalf("visible lines mismatch: got %#v want [\"xyz\" \"tai\"]", got)
	}
}

func TestVisibleLinesWithScroll_AppliesOffsetAndClamp(t *testing.T) {
	p := &pane{
		lines: []string{"l1", "l2", "l3", "l4", "l5"},
		curr:  []rune("l6"),
	}

	lines, offset := p.visibleLinesWithScroll(10, 3, 0)
	if offset != 0 {
		t.Fatalf("offset: got %d want 0", offset)
	}
	if want := []string{"l4", "l5", "l6"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines mismatch: got %#v want %#v", lines, want)
	}

	lines, offset = p.visibleLinesWithScroll(10, 3, 2)
	if offset != 2 {
		t.Fatalf("offset: got %d want 2", offset)
	}
	if want := []string{"l2", "l3", "l4"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines mismatch: got %#v want %#v", lines, want)
	}

	lines, offset = p.visibleLinesWithScroll(10, 3, 999)
	if offset != 3 {
		t.Fatalf("offset clamp: got %d want 3", offset)
	}
	if want := []string{"l1", "l2", "l3"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines mismatch: got %#v want %#v", lines, want)
	}
}

func TestKeyToBytes_Mappings(t *testing.T) {
	tests := []struct {
		name string
		ev   *tcell.EventKey
		want []byte
	}{
		{
			name: "rune",
			ev:   tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone),
			want: []byte("a"),
		},
		{
			name: "enter",
			ev:   tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
			want: []byte("\r"),
		},
		{
			name: "up",
			ev:   tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone),
			want: []byte("\x1b[A"),
		},
		{
			name: "ctrl-c",
			ev:   tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone),
			want: []byte{0x03},
		},
		{
			name: "ctrl-r rune modifier",
			ev:   tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModCtrl),
			want: []byte{0x12},
		},
		{
			name: "alt rune",
			ev:   tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModAlt),
			want: []byte{0x1b, 'x'},
		},
		{
			name: "meta up",
			ev:   tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModMeta),
			want: []byte{0x1b, 0x1b, '[', 'A'},
		},
		{
			name: "f1",
			ev:   tcell.NewEventKey(tcell.KeyF1, 0, tcell.ModNone),
			want: []byte("\x1bOP"),
		},
		{
			name: "unknown",
			ev:   tcell.NewEventKey(tcell.KeyF13, 0, tcell.ModNone),
			want: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := keyToBytes(tc.ev)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("keyToBytes mismatch: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestShouldUseLoginShell(t *testing.T) {
	tests := []struct {
		shell string
		want  bool
	}{
		{shell: "/bin/zsh", want: true},
		{shell: "/bin/bash", want: true},
		{shell: "/usr/bin/fish", want: true},
		{shell: "/custom/nu", want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.shell, func(t *testing.T) {
			if got := shouldUseLoginShell(tc.shell); got != tc.want {
				t.Fatalf("shouldUseLoginShell(%q): got %v want %v", tc.shell, got, tc.want)
			}
		})
	}
}

func TestShouldEnableGhosttyVT(t *testing.T) {
	t.Setenv("MULTIPLEXER_GHOSTTY_VT", "")
	if !shouldEnableGhosttyVT() {
		t.Fatal("expected ghostty vt enabled by default")
	}

	t.Setenv("MULTIPLEXER_GHOSTTY_VT", "0")
	if shouldEnableGhosttyVT() {
		t.Fatal("expected ghostty vt disabled when env var is 0")
	}

	t.Setenv("MULTIPLEXER_GHOSTTY_VT", "1")
	if !shouldEnableGhosttyVT() {
		t.Fatal("expected ghostty vt enabled when env var is 1")
	}
}

func TestShouldEnableStats(t *testing.T) {
	t.Setenv("STATS", "")
	if shouldEnableStats() {
		t.Fatal("expected stats disabled by default")
	}

	t.Setenv("STATS", "ON")
	if !shouldEnableStats() {
		t.Fatal("expected stats enabled when env var is ON")
	}

	t.Setenv("STATS", "true")
	if !shouldEnableStats() {
		t.Fatal("expected stats enabled when env var is true")
	}

	t.Setenv("STATS", "0")
	if shouldEnableStats() {
		t.Fatal("expected stats disabled when env var is 0")
	}
}

func TestToGhosttyMods(t *testing.T) {
	got := toGhosttyMods(tcell.ModShift | tcell.ModCtrl | tcell.ModAlt)
	want := libghostty.ModShift | libghostty.ModCtrl | libghostty.ModAlt
	if got != want {
		t.Fatalf("toGhosttyMods mismatch: got %v want %v", got, want)
	}

	got = toGhosttyMods(tcell.ModMeta)
	if got != libghostty.ModAlt {
		t.Fatalf("meta should map to alt: got %v", got)
	}
}

func TestTcellToGhosttyKey(t *testing.T) {
	tests := []struct {
		name     string
		ev       *tcell.EventKey
		wantCode libghostty.KeyCode
		wantMods libghostty.Modifier
		wantOK   bool
	}{
		{
			name:     "rune ctrl",
			ev:       tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModCtrl),
			wantCode: libghostty.KeyCode('a'),
			wantMods: libghostty.ModCtrl,
			wantOK:   true,
		},
		{
			name:     "arrow up",
			ev:       tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModShift|tcell.ModAlt),
			wantCode: libghostty.KeyUp,
			wantMods: libghostty.ModShift | libghostty.ModAlt,
			wantOK:   true,
		},
		{
			name:     "enter",
			ev:       tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone),
			wantCode: libghostty.KeyEnter,
			wantMods: 0,
			wantOK:   true,
		},
		{
			name:   "unsupported",
			ev:     tcell.NewEventKey(tcell.KeyF13, 0, tcell.ModNone),
			wantOK: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			code, mods, ok := tcellToGhosttyKey(tc.ev)
			if ok != tc.wantOK {
				t.Fatalf("ok mismatch: got %v want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if code != tc.wantCode {
				t.Fatalf("code mismatch: got %v want %v", code, tc.wantCode)
			}
			if mods != tc.wantMods {
				t.Fatalf("mods mismatch: got %v want %v", mods, tc.wantMods)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	if got, want := truncateRunes("hello", 3), "hel"; got != want {
		t.Fatalf("truncate ascii: got %q want %q", got, want)
	}
	if got, want := truncateRunes("🙂🙂🙂", 2), "🙂🙂"; got != want {
		t.Fatalf("truncate runes: got %q want %q", got, want)
	}
}
