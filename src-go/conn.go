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
	"sync"
	"time"

	"github.com/coder/websocket"
)

// outboxSize bounds what a slow client may have queued before it is dropped.
const outboxSize = 256

// conn is one client socket. Writes go through a queue drained by a single goroutine,
// so a slow client can't stall the room that is broadcasting to it.
type conn struct {
	ws     *websocket.Conn
	id     string
	name   string
	mark   string
	joined time.Time

	// Only touched by the room under its lock, plus the reader for frameBucket.
	queueBucket bucket
	frameBucket bucket

	mu        sync.Mutex
	outbox    chan []byte
	closeWith errorCode // "": a normal close
	closed    bool
}

func newConn(ws *websocket.Conn, id, name, mark string) *conn {
	now := time.Now()
	return &conn{
		ws: ws, id: id, name: name, mark: mark, joined: now,
		queueBucket: newBucket(queueBudget, now),
		frameBucket: newBucket(frameBudget, now),
		outbox:      make(chan []byte, outboxSize),
	}
}

// send queues a text frame; a client that can't keep up is disconnected.
func (c *conn) send(msg []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.outbox <- msg:
	default:
		c.finish(errRateLimited)
	}
}

// reject tells the client why, then closes with the matching code.
func (c *conn) reject(code errorCode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.outbox <- marshal(errorFrame{T: "error", Code: code}):
	default:
	}
	c.finish(code)
}

// finish stops accepting frames; the writer flushes what is queued, then closes.
// Called with mu held.
func (c *conn) finish(code errorCode) {
	c.closed = true
	c.closeWith = code
	close(c.outbox)
}

// shutdown closes without an error frame (the reader saw the peer go away).
func (c *conn) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.finish("")
	}
}

// writeLoop flushes the outbox, pings the peer to detect dead connections, and closes
// the socket once the outbox is closed.
func (c *conn) writeLoop(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.outbox:
			if !ok {
				c.mu.Lock()
				code := c.closeWith
				c.mu.Unlock()
				if code == "" {
					c.ws.Close(websocket.StatusNormalClosure, "")
				} else {
					c.ws.Close(websocket.StatusCode(closeCodes[code]), string(code))
				}
				return
			}
			writeCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Write(writeCtx, websocket.MessageText, msg)
			done()
			if err != nil {
				c.shutdown()
				c.ws.CloseNow()
				return
			}
		case <-ticker.C:
			pingCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Ping(pingCtx)
			done()
			if err != nil {
				c.shutdown()
				c.ws.CloseNow()
				return
			}
		case <-ctx.Done():
			c.shutdown()
			c.ws.CloseNow()
			return
		}
	}
}
