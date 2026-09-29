package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogWatch shares one incremental Docker log poll between all viewers and push
// subscribers. Idle watches are released; subscribed servers run without a UI.
type LogWatch struct {
	api     *API
	push    *PushManager
	ctx     context.Context
	mu      sync.Mutex
	watches map[string]*serverLogWatch
}
type serverLogWatch struct {
	mu              sync.Mutex
	lines           []string
	cursor          time.Time
	boundary        map[string]int
	ready           chan struct{}
	lastRead        time.Time
	err             error
	started         time.Time
	composeRevision string
	args            []string
}
type playerEvent struct{ Server, Player, Action string }

var javaPlayerEvent = regexp.MustCompile(`^\[[^\]]+\] \[Server thread/INFO\](?: \[[^\]]+\])?: ([A-Za-z0-9_]{1,16}) (joined|left) the game$`)
var bedrockPlayerEvent = regexp.MustCompile(`^\[[^\]]+ INFO\] Player (connected|disconnected): (.+), xuid: [0-9]+(?:,.*)?$`)
var ansiLog = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func parsePlayerEvent(line string) (string, string, bool) {
	line = ansiLog.ReplaceAllString(strings.TrimSpace(line), "")
	if m := javaPlayerEvent.FindStringSubmatch(line); m != nil {
		return m[1], m[2], true
	}
	if m := bedrockPlayerEvent.FindStringSubmatch(line); m != nil {
		action := "joined"
		if m[1] == "disconnected" {
			action = "left"
		}
		return m[2], action, true
	}
	return "", "", false
}
func newLogWatch(ctx context.Context, api *API, push *PushManager) *LogWatch {
	return &LogWatch{api: api, push: push, ctx: ctx, watches: make(map[string]*serverLogWatch)}
}
func (l *LogWatch) ensure(name string) *serverLogWatch {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.watches[name]; w != nil {
		w.mu.Lock()
		w.lastRead = time.Now()
		w.mu.Unlock()
		return w
	}
	w := &serverLogWatch{ready: make(chan struct{}), lastRead: time.Now(), started: time.Now(), boundary: make(map[string]int)}
	l.watches[name] = w
	go l.run(name, w)
	return w
}
func (l *LogWatch) snapshot(ctx context.Context, name string) (string, error) {
	w := l.ensure(name)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-w.ready:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.lines, "\n"), w.err
}
func (l *LogWatch) run(name string, w *serverLogWatch) {
	defer func() {
		l.mu.Lock()
		if l.watches[name] == w {
			delete(l.watches, name)
		}
		l.mu.Unlock()
	}()
	first := true
	for {
		err := l.poll(name, w)
		w.mu.Lock()
		w.err = err
		w.mu.Unlock()
		if first {
			close(w.ready)
			first = false
		}
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		// Hold the registry lock while deciding to retire so ensure cannot attach
		// a reader to a watch that is about to disappear.
		l.mu.Lock()
		w.mu.Lock()
		idle := time.Since(w.lastRead) > time.Minute && !l.push.hasServer(name)
		w.mu.Unlock()
		if idle {
			delete(l.watches, name)
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}
func (l *LogWatch) poll(name string, w *serverLogWatch) error {
	file, err := l.api.composeFile(name)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	version := file + revision(content)
	if version != w.composeRevision {
		_, service, err := l.api.minecraftService(name)
		if err != nil {
			return fmt.Errorf("Server logs unavailable: %w", err)
		}
		args, err := l.api.composeArgs(name)
		if err != nil {
			return err
		}
		w.args = append(args, "logs", "--no-color", "--no-log-prefix", "--timestamps", service)
		w.composeRevision = version
	}
	args := append([]string(nil), w.args[:len(w.args)-1]...)
	if w.cursor.IsZero() {
		args = append(args, "--tail", "200")
	} else {
		args = append(args, "--since", w.cursor.Format(time.RFC3339Nano))
	}
	args = append(args, w.args[len(w.args)-1])
	ctx, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Docker logs unavailable: %w", err)
	}
	events := w.ingest(string(out), time.Now())
	for _, event := range events {
		event.Server = name
		l.push.enqueue(event)
	}
	return nil
}

// Docker's --since is inclusive. Counts at the cursor handle identical lines
// with identical timestamps without replaying notifications on the next poll.
func (w *serverLogWatch) ingest(output string, now time.Time) []playerEvent {
	type entry struct {
		stamp time.Time
		line  string
	}
	var entries []entry
	for _, raw := range strings.Split(output, "\n") {
		stamp, line, ok := strings.Cut(strings.TrimSuffix(raw, "\r"), " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			continue
		}
		entries = append(entries, entry{at, line})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].stamp.Before(entries[j].stamp) })
	w.mu.Lock()
	defer w.mu.Unlock()
	initial := w.cursor.IsZero()
	previous := w.cursor
	seen := make(map[string]int)
	var events []playerEvent
	for _, e := range entries {
		if e.stamp.Before(previous) {
			continue
		}
		if e.stamp.Equal(previous) {
			seen[e.line]++
			if seen[e.line] <= w.boundary[e.line] {
				continue
			}
		}
		if e.stamp.After(w.cursor) {
			w.cursor = e.stamp
			w.boundary = make(map[string]int)
		}
		w.boundary[e.line]++
		w.lines = append(w.lines, e.line)
		if !initial || !w.started.IsZero() && !e.stamp.Before(w.started) {
			if player, action, ok := parsePlayerEvent(e.line); ok {
				events = append(events, playerEvent{Player: player, Action: action})
			}
		}
	}
	if len(w.lines) > 200 {
		w.lines = append([]string(nil), w.lines[len(w.lines)-200:]...)
	}
	if w.cursor.IsZero() {
		w.cursor = now
	}
	return events
}
