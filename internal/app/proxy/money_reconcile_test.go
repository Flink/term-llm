package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func insertMoneyReconcileRequest(t *testing.T, db *sql.DB, id string, started int64, raw string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO requests(id,started,caller,route,method,path,outcome,data)
		VALUES(?,?,'device','route','POST','/','complete',?)`, id, started, raw)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMoneyReconcileTimeCursorCrossesBatchTies(t *testing.T) {
	s := testService(t).store
	const since, until int64 = 100, 103
	// The smallest seq is at the newest time. A tie straddles the 128th
	// recovered row, and the JSON timestamps disagree with the index keys.
	insertMoneyReconcileRequest(t, s.db, "newest", until, `{"started":1,"outcome":"complete","money":{"billing":"plan","kind":"plan"}}`)
	insertMoneyReconcileRequest(t, s.db, "before", since-1, `{}`)
	for i := 0; i < 127; i++ {
		insertMoneyReconcileRequest(t, s.db, fmt.Sprintf("first-%03d", i), since, `{}`)
	}
	insertMoneyReconcileRequest(t, s.db, "tie-left", since+1, `{`)
	insertMoneyReconcileRequest(t, s.db, "tie-right", since+1, `{}`)
	insertMoneyReconcileRequest(t, s.db, "after", until+1, `{}`)

	if err := s.reconcileMoneyHistory(context.Background(), since, until); err != nil {
		t.Fatal(err)
	}
	var total, outside int
	if err := s.db.QueryRow(`SELECT count(*),count(CASE WHEN started NOT BETWEEN ? AND ? THEN 1 END)
		FROM money_history`, since, until).Scan(&total, &outside); err != nil {
		t.Fatal(err)
	}
	if total != 130 || outside != 0 {
		t.Fatalf("recovered %d rows (%d outside the interval), want 130 in range", total, outside)
	}
	for _, tc := range []struct {
		id   string
		at   int64
		kind string
	}{
		{"newest", until, "plan"},
		{"tie-left", since + 1, "unmeasured"},
		{"tie-right", since + 1, "unmeasured"},
	} {
		var raw string
		if err := s.db.QueryRow("SELECT data FROM money_history WHERE request_id=?", tc.id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var row MoneyRecord
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			t.Fatal(err)
		}
		if row.Started.UnixMilli() != tc.at || row.Money.Kind != tc.kind || row.DeviceID != "device" || row.Provider != "route" {
			t.Errorf("recovered %s = %s", tc.id, raw)
		}
	}
}

func TestMoneyReconcileUsageBackfillCandidates(t *testing.T) {
	const frozen = `{"Money" : { "kind":"charged", "amount_micros":23 }, "future":{"keep":true}}`
	const usage = `{"protocol":"responses","usage":{"Input":42}}`
	const request = `{"usage":` + usage + `}`
	deep := strings.Repeat("[", 1100) + "0" + strings.Repeat("]", 1100)
	for _, tc := range []struct {
		name, money, request string
		candidate, backfill  bool
	}{
		{"absent", frozen, request, true, true},
		{"empty money object", `{}`, request, true, true},
		{"case folded request", frozen, `{"Usage":` + usage + `}`, true, true},
		{"escaped fold", frozen, `{"U\u017fAGE":` + usage + `}`, true, true},
		{"money exact key only", `{"Usage":null}`, request, true, true},
		{"null money usage", `{"usage":null}`, request, false, false},
		{"escaped money usage", `{"\u0075sage":null}`, request, false, false},
		{"existing money usage", `{"usage":{}}`, request, false, false},
		{"corrupt money", `{"usage":`, request, true, false},
		{"deep money", `{"future":` + deep + `}`, request, true, true},
		{"non-object money", `[]`, request, false, false},
		{"corrupt request", frozen, `{"usage":`, true, false},
		{"deep request", frozen, `{"future":` + deep + `,"usage":` + usage + `}`, true, true},
		{"non-object request", frozen, `[` + request + `]`, false, false},
		{"wrong request usage type", frozen, `{"usage":true}`, false, false},
		{"null request usage", frozen, `{"usage":null}`, false, false},
		{"no request usage", frozen, `{"nested":` + request + `}`, false, false},
		{"duplicate request usage", frozen, `{"usage":null,"usage":` + usage + `}`, true, true},
		{"later null request usage", frozen, `{"usage":` + usage + `,"Usage":null}`, true, false},
		{"request decode error", frozen, `{"usage":` + usage + `,"started":false}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testService(t).store
			const at = 100
			insertMoneyReconcileRequest(t, s.db, "request", at, tc.request)
			if _, err := s.db.Exec("INSERT INTO money_history(request_id,started,caller,route,data) VALUES('request',?,'device','route',?)", at, tc.money); err != nil {
				t.Fatal(err)
			}
			// Verify the filtering happens before decoding in Go, not just that
			// prepareUsageBackfill eventually decides to leave the row alone.
			var id, money, request string
			err := s.db.QueryRow(moneyUsageBackfillQuery, at, at).Scan(&id, &money, &request)
			if tc.candidate && err != nil || !tc.candidate && err != sql.ErrNoRows {
				t.Fatalf("query error = %v, want candidate = %v", err, tc.candidate)
			}
			for i := 0; i < 2; i++ {
				if err := s.reconcileMoneyHistory(context.Background(), at, at); err != nil {
					t.Fatal(err)
				}
			}
			var got string
			if err := s.db.QueryRow("SELECT data FROM money_history WHERE request_id='request'").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if !tc.backfill {
				if got != tc.money {
					t.Fatalf("money changed: got %q, want %q", got, tc.money)
				}
				return
			}
			var rec Record
			if err := json.Unmarshal([]byte(request), &rec); err != nil || rec.Usage == nil || rec.Usage.Usage.Input != 42 {
				t.Fatalf("invalid usage fixture: %+v, %v", rec.Usage, err)
			}
			encoded, err := json.Marshal(rec.Usage)
			if err != nil {
				t.Fatal(err)
			}
			prefix := tc.money[:len(tc.money)-1]
			if tc.money != `{}` {
				prefix += ","
			}
			if want := prefix + `"usage":` + string(encoded) + `}`; got != want {
				t.Fatalf("backfill changed something besides appending usage: got %s, want %s", got, want)
			}
		})
	}
}
