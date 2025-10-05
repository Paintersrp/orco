package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Paintersrp/orco/internal/engine"
)

func newTestUI(t *testing.T) *UI {
	t.Helper()
	app := tview.NewApplication()
	table := tview.NewTable().SetFixed(1, 1).SetSelectable(true, false)
	logs := tview.NewTextView()
	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(table, 0, 3, true).
		AddItem(logs, 0, 2, false)
	pages := tview.NewPages().AddPage("main", flex, true, true)

	ui := &UI{
		app:        app,
		pages:      pages,
		table:      table,
		logs:       logs,
		events:     make(chan engine.Event, 1),
		services:   make(map[string]*serviceState),
		logsPretty: true,
		maxLogs:    defaultLogRetention,
		done:       make(chan struct{}),
	}

	app.SetRoot(pages, true)
	app.SetInputCapture(ui.handleKey)

	return ui
}

func TestHandleKeyRespectsOverlayFocus(t *testing.T) {
	ui := newTestUI(t)
	ui.app.SetFocus(ui.table)

	slash := tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone)
	if res := ui.handleKey(slash); res != nil {
		t.Fatalf("expected filter shortcut to be consumed when table focused")
	}

	if _, ok := ui.app.GetFocus().(*tview.InputField); !ok {
		t.Fatalf("expected filter input to have focus, got %T", ui.app.GetFocus())
	}

	enter := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if res := ui.handleKey(enter); res != enter {
		t.Fatalf("expected Enter to bypass global handler when overlay focused")
	}

	runeEvent := tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)
	if res := ui.handleKey(runeEvent); res != runeEvent {
		t.Fatalf("expected rune to bypass global handler when overlay focused")
	}

	ui.pages.RemovePage(filterPageName)
	ui.app.SetFocus(ui.table)

	if res := ui.handleKey(runeEvent); res != runeEvent {
		t.Fatalf("expected rune to pass through when table focused")
	}
	if ui.logsFocused {
		t.Fatalf("expected logsFocused to match table focus")
	}
}

func TestHandleKeyAllowsLogShortcuts(t *testing.T) {
	ui := newTestUI(t)
	ui.app.SetFocus(ui.table)

	ui.toggleFocus()
	if ui.app.GetFocus() != ui.logs {
		t.Fatalf("expected logs to have focus after toggle")
	}

	slash := tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone)
	if res := ui.handleKey(slash); res != nil {
		t.Fatalf("expected filter shortcut to be consumed when logs focused")
	}
}
