/*
 * Copyright (C) 2026 Kingkor Roy Tirtho and Spotube Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// connectBudget limits room connects per client IP: it slows down code guessing and
// connection floods. Generous because carrier-grade NAT puts many phones behind one IP.
var connectBudget = budget{burst: 60, perSecond: 1}

var roomPath = regexp.MustCompile(`^/v1/rooms/([^/]+)$`)

type server struct {
	hub      *hub
	ipHeader string

	mu       sync.Mutex
	limiters map[string]*bucket
}

// newServer routes:
//   - GET /v1/health: { ok, v, now }, used by the app's "Test connection"
//   - GET /v1/rooms/{CODE}?key=&name=&mode=create|join[&dev=] (WebSocket)
func newServer(h *hub, ipHeader string) http.Handler {
	s := &server{hub: h, ipHeader: ipHeader, limiters: map[string]*bucket{}}
	go func() {
		for range time.Tick(time.Minute) {
			s.pruneLimiters()
		}
	}()
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "v": protocolVersion, "now": time.Now().UnixMilli()})
		return
	}
	match := roomPath.FindStringSubmatch(r.URL.Path)
	if match == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "Expected a WebSocket upgrade", http.StatusUpgradeRequired)
		return
	}

	query := r.URL.Query()
	code := strings.ToUpper(match[1])
	key := query.Get("key")
	name := cleanName(query.Get("name"))
	mode := joinMode(query.Get("mode"))
	dev := ""
	_, hasDev := query["dev"]
	if hasDev {
		dev = query.Get("dev")
	}
	if !roomCode.MatchString(code) || !clientKey.MatchString(key) || name == "" ||
		(mode != modeCreate && mode != modeJoin) || (hasDev && !deviceDigest.MatchString(dev)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": string(errBadRequest)})
		return
	}

	ip := s.clientIP(r)
	if !s.allow(ip) {
		http.Error(w, "Too many connections", http.StatusTooManyRequests)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return // Accept already replied.
	}
	// Frames over the limit get frame_too_large instead of a bare protocol close.
	ws.SetReadLimit(2 * maxFrameBytes)

	c := newConn(ws, participantID(code, key), name, deviceMark(code, dev, ip))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.writeLoop(ctx, cancel)

	// A room can be expired between lookup and join; look it up again.
	var joined *room
	for attempt := 0; attempt < 3 && joined == nil; attempt++ {
		rm, err := s.hub.room(code)
		if err != nil {
			c.reject(errBadRequest)
			<-ctx.Done()
			return
		}
		if rm.join(c, mode) {
			joined = rm
		}
	}
	if joined == nil {
		c.reject(errBadRequest)
		<-ctx.Done()
		return
	}

	s.readLoop(ctx, joined, c)
	joined.leave(c)
	c.shutdown()
	<-ctx.Done() // The writer closes the socket once it has flushed.
}

func (s *server) readLoop(ctx context.Context, rm *room, c *conn) {
	for {
		kind, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if len(data) > maxFrameBytes {
			c.reject(errFrameTooLarge)
			return
		}
		if kind != websocket.MessageText {
			continue
		}
		if !c.frameBucket.spend(frameBudget, time.Now()) {
			c.reject(errRateLimited)
			return
		}
		if string(data) == keepalivePing {
			c.send([]byte(keepalivePong))
			continue
		}
		rm.handle(c, data)
	}
}

// clientIP is the peer's address, or the trusted proxy header's value.
func (s *server) clientIP(r *http.Request) string {
	if s.ipHeader != "" {
		if v := r.Header.Get(s.ipHeader); v != "" {
			// X-Forwarded-For lists the chain; the proxy we trust appended the last entry.
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) allow(ip string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.limiters[ip]
	if !ok {
		b = &bucket{tokens: connectBudget.burst, at: now}
		s.limiters[ip] = b
	}
	return b.spend(connectBudget, now)
}

// pruneLimiters forgets IPs whose bucket has refilled completely.
func (s *server) pruneLimiters() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for ip, b := range s.limiters {
		if now.Sub(b.at).Seconds()*connectBudget.perSecond >= connectBudget.burst {
			delete(s.limiters, ip)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
