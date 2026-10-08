package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestManagedServiceLifecycleUsesGatewayBeforeNode(t *testing.T) {
	previousSystemctl := updateSystemctl
	previousNFTCommand := nftCommand
	var calls []string
	updateSystemctl = func(arguments ...string) ([]byte, error) {
		calls = append(calls, strings.Join(arguments, " "))
		return nil, nil
	}
	nftCommand = fakeNFTCommand
	t.Cleanup(func() {
		updateSystemctl = previousSystemctl
		nftCommand = previousNFTCommand
	})

	if err := stopManagedNodeServices(false); err != nil {
		t.Fatal(err)
	}
	if err := startManagedNodeServices(true); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"stop speedquality-node-gate.service",
		"stop speedquality-node.service",
		"enable --now speedquality-node.service",
		"enable --now speedquality-node-gate.service",
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("systemctl calls=%q", calls)
	}
}
