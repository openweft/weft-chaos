// process_kill.go — injector `kind = "process_kill"` :
// SIGKILLs a process on every host matching the selector, then
// (optionally) lets the host's supervisor restart it. The point is
// to exercise the chaos paths weft v0.4.1+ wires : etcdcoord
// election failover, cross-host VM claim, respawn-on-host-down,
// systemd watchdog reset. Killing weft-agent itself is the canonical
// "what if the control plane on one DC vanishes ?" probe.
//
// Scenario block :
//
//	injector "kill-dc2-agent" {
//	  kind       = "process_kill"
//	  selector   = "az=dc2"
//	  at_offset  = "15m"
//	  recover_at = "16m"        # optional ; weft-agent restarts via systemd
//	  params = {
//	    target = "weft-agent"   # default ; supports weft-microvm-agent, etcd, etc.
//	    signal = "KILL"         # KILL | TERM | INT — default KILL
//	  }
//	}
//
// Scaffold-only : Apply/Revert validate + log. Real wiring goes
// through wclient.KillProcessOn(ctx, host, target, signal) once the
// gRPC channel to weft-agent lands ; weft-agent already has the
// process supervisor map (used by ZombieGC) so the receive side is
// cheap.

package injectors

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/openweft/weft-chaos/internal/scenario"
)

// ProcessKill implements `kind = "process_kill"`. Targets every
// host in the AZ from the selector + sends `signal` to the named
// `target` process.
type ProcessKill struct {
	Spec   scenario.Injector
	Logger *slog.Logger
}

func (p *ProcessKill) Name() string { return p.Spec.Name }

func (p *ProcessKill) Apply(ctx context.Context) error {
	az, ok := parseSelector(p.Spec.Selector, "az")
	if !ok {
		return fmt.Errorf("process_kill : selector %q : expected az=<name>", p.Spec.Selector)
	}
	target := p.Spec.Params["target"]
	if target == "" {
		target = "weft-agent"
	}
	signal := p.Spec.Params["signal"]
	if signal == "" {
		signal = "KILL"
	}
	if !validSignal(signal) {
		return fmt.Errorf("process_kill : signal %q : expected KILL|TERM|INT", signal)
	}
	p.Logger.Info("applying process_kill",
		"name", p.Spec.Name, "az", az,
		"target", target, "signal", signal)
	// TODO(weft-chaos) : wclient.KillProcessOn(ctx, host, target, signal)
	// per host in AZ. Continue on individual errors — the test is
	// "supervisor reacts to the kills it sees", not "every API
	// call succeeded".
	return nil
}

func (p *ProcessKill) Revert(ctx context.Context) error {
	if p.Spec.RecoverAt == "" {
		return nil
	}
	// process_kill has no symmetric "un-kill" : the supervisor (systemd
	// for weft-agent, weft-init for in-VM processes) restarts the
	// process. Revert here is a marker on the timeline — useful when
	// an operator wants to assert "by RecoverAt, the process is
	// alive again" via a paired healthy_endpoint invariant.
	az, _ := parseSelector(p.Spec.Selector, "az")
	target := p.Spec.Params["target"]
	if target == "" {
		target = "weft-agent"
	}
	p.Logger.Info("process_kill recovery checkpoint",
		"name", p.Spec.Name, "az", az, "target", target)
	return nil
}

// validSignal is the allow-list. We could broaden but Apple's
// macOS dev hosts don't have RTMIN ; sticking to the POSIX signals
// keeps the harness portable.
func validSignal(s string) bool {
	switch s {
	case "KILL", "TERM", "INT":
		return true
	}
	return false
}
