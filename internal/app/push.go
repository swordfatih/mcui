package app

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type pushDevice struct {
	Subscription webpush.Subscription `json:"subscription"`
	Servers      map[string]bool      `json:"servers"`
}
type pushState struct {
	PrivateKey string                `json:"privateKey"`
	PublicKey  string                `json:"publicKey"`
	Devices    map[string]pushDevice `json:"devices"`
}
type PushManager struct {
	mu          sync.Mutex
	state       pushState
	path        string
	contact     string
	client      webpush.HTTPClient
	queue       chan playerEvent
	watch       *LogWatch
	authHeaders map[string]pushAuthHeader
}

type pushAuthHeader struct {
	value string
	until time.Time
}
type pushAuthClient struct{ manager *PushManager }

func (c pushAuthClient) Do(r *http.Request) (*http.Response, error) {
	p := c.manager
	// Apple asks applications not to refresh VAPID JWTs more than once an hour.
	// Share authorization only within the same push-service origin and key pair.
	key := r.URL.Scheme + "://" + r.URL.Host + "|" + p.contact + "|" + p.state.PublicKey
	p.mu.Lock()
	if p.authHeaders == nil {
		p.authHeaders = make(map[string]pushAuthHeader)
	}
	now := time.Now()
	for origin, entry := range p.authHeaders {
		if !now.Before(entry.until) {
			delete(p.authHeaders, origin)
		}
	}
	if cached, ok := p.authHeaders[key]; ok {
		r.Header.Set("Authorization", cached.value)
	} else if len(p.authHeaders) < 1024 {
		p.authHeaders[key] = pushAuthHeader{r.Header.Get("Authorization"), now.Add(time.Hour)}
	}
	p.mu.Unlock()
	return p.client.Do(r)
}

func newPushManager(root string) (*PushManager, error) {
	p := &PushManager{path: filepath.Join(root, ".mcui-push", "state.json"), contact: os.Getenv("MCUI_PUSH_CONTACT"), client: pushHTTPClient(), queue: make(chan playerEvent, 1024)}
	// A public HTTPS URL or a real mailto address is required by push providers.
	if p.contact == "" && os.Getenv("MCUI_PUBLIC_HOST") != "" {
		p.contact = "https://" + os.Getenv("MCUI_PUBLIC_HOST")
	}
	if p.contact != "" {
		u, err := url.Parse(p.contact)
		if err != nil || !(u.Scheme == "mailto" && strings.Contains(u.Opaque, "@") || u.Scheme == "https" && u.Hostname() != "" && u.User == nil) {
			return nil, errors.New("MCUI_PUSH_CONTACT must be a mailto address or public HTTPS URL")
		}
	}
	if strings.HasPrefix(p.contact, "mailto:") {
		address, err := mail.ParseAddress(strings.TrimPrefix(p.contact, "mailto:"))
		if err != nil || address.Address != strings.TrimPrefix(p.contact, "mailto:") {
			return nil, errors.New("MCUI_PUSH_CONTACT must contain a valid mailto email address")
		}
	}
	data, err := os.ReadFile(p.path)
	if err == nil {
		if err = json.Unmarshal(data, &p.state); err != nil {
			return nil, fmt.Errorf("read push state: %w", err)
		}
		private, err := base64.RawURLEncoding.DecodeString(p.state.PrivateKey)
		if err != nil {
			return nil, errors.New("invalid stored push private key")
		}
		key, err := ecdh.P256().NewPrivateKey(private)
		if err != nil || base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != p.state.PublicKey {
			return nil, errors.New("invalid stored push key pair")
		}
	} else if os.IsNotExist(err) {
		p.state.PrivateKey, p.state.PublicKey, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if p.state.Devices == nil {
		p.state.Devices = make(map[string]pushDevice)
	}
	if err := p.saveLocked(); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *PushManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(p.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.path), "state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), p.path)
}
func (p *PushManager) hasServer(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, device := range p.state.Devices {
		if device.Servers[name] {
			return true
		}
	}
	return false
}
func (p *PushManager) start(ctx context.Context) {
	p.mu.Lock()
	names := make(map[string]bool)
	for _, device := range p.state.Devices {
		for name := range device.Servers {
			names[name] = true
		}
	}
	p.mu.Unlock()
	for name := range names {
		p.watch.ensure(name)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-p.queue:
				p.deliver(ctx, event)
			}
		}
	}()
}
func (p *PushManager) enqueue(event playerEvent) {
	if !p.hasServer(event.Server) {
		return
	}
	select {
	case p.queue <- event:
	default:
		log.Printf("push queue full; notification dropped for server %q", event.Server)
	}
}
func (p *PushManager) deliver(ctx context.Context, event playerEvent) {
	p.mu.Lock()
	var devices []pushDevice
	for _, d := range p.state.Devices {
		if d.Servers[event.Server] {
			devices = append(devices, d)
		}
	}
	p.mu.Unlock()
	payload := p.eventPayload(event)
	for _, d := range devices {
		// Recheck so an unsubscribe while another endpoint is sending takes effect.
		p.mu.Lock()
		active := p.state.Devices[d.Subscription.Endpoint].Servers[event.Server]
		p.mu.Unlock()
		if !active {
			continue
		}
		for attempt := 0; attempt < 3; attempt++ {
			status, err := p.send(ctx, payload, d.Subscription)
			if status == http.StatusGone || status == http.StatusNotFound {
				p.mu.Lock()
				old, exists := p.state.Devices[d.Subscription.Endpoint]
				if exists && old.Subscription == d.Subscription {
					delete(p.state.Devices, d.Subscription.Endpoint)
					if saveErr := p.saveLocked(); saveErr != nil {
						p.state.Devices[d.Subscription.Endpoint] = old
						log.Printf("could not remove expired push subscription: %v", saveErr)
					}
				}
				p.mu.Unlock()
				break
			}
			if err == nil && status >= 200 && status < 300 {
				break
			}
			if attempt == 2 || status > 0 && status != 429 && status < 500 {
				// Endpoints are bearer capabilities; never include them or HTTP errors in logs.
				log.Printf("push delivery failed for server %q: %v", event.Server, err)
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
}

// send is shared by real events and the user-triggered delivery check.
// Error text intentionally excludes capability URLs, tokens and raw responses.
func (p *PushManager) send(ctx context.Context, payload []byte, sub webpush.Subscription) (int, error) {
	// webpush-go v1.4 adds mailto: to non-HTTPS contacts itself.
	response, err := webpush.SendNotificationWithContext(ctx, payload, &sub, &webpush.Options{
		HTTPClient: pushAuthClient{p}, Subscriber: strings.TrimPrefix(p.contact, "mailto:"), VAPIDPublicKey: p.state.PublicKey,
		VAPIDPrivateKey: p.state.PrivateKey, TTL: 60, Urgency: webpush.UrgencyHigh,
	})
	if response == nil {
		return 0, errors.New("Could not contact the push service; check server connectivity and push configuration")
	}
	defer response.Body.Close()
	if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		return response.StatusCode, nil
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body)
	reason := ""
	// Apple's documented error codes are useful diagnostics without disclosing
	// arbitrary response content from a user-supplied endpoint.
	switch body.Reason {
	case "BadJwtToken", "BadAuthorizationHeader", "VapidPkHashMismatch", "BadTtl", "BadUrgency", "BadWebPushRequest", "ExpiredToken", "BadDeviceToken", "Unregistered", "TooManyRequests", "PayloadTooLarge":
		reason = ", " + body.Reason
	}
	return response.StatusCode, fmt.Errorf("Push service rejected the notification (HTTP %d%s)", response.StatusCode, reason)
}

func (p *PushManager) eventPayload(event playerEvent) []byte {
	profile := serverProfile{DisplayName: event.Server}
	if p.watch != nil {
		profile = p.watch.api.profile(event.Server)
	}
	body := "✨ " + event.Player + " joined the adventure!"
	if event.Action == "left" {
		body = "👋 " + event.Player + " left the world. See you soon!"
	}
	payload, _ := json.Marshal(map[string]string{"title": profile.DisplayName, "body": body, "url": "/servers/" + url.PathEscape(event.Server), "icon": profile.iconURL(event.Server)})
	return payload
}

// Restrict push endpoints to HTTPS and resolve/dial public addresses ourselves.
// This prevents authenticated subscription requests from becoming an SSRF path.
func validPushEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && len(endpoint) <= 4096 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}
func publicPushIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !(ip.To4() != nil && (ip.To4()[0] == 0 || ip.To4()[0] == 100 && ip.To4()[1] >= 64 && ip.To4()[1] <= 127 || ip.To4()[0] >= 224))
}
func pushHTTPClient() *http.Client {
	transport := &http.Transport{ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, errors.New("push endpoint has no address")
			}
			for _, ip := range ips {
				if !publicPushIP(ip.IP) {
					return nil, errors.New("push endpoint must use a public address")
				}
			}
			var last error
			for _, ip := range ips {
				conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func validatePushSubscription(s webpush.Subscription) error {
	if !validPushEndpoint(s.Endpoint) {
		return errors.New("Invalid HTTPS push endpoint")
	}
	auth, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s.Keys.Auth, "="))
	if err != nil || len(auth) != 16 {
		return errors.New("Invalid push authentication key")
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s.Keys.P256dh, "="))
	if err != nil {
		return errors.New("Invalid push public key")
	}
	if _, err := ecdh.P256().NewPublicKey(key); err != nil {
		return errors.New("Invalid push public key")
	}
	return nil
}
func (a *API) pushHandler(w http.ResponseWriter, r *http.Request) {
	p := a.push
	if r.URL.Path == "/api/push/config" && r.Method == http.MethodGet {
		respond(w, 200, map[string]any{"enabled": p.contact != "", "publicKey": p.state.PublicKey})
		return
	}
	if r.URL.Path != "/api/push/subscription" {
		bad(w, 404, "Not found")
		return
	}
	if r.Method != http.MethodPost {
		bad(w, 405, "Method not allowed")
		return
	}
	// A custom header requires a same-origin request or a CORS preflight, which
	// MCUI does not grant. JSON alone is insufficient for CSRF protection.
	if r.Header.Get("X-MCUI-Push") != "1" {
		bad(w, 403, "Same-origin push request required")
		return
	}
	if p.contact == "" {
		bad(w, 503, "Set MCUI_PUSH_CONTACT to enable notifications")
		return
	}
	var request struct {
		Action       string               `json:"action"`
		Server       string               `json:"server"`
		Subscription webpush.Subscription `json:"subscription"`
		Endpoint     string               `json:"endpoint"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		bad(w, 400, "Invalid push request")
		return
	}
	if request.Action != "status" && request.Action != "subscribe" && request.Action != "unsubscribe" && request.Action != "test" {
		bad(w, 400, "Invalid push action")
		return
	}
	if request.Action == "subscribe" || request.Action == "status" && request.Subscription.Endpoint != "" {
		if err := validatePushSubscription(request.Subscription); err != nil {
			bad(w, 400, err.Error())
			return
		}
		request.Endpoint = request.Subscription.Endpoint
	}
	if !validPushEndpoint(request.Endpoint) {
		bad(w, 400, "Invalid push endpoint")
		return
	}
	if request.Action != "status" {
		if _, err := a.composeFile(request.Server); err != nil {
			bad(w, 404, "Server not found")
			return
		}
	}
	p.mu.Lock()
	old, exists := p.state.Devices[request.Endpoint]
	if request.Action == "test" {
		p.mu.Unlock()
		if !exists || !old.Servers[request.Server] {
			bad(w, 409, "Enable notifications for this server on this device first")
			return
		}
		profile := a.profile(request.Server)
		payload, _ := json.Marshal(map[string]string{"title": profile.DisplayName, "body": "🔔 Ding! You’re ready for little updates from your world.", "url": "/servers/" + url.PathEscape(request.Server), "icon": profile.iconURL(request.Server)})
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		_, err := p.send(ctx, payload, old.Subscription)
		if err != nil {
			bad(w, 502, err.Error())
			return
		}
		respond(w, 200, map[string]bool{"accepted": true})
		return
	}
	device := pushDevice{Subscription: old.Subscription, Servers: make(map[string]bool)}
	for name, enabled := range old.Servers {
		device.Servers[name] = enabled
	}
	if request.Action == "subscribe" {
		if !exists && len(p.state.Devices) >= 1000 {
			p.mu.Unlock()
			bad(w, 409, "Push device limit reached")
			return
		}
		device.Subscription = request.Subscription
		device.Servers[request.Server] = true
	} else if request.Action == "unsubscribe" {
		delete(device.Servers, request.Server)
	}
	refreshKeys := request.Action == "status" && exists && request.Subscription.Endpoint != "" && device.Subscription != request.Subscription
	if refreshKeys {
		device.Subscription = request.Subscription
	}
	if request.Action != "status" || refreshKeys {
		if len(device.Servers) == 0 {
			delete(p.state.Devices, request.Endpoint)
		} else {
			p.state.Devices[request.Endpoint] = device
		}
		if err := p.saveLocked(); err != nil {
			if exists {
				p.state.Devices[request.Endpoint] = old
			} else {
				delete(p.state.Devices, request.Endpoint)
			}
			p.mu.Unlock()
			bad(w, 500, "Could not save notification preferences")
			return
		}
	}
	names := make([]string, 0, len(device.Servers))
	for name := range device.Servers {
		names = append(names, name)
	}
	p.mu.Unlock()
	if request.Action == "subscribe" {
		a.logs.ensure(request.Server)
	}
	respond(w, 200, map[string]any{"servers": names})
}
