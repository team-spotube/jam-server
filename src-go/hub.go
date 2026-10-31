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
	"log"
	"sync"
	"time"
)

// hub owns the rooms that are in memory: those with someone connected, or empty
// ones waiting out their TTL.
type hub struct {
	store *store

	mu    sync.Mutex
	rooms map[string]*room
}

func newHub(s *store) *hub { return &hub{store: s, rooms: map[string]*room{}} }

// room returns the live room for code, loading it from SQLite on first use. A room
// that doesn't exist yet is returned empty: creating it is the first join's call.
func (h *hub) room(code string) (*room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.rooms[code]; ok {
		return r, nil
	}
	state, err := h.store.load(code)
	if err != nil {
		return nil, err
	}
	r := newRoom(h, code, state)
	h.rooms[code] = r
	return r, nil
}

// sweep drops rooms nobody is in: expired ones from disk, and idle ones from memory
// (they reload on demand). A room that never got created (a refused "join" of an
// unknown code) is dropped at once.
func (h *hub) sweep() {
	h.mu.Lock()
	var rooms []*room
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.mu.Unlock()

	for _, r := range rooms {
		r.mu.Lock()
		idle := len(r.conns) == 0 && !r.dead
		never := idle && !r.exists()
		r.mu.Unlock()
		if !idle {
			continue
		}
		if never || r.expire() {
			h.drop(r)
		}
	}
	// Rooms that expired while the server was down never made it into memory.
	if codes, err := h.store.expired(time.Now().Add(-emptyRoomTTL).UnixMilli()); err != nil {
		log.Printf("sweep: %v", err)
	} else {
		for _, code := range codes {
			h.mu.Lock()
			if r, ok := h.rooms[code]; ok {
				r.mu.Lock()
				if len(r.conns) == 0 {
					r.dead = true
					delete(h.rooms, code)
				}
				r.mu.Unlock()
			}
			h.mu.Unlock()
		}
	}
}

func (h *hub) drop(r *room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.conns) == 0 && h.rooms[r.code] == r {
		r.dead = true
		delete(h.rooms, r.code)
	}
}
