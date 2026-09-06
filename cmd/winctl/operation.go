package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/PLN/winunitd/internal/protocol"
)

func (c *cli) operation(args []string) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(c.stderr, "winctl operation: operation ID required")
		return 2
	}
	var op *protocol.OperationResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		op, err = cl.Operation(ctx, args[0])
		return err
	})
	if err != nil {
		if protocolCode(err) == protocol.CodeNotFound {
			fmt.Fprintf(c.stderr, "winctl: %v\n", err)
			return 4
		}
		return c.rpcError(err)
	}
	fmt.Fprintf(c.stdout, "OperationID=%s\nUnit=%s\nAction=%s\nOrigin=%s\nState=%s\nConfigRevision=%s\nStartedAt=%s\n", op.ID, op.Unit, op.Action, op.Origin, op.State, op.ConfigRevision, op.StartedAt)
	if op.CompletedAt != "" {
		fmt.Fprintf(c.stdout, "CompletedAt=%s\n", op.CompletedAt)
	}
	if op.Error != "" {
		fmt.Fprintf(c.stdout, "Error=%s\n", op.Error)
	}
	if op.ErrorTruncated {
		fmt.Fprintln(c.stdout, "ErrorTruncated=true")
	}
	switch op.State {
	case "succeeded":
		return 0
	case "running":
		return 3
	default:
		return 1
	}
}
