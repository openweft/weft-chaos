// process_kill_test.go — pin parameter validation + signal
// allow-list + no-op-when-scaffold behaviour of the ProcessKill
// injector.

package injectors

import (
	"context"
	"strings"
	"testing"

	"github.com/openweft/weft-chaos/internal/scenario"
)

func TestProcessKill_RejectsMissingSelector(t *testing.T) {
	inj := &ProcessKill{
		Spec: scenario.Injector{
			Name:     "no-az",
			Kind:     "process_kill",
			Selector: "host=foo", // wrong key
		},
		Logger: nullLogger(),
	}
	err := inj.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply with missing az= = nil err, want config error")
	}
	if !strings.Contains(err.Error(), "az=") {
		t.Errorf("err = %v, want mention of az=", err)
	}
}

func TestProcessKill_RejectsBadSignal(t *testing.T) {
	inj := &ProcessKill{
		Spec: scenario.Injector{
			Name:     "bad-signal",
			Kind:     "process_kill",
			Selector: "az=dc2",
			Params:   map[string]string{"signal": "USR3"},
		},
		Logger: nullLogger(),
	}
	err := inj.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply with bad signal = nil err, want config error")
	}
	if !strings.Contains(err.Error(), "KILL|TERM|INT") {
		t.Errorf("err = %v, want mention of KILL|TERM|INT", err)
	}
}

func TestProcessKill_DefaultTargetAndSignal(t *testing.T) {
	// No params → target=weft-agent, signal=KILL, Apply succeeds.
	inj := &ProcessKill{
		Spec: scenario.Injector{
			Name:     "defaults",
			Kind:     "process_kill",
			Selector: "az=dc1",
		},
		Logger: nullLogger(),
	}
	if err := inj.Apply(context.Background()); err != nil {
		t.Fatalf("Apply with defaults = %v, want nil", err)
	}
}

func TestProcessKill_AcceptsAllValidSignals(t *testing.T) {
	for _, s := range []string{"KILL", "TERM", "INT"} {
		inj := &ProcessKill{
			Spec: scenario.Injector{
				Name:     "sig-" + s,
				Kind:     "process_kill",
				Selector: "az=dc1",
				Params:   map[string]string{"signal": s},
			},
			Logger: nullLogger(),
		}
		if err := inj.Apply(context.Background()); err != nil {
			t.Errorf("signal=%s rejected : %v", s, err)
		}
	}
}

func TestProcessKill_CustomTarget(t *testing.T) {
	// Killing weft-microvm-agent or etcd inside the host is a
	// legit chaos move ; just make sure Apply doesn't reject.
	inj := &ProcessKill{
		Spec: scenario.Injector{
			Name:     "etcd-kill",
			Kind:     "process_kill",
			Selector: "az=dc2",
			Params:   map[string]string{"target": "etcd"},
		},
		Logger: nullLogger(),
	}
	if err := inj.Apply(context.Background()); err != nil {
		t.Errorf("custom target rejected : %v", err)
	}
}

func TestProcessKill_RevertNoOpWithoutRecoverAt(t *testing.T) {
	inj := &ProcessKill{
		Spec: scenario.Injector{
			Name:     "no-recover",
			Kind:     "process_kill",
			Selector: "az=dc2",
		},
		Logger: nullLogger(),
	}
	if err := inj.Revert(context.Background()); err != nil {
		t.Errorf("Revert without RecoverAt = %v, want nil", err)
	}
}

func TestProcessKill_RevertCheckpointWhenScheduled(t *testing.T) {
	// With RecoverAt set, Revert logs a checkpoint but still
	// returns nil (the supervisor restarts the process ; chaos
	// can't and shouldn't pretend to undo a SIGKILL).
	inj := &ProcessKill{
		Spec: scenario.Injector{
			Name:      "scheduled",
			Kind:      "process_kill",
			Selector:  "az=dc2",
			RecoverAt: "16m",
		},
		Logger: nullLogger(),
	}
	if err := inj.Revert(context.Background()); err != nil {
		t.Errorf("Revert with RecoverAt = %v, want nil", err)
	}
}
