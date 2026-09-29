package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
)

type checkingPushClient struct{ check func(*http.Request) }

func (c checkingPushClient) Do(r *http.Request) (*http.Response, error) {
	c.check(r)
	return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
}

// Independently decrypt an outbound payload using the device's private key.
// This exercises real Web Push encryption and VAPID signing, not just a mocked
// sender returning 201, for both Apple and Google's endpoint origins.
func TestPushWirePayloadForAppleAndAndroid(t *testing.T) {
	for _, origin := range []string{"https://web.push.apple.com", "https://fcm.googleapis.com"} {
		t.Run(origin, func(t *testing.T) {
			a, p := testPushManager(t)
			if w := profileRequest(a, "one", "Family world", profileImage(t, 64, "png"), false, true); w.Code != 200 {
				t.Fatal(w.Body)
			}
			deviceKey, err := ecdh.P256().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			sub := testPushSubscription(t, origin+"/device")
			sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(deviceKey.PublicKey().Bytes())
			p.state.Devices[sub.Endpoint] = pushDevice{Subscription: sub, Servers: map[string]bool{"one": true}}
			called := false
			p.client = checkingPushClient{check: func(r *http.Request) {
				called = true
				if r.Method != "POST" || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Urgency") != "high" {
					t.Fatal("incorrect push request headers")
				}
				token, pub, ok := strings.Cut(strings.TrimPrefix(r.Header.Get("Authorization"), "vapid t="), ", k=")
				if !ok || pub != p.state.PublicKey {
					t.Fatal("wrong VAPID public key")
				}
				parts := strings.Split(token, ".")
				if len(parts) != 3 {
					t.Fatal("invalid JWT")
				}
				signature, err := base64.RawURLEncoding.DecodeString(parts[2])
				if err != nil || len(signature) != 64 {
					t.Fatal("invalid JWT signature encoding")
				}
				pubBytes, _ := base64.RawURLEncoding.DecodeString(pub)
				x, y := elliptic.Unmarshal(elliptic.P256(), pubBytes)
				digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
				if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
					t.Fatal("VAPID signature does not verify")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) < 86 {
					t.Fatal("missing encryption header")
				}
				salt := body[:16]
				keyLength := int(body[20])
				if keyLength != 65 || len(body) <= 21+keyLength {
					t.Fatal("invalid sender key")
				}
				sender, err := ecdh.P256().NewPublicKey(body[21 : 21+keyLength])
				if err != nil {
					t.Fatal(err)
				}
				secret, err := deviceKey.ECDH(sender)
				if err != nil {
					t.Fatal(err)
				}
				auth, _ := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
				info := append([]byte("WebPush: info\x00"), deviceKey.PublicKey().Bytes()...)
				info = append(info, sender.Bytes()...)
				ikm, err := hkdf.Key(sha256.New, secret, auth, string(info), 32)
				if err != nil {
					t.Fatal(err)
				}
				key, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
				if err != nil {
					t.Fatal(err)
				}
				nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
				if err != nil {
					t.Fatal(err)
				}
				block, err := aes.NewCipher(key)
				if err != nil {
					t.Fatal(err)
				}
				gcm, err := cipher.NewGCM(block)
				if err != nil {
					t.Fatal(err)
				}
				plaintext, err := gcm.Open(nil, nonce, body[21+keyLength:], nil)
				if err != nil {
					t.Fatalf("device could not decrypt: %v", err)
				}
				plaintext = bytes.TrimRight(plaintext, "\x00")
				if len(plaintext) == 0 || plaintext[len(plaintext)-1] != 2 {
					t.Fatal("invalid final record delimiter")
				}
				var payload map[string]string
				if err := json.Unmarshal(plaintext[:len(plaintext)-1], &payload); err != nil {
					t.Fatal(err)
				}
				if payload["title"] != "Family world" || payload["body"] != "✨ Alex joined the adventure!" || payload["url"] != "/servers/one" || payload["icon"] != a.profile("one").iconURL("one") {
					t.Fatalf("wrong decrypted notification: %v", payload)
				}
			}}
			p.deliver(context.Background(), playerEvent{Server: "one", Player: "Alex", Action: "joined"})
			if !called {
				t.Fatal("no notification sent")
			}
		})
	}
}
