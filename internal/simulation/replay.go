package simulation

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/celanwang/stead/internal/delegation"
)

type ReplayResult struct {
	Decisions     int
	Approved      []delegation.Action
	Receipts      []delegation.Receipt
	Result        *delegation.Result
	ExternalCalls int
}

// Replay reapplies the kernel rules to recorded inputs with no executor dependency.
func Replay(records []delegation.Record) (ReplayResult, error) {
	var out ReplayResult
	for i, record := range records {
		switch {
		case record.Decision != nil && record.Receipt == nil:
			d := record.Decision
			result := delegation.Resolve(d.Snapshot, d.Proposal, d.At)
			if !sameJSON(result, d.Outcome) {
				return out, fmt.Errorf("record %d: decision differs from ledger", i+1)
			}
			out.Decisions++
			if result.Action != nil {
				out.Approved = append(out.Approved, *result.Action)
			}
			if result.Result != nil {
				out.Result = result.Result
			}
		case record.Receipt != nil && record.Decision == nil:
			r := record.Receipt
			action, changed, err := delegation.ApplyReceipt(r.Before, r.Receipt)
			if err != nil || !changed || !sameJSON(action, r.After) {
				return out, fmt.Errorf("record %d: receipt transition differs from ledger", i+1)
			}
			out.Receipts = append(out.Receipts, *action.Receipt)
		default:
			return out, fmt.Errorf("record %d: invalid record type", i+1)
		}
	}
	return out, nil
}

func sameJSON(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}
