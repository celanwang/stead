package simulation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/celanwang/stead/internal/delegation"
)

// Executor records each simulated delivery in a directory independent of MySQL.
type Executor struct {
	dir string
	// AfterDelivery runs after the external record exists, before the receipt returns.
	AfterDelivery func(delegation.Receipt)
}

func NewExecutor(dir string) (*Executor, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Executor{dir: dir}, nil
}

func (e *Executor) Execute(ctx context.Context, a delegation.Action) (delegation.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return delegation.Receipt{}, err
	}
	f, err := os.CreateTemp(e.dir, ".delivery-")
	if err != nil {
		return delegation.Receipt{}, err
	}
	defer os.Remove(f.Name())

	err = json.NewEncoder(f).Encode(a)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return delegation.Receipt{}, err
	}
	name := strings.TrimPrefix(filepath.Base(f.Name()), ".")
	if err := os.Link(f.Name(), filepath.Join(e.dir, name)); err != nil {
		return delegation.Receipt{}, err
	}
	receipt := delegation.Receipt{ActionID: a.ID, State: delegation.Delivered, Detail: name}
	if e.AfterDelivery != nil {
		e.AfterDelivery(receipt)
	}
	return receipt, nil
}

func (e *Executor) Deliveries() ([]delegation.Action, error) {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return nil, err
	}
	var actions []delegation.Action
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "delivery-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var a delegation.Action
		if err := json.Unmarshal(data, &a); err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, nil
}
