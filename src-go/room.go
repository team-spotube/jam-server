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
	"encoding/json"
	"log"
	"slices"
	"sort"
	"sync"
	"time"
)

// bucket is one token bucket's state.
type bucket struct {
	tokens float64
	at     time.Time
}

func newBucket(b budget, now time.Time) bucket { return bucket{tokens: b.burst, at: now} }

// spend takes one token; false when the bucket is empty.
func (k *bucket) spend(b budget, now time.Time) bool {
	k.tokens = min(b.burst, k.tokens+now.Sub(k.at).Seconds()*b.perSecond)
	k.at = now
	if k.tokens < 1 {
		return false
	}
	k.tokens--
	return true
}

// room is one jam room, named by its six-letter code.
//
// It is a relay with memory: it tracks who is connected and who hosts, keeps the
// host's latest queue and playback anchor for late joiners, routes guest commands to
// the host, and serves the clock everyone syncs playback against. All of its state
// is guarded by mu; the durable part is written through to SQLite.
type room struct {
	code  string
	hub   *hub
	store *store

	mu     sync.Mutex
	state  roomState
	anchor json.RawMessage
	conns  map[*conn]struct{}
	// dead: the room was expired and removed from the hub; callers must look it up again.
	dead       bool
	graceTimer *time.Timer
}

func newRoom(h *hub, code string, state *roomState) *room {
	if state == nil {
		state = &roomState{}
	}
	return &room{code: code, hub: h, store: h.store, state: *state, conns: map[*conn]struct{}{}}
}

// exists: a room exists once it has been created (it has a host).
func (r *room) exists() bool { return r.state.Host != "" }

func (r *room) persist() {
	if err := r.store.save(r.code, &r.state); err != nil {
		log.Printf("room %s: save: %v", r.code, err)
	}
}

// join admits c, or rejects it. It reports whether c was admitted.
func (r *room) join(c *conn, mode joinMode) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return false
	}

	// A reconnect with the same id (network change, app restart) takes over the old socket.
	for other := range r.conns {
		if other.id == c.id {
			delete(r.conns, other)
			other.reject(errReplaced)
		}
	}

	if refusal := r.admit(c, mode); refusal != "" {
		c.reject(refusal)
		return true
	}
	r.conns[c] = struct{}{}
	if r.state.EmptySince != 0 {
		r.state.EmptySince = 0
		r.persist()
	}
	var host *string
	if r.state.Host != "" {
		host = &r.state.Host
	}
	c.send(marshal(welcomeFrame{
		T: "welcome", V: protocolVersion, You: c.id, Host: host,
		Participants: r.participants(), Queue: rawOrNull(r.state.Queue), Anchor: rawOrNull(r.anchor),
	}))
	r.broadcast(r.presence(), c)
	return true
}

// admit decides whether c may enter; it returns the refusal, or "" to let it in. Called
// with mu held, before c is registered, so r.conns holds only the others.
func (r *room) admit(c *conn, mode joinMode) errorCode {
	others := len(r.conns)
	// An empty room past its TTL whose cleanup hasn't run yet is gone.
	if others == 0 && r.state.EmptySince != 0 && time.Since(time.UnixMilli(r.state.EmptySince)) >= emptyRoomTTL {
		r.reset()
	}
	exists := r.exists()

	if mode == modeCreate {
		// Only a fresh code, or the room's own host bringing it back. Anyone else
		// "creating" an existing room (even an empty one) would take it over.
		if others > 0 || (exists && r.state.Host != c.id) {
			return errRoomTaken
		}
		if !exists {
			r.state.Host = c.id
			r.state.Members = []string{c.id}
			r.persist()
		}
		return ""
	}

	if !exists {
		return errRoomNotFound
	}
	if slices.Contains(r.state.Banned, c.id) {
		return errBanned
	}
	isMember := slices.Contains(r.state.Members, c.id)
	// A member who was in the room before a ban (a friend on the same Wi-Fi with the
	// same phone, say) is never caught by another person's device ban.
	if c.mark != "" && !isMember && slices.Contains(r.state.BannedMarks, c.mark) {
		return errBanned
	}
	if others >= maxParticipants {
		return errRoomFull
	}
	if !isMember {
		r.state.Members = tail(append(r.state.Members, c.id), maxMembers)
		r.persist()
	}

	// Joining a room whose host is gone (and whose grace period is over) promotes someone.
	hostLive := r.state.Host == c.id || r.hasLive(r.state.Host)
	if !hostLive {
		if r.state.HostGoneAt == 0 || time.Since(time.UnixMilli(r.state.HostGoneAt)) >= hostGrace {
			r.promote(c)
		}
	} else if r.state.Host == c.id && r.state.HostGoneAt != 0 {
		r.state.HostGoneAt = 0
		r.persist()
	}
	return ""
}

// handle processes one text frame from c.
func (r *room) handle(c *conn, raw []byte) {
	var frame clientFrame
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[c]; !ok {
		return
	}

	switch frame.T {
	case "ping":
		c.send(marshal(pongFrame{T: "pong", ID: frame.ID, C: frame.C, S: time.Now().UnixMilli()}))

	case "queue":
		if r.state.Host != c.id {
			return
		}
		if !c.queueBucket.spend(queueBudget, time.Now()) {
			c.reject(errRateLimited)
			return
		}
		r.state.Queue = rawOrNull(frame.Body)
		r.persist()
		r.broadcast(bodyFrame{T: "queue", Body: rawOrNull(frame.Body)}, c)

	case "anchor":
		if r.state.Host != c.id {
			return
		}
		r.anchor = rawOrNull(frame.Body)
		r.broadcast(bodyFrame{T: "anchor", Body: rawOrNull(frame.Body)}, c)

	case "cmd":
		out := marshal(cmdFrame{T: "cmd", From: c.id, Body: rawOrNull(frame.Body)})
		if c.id == r.state.Host {
			r.broadcastRaw(out, c)
		} else if r.state.Host != "" {
			for target := range r.conns {
				if target.id == r.state.Host {
					target.send(out)
				}
			}
		}

	case "kick":
		if r.state.Host != c.id || frame.Target == c.id {
			return
		}
		if frame.Ban {
			r.ban(frame.Target)
		}
		reason := errKicked
		if frame.Ban {
			reason = errBanned
		}
		removed := false
		for target := range r.conns {
			if target.id == frame.Target {
				delete(r.conns, target)
				target.reject(reason)
				removed = true
			}
		}
		if removed {
			r.afterDeparture(frame.Target)
		}
	}
}

// ban bans id, and the device it's on: someone who comes back with a fresh identity
// from the same device and network is refused too, unless they were already a member.
func (r *room) ban(id string) {
	for c := range r.conns {
		if c.id == id && c.mark != "" && !slices.Contains(r.state.BannedMarks, c.mark) {
			r.state.BannedMarks = append(r.state.BannedMarks, c.mark)
		}
	}
	if !slices.Contains(r.state.Banned, id) {
		r.state.Banned = append(r.state.Banned, id)
	}
	r.state.Banned = tail(r.state.Banned, maxBans)
	r.state.BannedMarks = tail(r.state.BannedMarks, maxBans)
	r.persist()
}

// leave runs when c's socket closes.
func (r *room) leave(c *conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[c]; !ok {
		return // Replaced or kicked: already handled.
	}
	delete(r.conns, c)
	r.afterDeparture(c.id)
}

// afterDeparture updates the room once every socket of id is gone.
func (r *room) afterDeparture(id string) {
	// Replaced by a newer socket of the same participant: nothing changed.
	if r.hasLive(id) {
		return
	}
	if len(r.conns) == 0 {
		r.state.EmptySince = time.Now().UnixMilli()
		r.persist()
		r.stopGrace()
		return
	}
	if id == r.state.Host {
		r.state.HostGoneAt = time.Now().UnixMilli()
		r.persist()
		r.stopGrace()
		r.graceTimer = time.AfterFunc(hostGrace, r.graceElapsed)
	}
	r.broadcast(r.presence(), nil)
}

// graceElapsed fires after a host's grace period.
func (r *room) graceElapsed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead || len(r.conns) == 0 || r.hasLive(r.state.Host) {
		return
	}
	r.promote(nil)
}

// promote hands the room to the longest-connected participant (or fallback in an
// empty room).
func (r *room) promote(fallback *conn) {
	var next string
	if p := r.participants(); len(p) > 0 {
		next = p[0].ID
	} else if fallback != nil {
		next = fallback.id
	}
	if next == "" {
		return
	}
	r.state.Host = next
	r.state.HostGoneAt = 0
	r.persist()
	r.stopGrace()
	r.broadcast(r.presence(), nil)
}

func (r *room) stopGrace() {
	if r.graceTimer != nil {
		r.graceTimer.Stop()
		r.graceTimer = nil
	}
}

// reset forgets the room completely, for when it expired.
func (r *room) reset() {
	if err := r.store.delete(r.code); err != nil {
		log.Printf("room %s: delete: %v", r.code, err)
	}
	r.state = roomState{}
	r.anchor = nil
	r.stopGrace()
}

// expire removes an empty room past its TTL; it reports whether the room is gone.
func (r *room) expire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.conns) > 0 || r.state.EmptySince == 0 || time.Since(time.UnixMilli(r.state.EmptySince)) < emptyRoomTTL {
		return false
	}
	r.reset()
	r.dead = true
	return true
}

func (r *room) hasLive(id string) bool {
	for c := range r.conns {
		if c.id == id {
			return true
		}
	}
	return false
}

// participants are the connected ones, longest-connected first, one entry per id.
func (r *room) participants() []participant {
	byID := map[string]*conn{}
	for c := range r.conns {
		if existing, ok := byID[c.id]; !ok || c.joined.Before(existing.joined) {
			byID[c.id] = c
		}
	}
	sorted := make([]*conn, 0, len(byID))
	for _, c := range byID {
		sorted = append(sorted, c)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].joined.Equal(sorted[j].joined) {
			return sorted[i].joined.Before(sorted[j].joined)
		}
		return sorted[i].id < sorted[j].id
	})
	out := make([]participant, len(sorted))
	for i, c := range sorted {
		out[i] = participant{ID: c.id, Name: c.name}
	}
	return out
}

func (r *room) presence() presenceFrame {
	var host *string
	if r.state.Host != "" {
		host = &r.state.Host
	}
	return presenceFrame{T: "presence", Host: host, Participants: r.participants()}
}

func (r *room) broadcast(frame any, except *conn) { r.broadcastRaw(marshal(frame), except) }

func (r *room) broadcastRaw(msg []byte, except *conn) {
	for c := range r.conns {
		if c != except {
			c.send(msg)
		}
	}
}

// tail keeps the last n items.
func tail(list []string, n int) []string {
	if len(list) > n {
		return list[len(list)-n:]
	}
	return list
}
