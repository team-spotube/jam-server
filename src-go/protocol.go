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

// Wire protocol between Spotube and the jam room server (version 1), mirroring
// ../src/protocol.ts. Every frame is a JSON object with a `t` discriminator. The
// server never looks inside `body`: queue, anchor and command payloads are owned by
// the app and only routed (and, for queue/anchor, retained) here.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	protocolVersion = 1
	maxNameLength   = 40
	maxParticipants = 32
	// A long queue with cover URLs is the largest frame, a few hundred KB at most.
	maxFrameBytes = 512 * 1024
	maxBans       = 256
	// Everyone who has been in the room, to tell a returning member from a new arrival.
	maxMembers = 512

	// A host that drops keeps the room for this long before someone else takes over.
	hostGrace = 30 * time.Second
	// An empty room keeps its queue for this long, so everyone can rejoin after a blip.
	emptyRoomTTL = 10 * time.Minute

	// Keep-alive, sent as a bare text frame.
	keepalivePing = "ping"
	keepalivePong = "pong"
)

var (
	// Same alphabet as the app's JamRoomCode (no I, O, 0, 1).
	roomCode = regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{6}$`)
	// A participant's secret, chosen by the client. Others only see the id derived from it.
	clientKey = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)
	// Optional per-room device digest from the client.
	deviceDigest = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

// budget is a token bucket's shape: it holds up to burst tokens and refills perSecond.
type budget struct{ burst, perSecond float64 }

// Per-socket token buckets: a client that floods is disconnected.
var (
	frameBudget = budget{burst: 40, perSecond: 6}
	queueBudget = budget{burst: 5, perSecond: 1}
)

type errorCode string

const (
	errBadRequest    errorCode = "bad_request"
	errRoomTaken     errorCode = "room_taken"
	errRoomNotFound  errorCode = "room_not_found"
	errRoomFull      errorCode = "room_full"
	errBanned        errorCode = "banned"
	errKicked        errorCode = "kicked"
	errReplaced      errorCode = "replaced"
	errFrameTooLarge errorCode = "frame_too_large"
	errRateLimited   errorCode = "rate_limited"
)

// WebSocket close codes the app maps back to an errorCode.
var closeCodes = map[errorCode]int{
	errBadRequest:    4000,
	errRoomTaken:     4001,
	errRoomNotFound:  4004,
	errRoomFull:      4005,
	errBanned:        4003,
	errKicked:        4008,
	errReplaced:      4009,
	errFrameTooLarge: 4013,
	errRateLimited:   4029,
}

type joinMode string

const (
	modeCreate joinMode = "create"
	modeJoin   joinMode = "join"
)

// clientFrame is any frame a client may send; unused fields stay zero.
type clientFrame struct {
	T      string          `json:"t"`
	ID     json.RawMessage `json:"id"`
	C      json.RawMessage `json:"c"`
	Body   json.RawMessage `json:"body"`
	Target string          `json:"target"`
	Ban    bool            `json:"ban"`
}

type participant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// rawOrNull makes a missing body serialize as null, like the Worker does.
func rawOrNull(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

type welcomeFrame struct {
	T            string          `json:"t"`
	V            int             `json:"v"`
	You          string          `json:"you"`
	Host         *string         `json:"host"`
	Participants []participant   `json:"participants"`
	Queue        json.RawMessage `json:"queue"`
	Anchor       json.RawMessage `json:"anchor"`
}

type pongFrame struct {
	T  string          `json:"t"`
	ID json.RawMessage `json:"id,omitempty"`
	C  json.RawMessage `json:"c,omitempty"`
	S  int64           `json:"s"`
}

type presenceFrame struct {
	T            string        `json:"t"`
	Host         *string       `json:"host"`
	Participants []participant `json:"participants"`
}

type bodyFrame struct {
	T    string          `json:"t"`
	Body json.RawMessage `json:"body"`
}

type cmdFrame struct {
	T    string          `json:"t"`
	From string          `json:"from"`
	Body json.RawMessage `json:"body"`
}

type errorFrame struct {
	T    string    `json:"t"`
	Code errorCode `json:"code"`
}

func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal %T: %v", v, err))
	}
	return b
}

// shortHash is the first 96 bits of SHA-256, base64url.
func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// participantID is the public id for key in room code: a different id per room, so ids
// can't link rooms.
func participantID(code, key string) string {
	return shortHash(code + ":" + key)
}

// deviceMark is what a ban remembers about a device besides its participant id: the
// client's device digest combined with its network, hashed again so the room only
// ever stores an opaque value. Empty without a digest.
func deviceMark(code, digest, ip string) string {
	if digest == "" {
		return ""
	}
	return shortHash(code + ":" + digest + ":" + networkOf(ip))
}

// networkOf is an IPv4 address, or an IPv6 /64 (one home or phone gets a whole /64).
func networkOf(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	full := parsed.To16()
	groups := make([]string, 4)
	for i := range groups {
		groups[i] = strconv.FormatUint(uint64(full[i*2])<<8|uint64(full[i*2+1]), 16)
	}
	return strings.Join(groups, ":") + "::/64"
}

var whitespace = regexp.MustCompile(`\s+`)

// cleanName: names are shown to everyone, so no control or formatting characters, no padding.
func cleanName(raw string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.C, r) {
			return -1
		}
		return r
	}, raw)
	name := strings.TrimSpace(whitespace.ReplaceAllString(stripped, " "))
	if runes := []rune(name); len(runes) > maxNameLength {
		name = strings.TrimSpace(string(runes[:maxNameLength]))
	}
	return name
}
