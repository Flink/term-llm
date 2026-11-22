package poll

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage"
)

func TestSiblingBindingsKeepIndependentSnapshots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := openTestCache(t)
		var failA bool
		var aCalls, bCalls int
		a := Source{ID: "chatgpt", Route: "a", Key: "account-a", Fetch: func(context.Context) (nativeusage.Report, error) {
			aCalls++
			if failA {
				return nativeusage.Report{}, errors.New("fixture failure")
			}
			return nativeusage.Report{Plan: "a"}, nil
		}}
		b := Source{ID: "chatgpt", Route: "b", Key: "account-b", Fetch: func(context.Context) (nativeusage.Report, error) { bCalls++; return nativeusage.Report{Plan: "b"}, nil }}
		c.Reconcile([]Source{a, b})
		c.Start(t.Context())
		defer c.Close()
		synctest.Wait()
		first := c.Snapshot().Providers
		if len(first) != 2 || first[0].Route != "a" || first[1].Route != "b" || first[0].Report.Plan != "a" || first[1].Report.Plan != "b" {
			t.Fatalf("first snapshot: %+v", first)
		}
		failA = true
		requestRefresh(t, c)
		synctest.Wait()
		next := c.Snapshot().Providers
		if aCalls != 2 || bCalls != 2 || next[0].Error == "" || next[0].Report != first[0].Report || next[1].Error != "" || next[1].Report == first[1].Report {
			t.Fatalf("independent refresh: %+v", next)
		}
		b.Route = ""
		c.Reconcile([]Source{b})
		synctest.Wait()
		last := c.Snapshot().Providers
		if len(last) != 1 || last[0].Route != "" || last[0].Report != next[1].Report || bCalls != 2 {
			t.Fatalf("removed sibling lost fresh snapshot: %+v", last)
		}
	})
}
