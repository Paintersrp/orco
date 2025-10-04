package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/engine"
)

const (
	tableTitle          = "Services"
	logsTitle           = "Logs"
	filterPageName      = "filter"
	defaultLogRetention = 500
)

// Option configures UI behaviour.
type Option func(*UI)

// WithMaxLogs sets the maximum number of log entries retained for each service.
func WithMaxLogs(n int) Option {
	return func(u *UI) {
		if n > 0 {
			u.maxLogs = n
		}
	}
}

// UI coordinates the interactive status interface backed by tview.
type UI struct {
	app    *tview.Application
	pages  *tview.Pages
	table  *tview.Table
	logs   *tview.TextView
	events chan engine.Event

	services map[string]*serviceState

	visible     []string
	selected    string
	logsPretty  bool
	filter      string
	filterExpr  *regexp.Regexp
	logsFocused bool
	maxLogs     int

	mu sync.RWMutex

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	wg        sync.WaitGroup
	stopOnce  sync.Once
	closeOnce sync.Once
	done      chan struct{}
}

type serviceState struct {
	name      string
	firstSeen time.Time
	lastEvent time.Time
	state     engine.EventType
	ready     bool
	replicas  int
	restarts  int
	message   string

	logs []cliutil.LogRecord
}

// New constructs a UI configured with the supplied options.
func New(opts ...Option) *UI {
	app := tview.NewApplication()
	table := tview.NewTable().SetFixed(1, 1).SetSelectable(true, false)
	table.SetBorder(true).SetTitle(tableTitle)

	logs := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	logs.SetBorder(true).SetTitle(logsTitle)
	logs.SetChangedFunc(func() {
		app.Draw()
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(table, 0, 3, true).
		AddItem(logs, 0, 2, false)

	pages := tview.NewPages().AddPage("main", flex, true, true)

	ui := &UI{
		app:        app,
		pages:      pages,
		table:      table,
		logs:       logs,
		events:     make(chan engine.Event, 256),
		services:   make(map[string]*serviceState),
		logsPretty: true,
		maxLogs:    defaultLogRetention,
		done:       make(chan struct{}),
	}

	for _, opt := range opts {
		opt(ui)
	}

	table.SetSelectedFunc(func(row, column int) {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		ui.syncSelection(row)
		ui.renderLogsLocked()
	})

	table.SetSelectionChangedFunc(func(row, column int) {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		ui.syncSelection(row)
		ui.renderLogsLocked()
	})

	logs.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter || (event.Key() == tcell.KeyRune && event.Rune() == '\n') {
			ui.toggleFocus()
			return nil
		}
		return event
	})

	app.SetRoot(pages, true)
	app.SetInputCapture(ui.handleKey)

	ui.mu.Lock()
	ui.refreshTableLocked()
	ui.mu.Unlock()

	return ui
}

// EventSink exposes the channel where orchestrator events should be delivered.
func (u *UI) EventSink() chan<- engine.Event {
	return u.events
}

// CloseEvents releases the event channel, allowing internal goroutines to exit cleanly.
func (u *UI) CloseEvents() {
	u.closeOnce.Do(func() {
		close(u.events)
	})
}

// Done returns a channel that is closed when the UI stops.
func (u *UI) Done() <-chan struct{} {
	return u.done
}

// Run starts the tview application and processes incoming events until Stop is invoked
// or the provided context is cancelled.
func (u *UI) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)

	u.cancelMu.Lock()
	u.cancel = cancel
	u.cancelMu.Unlock()

	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		u.consumeEvents(ctx)
	}()

	go func() {
		<-ctx.Done()
		u.Stop()
	}()

	err := u.app.Run()

	u.cancelMu.Lock()
	cancel = u.cancel
	u.cancel = nil
	u.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}

	u.wg.Wait()
	u.Stop()

	return err
}

// Stop terminates the application loop and releases resources.
func (u *UI) Stop() {
	u.stopOnce.Do(func() {
		u.cancelMu.Lock()
		cancel := u.cancel
		u.cancel = nil
		u.cancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
		u.app.Stop()
		close(u.done)
	})
}

func (u *UI) consumeEvents(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	draining := false
	ctxDone := ctx.Done()

	for {
		var tick <-chan time.Time
		if !draining {
			tick = ticker.C
		}

		select {
		case <-ctxDone:
			if !draining {
				draining = true
				ticker.Stop()
			}
			ctxDone = nil
		case evt, ok := <-u.events:
			if !ok {
				return
			}
			if draining {
				continue
			}
			u.applyEvent(evt)
		case <-tick:
			if !draining {
				u.refreshAge()
			}
		}
	}
}

func (u *UI) handleKey(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEnter:
		u.toggleFocus()
		return nil
	case tcell.KeyUp, tcell.KeyDown:
		return event
	case tcell.KeyRune:
		switch event.Rune() {
		case 'q', 'Q':
			go u.Stop()
			return nil
		case '/':
			u.showFilterPrompt()
			return nil
		case 'j', 'J':
			u.toggleJSON()
			return nil
		}
	}
	return event
}

func (u *UI) toggleFocus() {
	if u.logsFocused {
		u.app.SetFocus(u.table)
	} else {
		u.app.SetFocus(u.logs)
	}
	u.logsFocused = !u.logsFocused
}

func (u *UI) toggleJSON() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.logsPretty = !u.logsPretty
	u.renderLogsLocked()
}

func (u *UI) showFilterPrompt() {
	u.mu.RLock()
	current := u.filter
	u.mu.RUnlock()

	input := tview.NewInputField().
		SetLabel("Regex filter: ").
		SetText(current).
		SetFieldWidth(40)

	form := tview.NewForm().
		AddFormItem(input).
		AddButton("Apply", func() {
			u.applyFilter(input.GetText())
			u.pages.RemovePage(filterPageName)
			u.app.SetFocus(u.table)
		}).
		AddButton("Cancel", func() {
			u.pages.RemovePage(filterPageName)
			u.app.SetFocus(u.table)
		})

	form.SetBorder(true).SetTitle("Filter Services")

	grid := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, 7, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	u.pages.AddPage(filterPageName, grid, true, true)
	u.app.SetFocus(input)
}

func (u *UI) applyFilter(expr string) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		u.mu.Lock()
		u.filter = ""
		u.filterExpr = nil
		u.mu.Unlock()
		u.queueRefresh(true)
		return
	}

	re, err := regexp.Compile(expr)
	if err != nil {
		u.showErrorModal(fmt.Sprintf("Invalid filter: %v", err))
		return
	}

	u.mu.Lock()
	u.filter = expr
	u.filterExpr = re
	u.mu.Unlock()
	u.queueRefresh(true)
}

func (u *UI) showErrorModal(message string) {
	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			u.pages.RemovePage(filterPageName)
			u.app.SetFocus(u.table)
		})

	// Ensure previous filter prompt is removed to avoid stacking pages.
	u.pages.RemovePage(filterPageName)
	u.pages.AddPage(filterPageName, modal, true, true)
}

func (u *UI) applyEvent(evt engine.Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	u.mu.Lock()

	state := u.services[evt.Service]
	if state == nil {
		state = &serviceState{name: evt.Service, firstSeen: evt.Timestamp}
		u.services[evt.Service] = state
	}
	state.lastEvent = evt.Timestamp
	if state.firstSeen.IsZero() {
		state.firstSeen = evt.Timestamp
	}

	if evt.Type != engine.EventTypeLog {
		state.state = evt.Type
		if evt.Type == engine.EventTypeReady {
			state.ready = true
		}
		if evt.Type == engine.EventTypeUnready || evt.Type == engine.EventTypeStopping || evt.Type == engine.EventTypeStopped || evt.Type == engine.EventTypeCrashed {
			state.ready = false
		}
		if evt.Type == engine.EventTypeCrashed {
			state.restarts++
		}
		if evt.Message != "" {
			state.message = evt.Message
		} else if evt.Err != nil {
			state.message = evt.Err.Error()
		} else {
			state.message = ""
		}
	}

	if evt.Replica+1 > state.replicas {
		state.replicas = evt.Replica + 1
	}

	if evt.Type == engine.EventTypeLog {
		record := cliutil.NewLogRecord(evt)
		state.logs = append(state.logs, record)
		if len(state.logs) > u.maxLogs {
			trim := len(state.logs) - u.maxLogs
			state.logs = append([]cliutil.LogRecord(nil), state.logs[trim:]...)
		}
	}

	selected := state.name == u.selected
	updateLogs := selected || u.selected == ""
	u.mu.Unlock()

	u.queueRefresh(updateLogs)
}

func (u *UI) refreshAge() {
	u.queueRefresh(false)
}

func (u *UI) queueRefresh(updateLogs bool) {
	u.app.QueueUpdateDraw(func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.refreshTableLocked()
		if updateLogs {
			u.renderLogsLocked()
		}
	})
}

func (u *UI) refreshTableLocked() {
	u.table.Clear()

	headers := []string{"SERVICE", "STATE", "READY", "REPL", "RESTARTS", "AGE", "MESSAGE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).
			SetSelectable(false).
			SetAttributes(tcell.AttrBold)
		u.table.SetCell(0, col, cell)
	}

	names := make([]string, 0, len(u.services))
	for name := range u.services {
		if u.filterExpr != nil && !u.filterExpr.MatchString(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	u.visible = names

	if u.filter != "" {
		u.table.SetTitle(fmt.Sprintf("%s /%s/", tableTitle, u.filter))
	} else {
		u.table.SetTitle(tableTitle)
	}

	for row, name := range names {
		state := u.services[name]
		age := "-"
		if !state.firstSeen.IsZero() {
			age = time.Since(state.firstSeen).Truncate(time.Second).String()
		}
		ready := "No"
		if state.ready {
			ready = "Yes"
		}
		repl := "-"
		if state.replicas > 0 {
			repl = fmt.Sprintf("%d", state.replicas)
		}
		message := state.message
		if len(message) > 80 {
			message = message[:77] + "..."
		}

		values := []string{
			name,
			formatState(state.state),
			ready,
			repl,
			fmt.Sprintf("%d", state.restarts),
			age,
			message,
		}
		for col, value := range values {
			cell := tview.NewTableCell(value)
			if col == 0 {
				cell = cell.SetReference(name)
			}
			u.table.SetCell(row+1, col, cell)
		}
	}

	u.ensureSelectionLocked()
}

func (u *UI) renderLogsLocked() {
	u.logs.Clear()
	var state *serviceState
	if u.selected != "" {
		state = u.services[u.selected]
	}
	if state == nil {
		u.logs.SetTitle(logsTitle)
		return
	}

	u.logs.SetTitle(fmt.Sprintf("%s (%s)", logsTitle, state.name))

	for _, record := range state.logs {
		var data []byte
		var err error
		if u.logsPretty {
			data, err = json.MarshalIndent(record, "", "  ")
		} else {
			data, err = json.Marshal(record)
		}
		if err != nil {
			fmt.Fprintf(u.logs, "{\"error\":\"%v\"}\n", err)
			continue
		}
		fmt.Fprintf(u.logs, "%s\n", data)
	}
	u.logs.ScrollToEnd()
}

func (u *UI) ensureSelectionLocked() {
	if len(u.visible) == 0 {
		u.selected = ""
		u.table.Select(0, 0)
		return
	}

	idx := 0
	if u.selected != "" {
		for i, name := range u.visible {
			if name == u.selected {
				idx = i
				break
			}
		}
	} else {
		u.selected = u.visible[0]
	}

	if idx >= len(u.visible) {
		idx = len(u.visible) - 1
	}
	if u.selected == "" && len(u.visible) > 0 {
		u.selected = u.visible[idx]
	}
	u.table.Select(idx+1, 0)
}

func (u *UI) syncSelection(row int) {
	if row <= 0 || row-1 >= len(u.visible) {
		return
	}
	u.selected = u.visible[row-1]
}

func formatState(t engine.EventType) string {
	if t == "" {
		return "-"
	}
	s := string(t)
	if len(s) <= 1 {
		return strings.ToUpper(s)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
