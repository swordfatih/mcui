package app

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestParsePlayerEvents(t *testing.T) {
	cases := []struct{ line, player, action string }{
		{"[12:03:05] [Server thread/INFO]: Alex joined the game", "Alex", "joined"},
		{"[12:03:05] [Server thread/INFO]: Alex left the game", "Alex", "left"},
		{"[12:03:05] [Server thread/INFO] [minecraft/MinecraftServer]: Alex joined the game", "Alex", "joined"},
		{"[2026-09-29 12:03:05:123 INFO] Player connected: My Friend, xuid: 12345, pfid: abc", "My Friend", "joined"},
		{"[2026-09-29 12:03:05:123 INFO] Player disconnected: My Friend, xuid: 12345, pfid: abc", "My Friend", "left"},
		{"\x1b[32m[12:03:05] [Server thread/INFO]: Alex left the game\x1b[0m", "Alex", "left"},
		{"[12:03:05] [Server thread/INFO]: <Steve> Alex joined the game", "", ""},
		{"[12:03:05] [Server thread/INFO]: [Not Secure] <Steve> Alex joined the game", "", ""},
		{"[2026-09-29 12:03:05:123 INFO] Player Spawned: My Friend xuid: 12345", "", ""},
		{"[12:03:05] [Server thread/INFO]: Alex lost connection: Disconnected", "", ""},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			player, action, ok := parsePlayerEvent(c.line)
			if player != c.player || action != c.action || ok != (c.player != "") {
				t.Fatalf("got %q %q %v", player, action, ok)
			}
		})
	}
}
func TestLogCursorReplayAndRing(t *testing.T) {
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	w := &serverLogWatch{boundary: make(map[string]int), started: base.Add(time.Second)}
	line := func(at time.Time, text string) string { return at.Format(time.RFC3339Nano) + " " + text + "\n" }
	join := "[12:00:00] [Server thread/INFO]: Alex joined the game"
	leave := "[12:00:00] [Server thread/INFO]: Alex left the game"
	history := line(base, join)
	if events := w.ingest(history, base); len(events) != 0 {
		t.Fatal("history generated alerts")
	}
	// Out-of-order Docker streams are ordered before advancing the cursor.
	fresh := line(base.Add(2*time.Second), leave) + line(base.Add(time.Second), join)
	events := w.ingest(history+fresh, base.Add(3*time.Second))
	if len(events) != 2 || events[0].Action != "joined" || events[1].Action != "left" {
		t.Fatalf("bad events: %+v", events)
	}
	if events := w.ingest(fresh, base); len(events) != 0 {
		t.Fatal("replayed cursor generated alerts")
	}
	if events := w.ingest(line(base.Add(2*time.Second), leave)+line(base.Add(2*time.Second), leave), base); len(events) != 1 {
		t.Fatalf("identical new boundary event lost: %+v", events)
	}
	if events := w.ingest(line(base.Add(2*time.Second), leave)+line(base.Add(2*time.Second), leave), base); len(events) != 0 {
		t.Fatal("identical boundary events repeated")
	}
	var output strings.Builder
	for i := 3; i < 260; i++ {
		output.WriteString(line(base.Add(time.Duration(i)*time.Second), "ordinary log"))
	}
	w.ingest(output.String(), base)
	if len(w.lines) != 200 {
		t.Fatalf("ring size %d", len(w.lines))
	}
}
func TestLogBootstrapIncludesEventsSinceWatchStarted(t *testing.T) {
	start := time.Now()
	w := &serverLogWatch{started: start, boundary: make(map[string]int)}
	events := w.ingest(start.Add(time.Second).Format(time.RFC3339Nano)+" [12:00:00] [Server thread/INFO]: Alex joined the game\n", start.Add(2*time.Second))
	if len(events) != 1 {
		t.Fatal("lost join during bootstrap")
	}
	empty := &serverLogWatch{started: start, boundary: make(map[string]int)}
	empty.ingest("", start)
	if empty.cursor != start {
		t.Fatal("empty log did not establish cursor")
	}
}
func testPushSubscription(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}}
}
func testPushManager(t *testing.T) (*API, *PushManager) {
	t.Helper()
	t.Setenv("MCUI_PUSH_CONTACT", "mailto:admin@example.com")
	a := &API{Root: t.TempDir()}
	p, err := newPushManager(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	a.push = p
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.logs = newLogWatch(ctx, a, p)
	p.watch = a.logs
	// Use ready watches so HTTP tests never launch Docker processes.
	for _, name := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(a.Root, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.Root, name, "compose.yaml"), []byte("services: {}"), 0644); err != nil {
			t.Fatal(err)
		}
		ready := make(chan struct{})
		close(ready)
		a.logs.watches[name] = &serverLogWatch{ready: ready}
	}
	return a, p
}
func pushRequest(a *API, body any, header bool) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/push/subscription", bytes.NewReader(data))
	if header {
		r.Header.Set("X-MCUI-Push", "1")
	}
	w := httptest.NewRecorder()
	a.pushHandler(w, r)
	return w
}
func TestPushPreferencesPersistAndUnsubscribeIndependently(t *testing.T) {
	a, p := testPushManager(t)
	sub := testPushSubscription(t, "https://push.example.com/device-a")
	request := map[string]any{"action": "subscribe", "server": "one", "subscription": sub}
	if w := pushRequest(a, request, false); w.Code != 403 {
		t.Fatalf("CSRF status: %d", w.Code)
	}
	for _, name := range []string{"one", "two"} {
		request["server"] = name
		if w := pushRequest(a, request, true); w.Code != 200 {
			t.Fatalf("subscribe: %d %s", w.Code, w.Body)
		}
	}
	if w := pushRequest(a, map[string]any{"action": "unsubscribe", "server": "one", "endpoint": sub.Endpoint}, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	restored, err := newPushManager(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if restored.state.PublicKey != p.state.PublicKey || restored.state.PrivateKey != p.state.PrivateKey {
		t.Fatal("VAPID keys changed on restart")
	}
	if restored.hasServer("one") || !restored.hasServer("two") {
		t.Fatal("unsubscribe affected the wrong server")
	}
	info, err := os.Stat(p.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("push state is not private")
	}
	w := pushRequest(a, map[string]any{"action": "status", "endpoint": "https://push.example.com/another-device"}, true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "two") {
		t.Fatal("device status leaked other subscriptions")
	}
	if w := pushRequest(a, map[string]any{"action": "unsubscribe", "server": "two", "endpoint": sub.Endpoint}, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if len(p.state.Devices) != 0 {
		t.Fatal("empty device retained")
	}
}
func TestPushSaveFailureRollsBack(t *testing.T) {
	a, p := testPushManager(t)
	p.path = filepath.Join(a.Root, "one", "compose.yaml", "state.json")
	sub := testPushSubscription(t, "https://push.example.com/device")
	w := pushRequest(a, map[string]any{"action": "subscribe", "server": "one", "subscription": sub}, true)
	if w.Code != 500 || p.hasServer("one") {
		t.Fatal("failed save changed preferences")
	}
}
func TestValidatePushAndNetworkTargets(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/push", "https://user:pass@example.com/push", "https://example.com:123/push", "https://example.com/push#secret", "file:///etc/passwd"} {
		if validPushEndpoint(endpoint) {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "224.0.0.1", "fd00::1", "::ffff:127.0.0.1"} {
		if publicPushIP(net.ParseIP(ip)) {
			t.Fatalf("accepted %s", ip)
		}
	}
	sub := testPushSubscription(t, "https://push.example.com/device")
	if err := validatePushSubscription(sub); err != nil {
		t.Fatal(err)
	}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
	if err := validatePushSubscription(sub); err == nil {
		t.Fatal("accepted invalid curve point")
	}
}

type fakePushClient struct {
	statuses  []int
	endpoints []string
	headers   []string
	encrypted bool
}

func (c *fakePushClient) Do(r *http.Request) (*http.Response, error) {
	c.endpoints = append(c.endpoints, r.URL.String())
	c.headers = append(c.headers, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	c.encrypted = len(body) > 0 && !bytes.Contains(body, []byte("Alex joined")) && strings.HasPrefix(r.Header.Get("Authorization"), "vapid ")
	if len(c.statuses) == 0 {
		return nil, errors.New("unexpected send")
	}
	status := c.statuses[0]
	c.statuses = c.statuses[1:]
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
}
func TestPushDeliveryEncryptedAndExpiredDeviceRemoved(t *testing.T) {
	_, p := testPushManager(t)
	sub := testPushSubscription(t, "https://push.example.com/device")
	p.state.Devices[sub.Endpoint] = pushDevice{Subscription: sub, Servers: map[string]bool{"one": true, "two": true}}
	client := &fakePushClient{statuses: []int{201, 410}}
	p.client = client
	p.deliver(context.Background(), playerEvent{Server: "one", Player: "Alex", Action: "joined"})
	if !client.encrypted || !p.hasServer("one") {
		t.Fatal("successful send was not encrypted or removed device")
	}
	p.deliver(context.Background(), playerEvent{Server: "two", Player: "Alex", Action: "left"})
	if len(p.state.Devices) != 0 {
		t.Fatal("expired endpoint retained")
	}
	restored, err := newPushManager(filepath.Dir(filepath.Dir(p.path)))
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.state.Devices) != 0 {
		t.Fatal("expired endpoint removal not persisted")
	}
	if !reflect.DeepEqual(client.endpoints, []string{sub.Endpoint, sub.Endpoint}) {
		t.Fatal("wrong delivery targets")
	}
}
func TestPushOnlySendsSubscribedServer(t *testing.T) {
	_, p := testPushManager(t)
	sub := testPushSubscription(t, "https://push.example.com/device")
	p.state.Devices[sub.Endpoint] = pushDevice{Subscription: sub, Servers: map[string]bool{"one": true}}
	client := &fakePushClient{}
	p.client = client
	p.deliver(context.Background(), playerEvent{Server: "two", Player: "Alex", Action: "joined"})
	if len(client.endpoints) != 0 {
		t.Fatal("sent unsubscribed server event")
	}
	p.enqueue(playerEvent{Server: "two"})
	if len(p.queue) != 0 {
		t.Fatal("queued unsubscribed event")
	}
}

func TestSharedLogSnapshotAndIncrementalPoll(t *testing.T) {
	a, p := testPushManager(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	logFile := filepath.Join(bin, "log")
	t.Setenv("MCUI_WATCH_CALLS", calls)
	t.Setenv("MCUI_WATCH_LOG", logFile)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$MCUI_WATCH_CALLS"
case " $* " in
 *" config "*) printf '%s\n' '{"services":{"mc":{"image":"itzg/minecraft-server:latest"}}}' ;;
 *" logs "*) cat "$MCUI_WATCH_LOG" >&2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	initial := "2026-09-29T12:00:00.000000001Z [12:00:00] [Server thread/INFO]: Alex joined the game\n"
	if err := os.WriteFile(logFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	sub := testPushSubscription(t, "https://push.example.com/device")
	p.state.Devices[sub.Endpoint] = pushDevice{Subscription: sub, Servers: map[string]bool{"one": true}}
	watch := a.logs.watches["one"]
	a.logs.ctx = context.Background()
	if err := a.logs.poll("one", watch); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		response := httptest.NewRecorder()
		a.serverDetails(response, httptest.NewRequest("GET", "/api/server-details/one/logs", nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), "Alex joined") {
			t.Fatalf("snapshot: %d %s", response.Code, response.Body)
		}
	}
	if len(p.queue) != 0 {
		t.Fatal("history generated push")
	}
	next := initial + "2026-09-29T12:00:03.000000001Z [12:00:03] [Server thread/INFO]: Alex left the game\n"
	if err := os.WriteFile(logFile, []byte(next), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.logs.poll("one", watch); err != nil {
		t.Fatal(err)
	}
	if len(p.queue) != 1 {
		t.Fatalf("expected one new event; got %d", len(p.queue))
	}
	if e := <-p.queue; e.Server != "one" || e.Action != "left" {
		t.Fatalf("bad event: %+v", e)
	}
	if err := a.logs.poll("one", watch); err != nil {
		t.Fatal(err)
	}
	if len(p.queue) != 0 {
		t.Fatal("same log replay generated push")
	}
	content, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), " config ") != 1 || strings.Count(string(content), " logs ") != 3 {
		t.Fatalf("console readers launched Docker calls or service was not cached: %s", content)
	}
	if !strings.Contains(string(content), "--since 2026-09-29T12:00:00.000000001Z") {
		t.Fatal("incremental poll lost nanosecond cursor")
	}
	// A transient Docker failure must not move the cursor or lose cached output.
	cursor := watch.cursor
	if err := os.Remove(logFile); err != nil {
		t.Fatal(err)
	}
	if err := a.logs.poll("one", watch); err == nil {
		t.Fatal("Docker failure was hidden")
	}
	if !watch.cursor.Equal(cursor) || len(watch.lines) != 2 {
		t.Fatal("Docker failure changed cached logs")
	}
}

// Inspect the actual JWT produced by the dependency, rather than only checking
// that a VAPID header exists. Email configuration must contain one mailto scheme.
func TestPushDeliveryVAPIDContact(t *testing.T) {
	for _, origin := range []string{"https://web.push.apple.com", "https://fcm.googleapis.com"} {
		for _, contact := range []string{"mailto:admin@example.com", "https://mc.example.com"} {
			t.Run(origin+"/"+contact, func(t *testing.T) {
				_, p := testPushManager(t)
				p.contact = contact
				sub := testPushSubscription(t, origin+"/device-token")
				p.state.Devices[sub.Endpoint] = pushDevice{Subscription: sub, Servers: map[string]bool{"one": true}}
				client := &fakePushClient{statuses: []int{201, 201}}
				p.client = client
				p.deliver(context.Background(), playerEvent{Server: "one", Player: "Patik9622", Action: "joined"})
				if len(client.headers) != 1 {
					t.Fatalf("expected one push, got %d", len(client.headers))
				}
				token, public, ok := strings.Cut(strings.TrimPrefix(client.headers[0], "vapid t="), ", k=")
				if !ok || public != p.state.PublicKey {
					t.Fatal("incorrect VAPID public key")
				}
				parts := strings.Split(token, ".")
				if len(parts) != 3 {
					t.Fatal("invalid JWT")
				}
				payload, err := base64.RawURLEncoding.DecodeString(parts[1])
				if err != nil {
					t.Fatal(err)
				}
				var claims struct {
					Subject  string `json:"sub"`
					Audience string `json:"aud"`
				}
				if err := json.Unmarshal(payload, &claims); err != nil {
					t.Fatal(err)
				}
				if claims.Subject != contact {
					t.Fatalf("VAPID subject = %q; want %q", claims.Subject, contact)
				}
				if claims.Audience != origin {
					t.Fatalf("wrong audience: %q", claims.Audience)
				}
				p.deliver(context.Background(), playerEvent{Server: "one", Player: "Patik9622", Action: "left"})
				if len(client.headers) != 2 || client.headers[0] != client.headers[1] {
					t.Fatal("VAPID token refreshed within an hour")
				}
			})
		}
	}

}

func TestReportedBedrockLogEvents(t *testing.T) {
	lines := []string{
		"[2026-09-29 11:19:02:700 INFO] Player disconnected: Ananana7817, xuid: 2535460849060830, pfid: ECA7BCAF0AB8CBA",
		"[2026-09-29 11:19:16:600 INFO] Player connected: Ananana7817, xuid: 2535460849060830",
		"[2026-09-29 11:19:17:606 INFO] Player PartyIdUpdate:  pfid: ECA7BCAF0AB8CBA, partyid: , isLeader: false",
		"[2026-09-29 11:19:19:849 INFO] Player Spawned: Ananana7817 xuid: 2535460849060830, pfid: ECA7BCAF0AB8CBA",
		"[2026-09-29 11:23:23:097 INFO] Player disconnected: Ananana7817, xuid: 2535460849060830, pfid: ECA7BCAF0AB8CBA",
		"[2026-09-29 11:37:34:647 INFO] Player disconnected: Patik9622, xuid: 2533274995732102, pfid: 3D1F8E956DA8669A",
		"[2026-09-29 12:58:51:182 INFO] Player connected: Patik9622, xuid: 2533274995732102",
		"[2026-09-29 12:58:52:307 INFO] Player PartyIdUpdate:  pfid: 3D1F8E956DA8669A, partyid: , isLeader: false",
		"[2026-09-29 12:58:52:840 INFO] Player Spawned: Patik9622 xuid: 2533274995732102, pfid: 3D1F8E956DA8669A",
		"[2026-09-29 12:59:43:761 INFO] Player disconnected: Patik9622, xuid: 2533274995732102, pfid: 3D1F8E956DA8669A",
	}
	base := time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC)
	watch := &serverLogWatch{boundary: make(map[string]int)}
	watch.ingest("", base)
	var output strings.Builder
	for i, line := range lines {
		// Docker supplies the outer RFC3339 timestamp; the console displays the body.
		output.WriteString(base.Add(time.Duration(i+1)*time.Second).Format(time.RFC3339Nano) + " " + line + "\n")
	}
	want := []playerEvent{
		{Player: "Ananana7817", Action: "left"}, {Player: "Ananana7817", Action: "joined"}, {Player: "Ananana7817", Action: "left"},
		{Player: "Patik9622", Action: "left"}, {Player: "Patik9622", Action: "joined"}, {Player: "Patik9622", Action: "left"},
	}
	if got := watch.ingest(output.String(), base.Add(time.Hour)); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v; want %+v", got, want)
	}
	if got := watch.ingest(output.String(), base.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("replayed events: %+v", got)
	}
}

func TestPushStatusRefreshesRotatedKeysWithoutNewSubscriptions(t *testing.T) {
	a, p := testPushManager(t)
	old := testPushSubscription(t, "https://fcm.googleapis.com/token")
	p.state.Devices[old.Endpoint] = pushDevice{Subscription: old, Servers: map[string]bool{"one": true, "two": true}}
	updated := testPushSubscription(t, old.Endpoint)
	response := pushRequest(a, map[string]any{"action": "status", "subscription": updated}, true)
	if response.Code != 200 {
		t.Fatalf("refresh: %d %s", response.Code, response.Body)
	}
	if p.state.Devices[old.Endpoint].Subscription != updated || !p.hasServer("two") {
		t.Fatal("did not refresh keys or lost another server preference")
	}
	fresh := testPushSubscription(t, "https://fcm.googleapis.com/new-token")
	response = pushRequest(a, map[string]any{"action": "status", "subscription": fresh}, true)
	if response.Code != 200 || len(p.state.Devices) != 1 {
		t.Fatal("status enrolled an unknown browser")
	}
}
func TestInvalidPushContactRejected(t *testing.T) {
	for _, contact := range []string{"mailto:mailto:admin@example.com", "mailto:not-an-address", "mailto:User <admin@example.com>"} {
		t.Setenv("MCUI_PUSH_CONTACT", contact)
		if _, err := newPushManager(t.TempDir()); err == nil {
			t.Fatalf("accepted invalid contact %q", contact)
		}
	}
}
