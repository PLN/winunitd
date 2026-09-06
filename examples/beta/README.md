# Try the beta unit format

These examples use Windows PowerShell from the standard `C:\Windows` location.
Adjust both paths if Windows is installed elsewhere. The worker prints one line
every five seconds; it needs no downloads or application configuration.

Verify both files, copy them into the system unit directory as administrator,
and run these commands from an elevated terminal:

```powershell
winctl verify --file .\worker.service .\worker.target
# Copy worker.service and worker.target to C:\ProgramData\winunitd\units\.
winctl daemon-reload
winctl start worker.target
winctl logs worker.service
winctl restart worker.target
winctl status worker.service
winctl stop worker.target
```

The restart gives the worker a new invocation ID. Stop terminates the worker's
job; no graceful signal is sent. To try workload crash recovery, terminate only
the worker PID reported by status, then observe its new invocation after two
seconds. Use a disposable test system for failure experiments.

Enable with `winctl enable worker.service` to start at daemon boot. Disable with
`winctl disable worker.service` before removing the examples and reloading. The
group is a control convenience; enablement is explicit and the examples are
never installed as enabled workloads by the package.
