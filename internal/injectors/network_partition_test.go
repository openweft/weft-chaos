// network_partition_test.go — pin parameter validation +
// no-op-when-scaffold behaviour of the NetworkPartition injector.

package injectors

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/openweft/weft-chaos/internal/scenario"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNetworkPartition_RejectsMissingSelectorKey(t *testing.T) {
	inj := &NetworkPartition{
		Spec: scenario.Injector{
			Name:     "no-az",
			Kind:     "network_partition",
			Selector: "rack=r1", // wrong key
		},
		Logger: nullLogger(),
	}
	err := inj.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply with selector missing az= = nil err, want config error")
	}
	if !strings.Contains(err.Error(), "az=") {
		t.Errorf("err = %v, want mention of az=", err)
	}
}

func TestNetworkPartition_RejectsBadMode(t *testing.T) {
	inj := &NetworkPartition{
		Spec: scenario.Injector{
			Name:     "bad-mode",
			Kind:     "network_partition",
			Selector: "az=dc2",
			Params:   map[string]string{"mode": "shrug"},
		},
		Logger: nullLogger(),
	}
	err := inj.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply with bad mode = nil err, want config error")
	}
	if !strings.Contains(err.Error(), "drop|reject") {
		t.Errorf("err = %v, want mention of drop|reject", err)
	}
}

func TestNetworkPartition_DefaultModeDrop(t *testing.T) {
	// No mode set → default "drop", Apply succeeds (scaffold no-op).
	inj := &NetworkPartition{
		Spec: scenario.Injector{
			Name:     "default-mode",
			Kind:     "network_partition",
			Selector: "az=dc2",
		},
		Logger: nullLogger(),
	}
	if err := inj.Apply(context.Background()); err != nil {
		t.Fatalf("Apply with default mode = %v, want nil", err)
	}
}

func TestNetworkPartition_AcceptsRejectMode(t *testing.T) {
	inj := &NetworkPartition{
		Spec: scenario.Injector{
			Name:     "reject-mode",
			Kind:     "network_partition",
			Selector: "az=dc1",
			Params:   map[string]string{"mode": "reject"},
		},
		Logger: nullLogger(),
	}
	if err := inj.Apply(context.Background()); err != nil {
		t.Fatalf("Apply with mode=reject = %v, want nil", err)
	}
}

func TestNetworkPartition_RevertNoOpWithoutRecoverAt(t *testing.T) {
	inj := &NetworkPartition{
		Spec: scenario.Injector{
			Name:     "no-recover",
			Kind:     "network_partition",
			Selector: "az=dc2",
			// RecoverAt empty
		},
		Logger: nullLogger(),
	}
	if err := inj.Revert(context.Background()); err != nil {
		t.Errorf("Revert without RecoverAt = %v, want nil", err)
	}
}

func TestNetworkPartition_RevertReturnsNilWhenScheduled(t *testing.T) {
	inj := &NetworkPartition{
		Spec: scenario.Injector{
			Name:      "with-recover",
			Kind:      "network_partition",
			Selector:  "az=dc2",
			RecoverAt: "5m",
		},
		Logger: nullLogger(),
	}
	if err := inj.Revert(context.Background()); err != nil {
		t.Errorf("Revert with RecoverAt = %v, want nil (scaffold)", err)
	}
}
