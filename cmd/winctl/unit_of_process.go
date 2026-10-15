package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/PLN/winunitd/internal/protocol"
)

func (c *cli) unitOfProcess(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(c.stderr, "winctl unit-of-process: process ID required")
		return 2
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil || pid <= 0 {
		fmt.Fprintf(c.stderr, "winctl unit-of-process: invalid process ID %q\n", args[0])
		return 2
	}
	var r *protocol.UnitOfProcessResult
	err = c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		r, err = cl.UnitOfProcess(ctx, pid)
		return err
	})
	if err != nil {
		if protocolCode(err) == protocol.CodeNotFound {
			fmt.Fprintf(c.stderr, "winctl: %v\n", err)
			return 4
		}
		return c.rpcError(err)
	}
	fmt.Fprintf(c.stdout, "PID=%d\nUnit=%s\nInvocationID=%s\nMain=%t\n", r.PID, r.Unit, r.InvocationID, r.Main)
	if r.StopHelper {
		fmt.Fprintln(c.stdout, "StopHelper=true")
	}
	fmt.Fprintf(c.stdout, "ProcessCreatedAt=%s\nScope=%s\n", r.ProcessCreatedAt, r.Scope)
	if r.SID != "" {
		fmt.Fprintf(c.stdout, "SID=%s\n", r.SID)
	}
	return 0
}
