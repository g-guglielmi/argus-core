// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

package zabbix

import (
	"encoding/json"
	"testing"
)

// Zabbix returns itemDiscovery as an object for a discovered item and as an empty array otherwise.
func TestItemDiscoveryReadsBothShapes(t *testing.T) {
	var items []UnsupportedItem
	raw := `[{"itemid":"1","flags":"4","itemDiscovery":{"status":"1"}},{"itemid":"2","flags":"0","itemDiscovery":[]},{"itemid":"3","flags":"4","itemDiscovery":{"status":"0"}}]`
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		t.Fatal(err)
	}
	if !items[0].Discovery.Lost() || items[1].Discovery.Lost() || items[2].Discovery.Lost() {
		t.Fatalf("lost: %v %v %v, want true false false", items[0].Discovery.Lost(), items[1].Discovery.Lost(), items[2].Discovery.Lost())
	}
}
