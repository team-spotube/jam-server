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

import "testing"

// Expected values come from ../src/protocol.ts: a client must get the same ids from
// either server.
func TestIdentityMatchesWorker(t *testing.T) {
	if got := participantID("ABCDEF", "kkkkkkkkkkkkkkkkkkkkkk"); got != "e39K3lM6Im9TzQM4" {
		t.Errorf("participantID = %s", got)
	}
	if got := deviceMark("ABCDEF", "dddddddddddddddd", "2001:db8:1:2:3:4:5:6"); got != "384mrhQkoAp5yWVl" {
		t.Errorf("deviceMark = %s", got)
	}
	if got := deviceMark("ABCDEF", "", "1.2.3.4"); got != "" {
		t.Errorf("deviceMark without digest = %q", got)
	}
}

func TestNetworkOf(t *testing.T) {
	for ip, want := range map[string]string{
		"2001:db8::1": "2001:db8:0:0::/64",
		"::1":         "0:0:0:0::/64",
		"1.2.3.4":     "1.2.3.4",
		"unknown":     "unknown",
	} {
		if got := networkOf(ip); got != want {
			t.Errorf("networkOf(%q) = %q, want %q", ip, got, want)
		}
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"  Ada \t Lovelace ": "Ada Lovelace",
		"a\u200bb\x00c":      "abc",
		"\u202e":             "",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	long := cleanName("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if len(long) != maxNameLength {
		t.Errorf("long name has %d chars", len(long))
	}
}
