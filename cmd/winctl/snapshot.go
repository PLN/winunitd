package main

import (
	"context"
	"encoding/json"

	"github.com/PLN/winunitd/internal/protocol"
)

func (c *cli) snapshot() int {
	var got *protocol.SnapshotResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		got, err = cl.Snapshot(ctx)
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	encoder := json.NewEncoder(c.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(got); err != nil {
		return c.rpcError(err)
	}
	return 0
}
