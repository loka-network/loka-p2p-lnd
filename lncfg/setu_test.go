package lncfg

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultSetuNode verifies that DefaultSetuNode returns a configuration
// struct populated with the expected constant defaults.
func TestDefaultSetuNode(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	require.NotNil(t, n)

	wantHost := fmt.Sprintf("%s:%d", DefaultSetuRPCHost, DefaultSetuRPCPort)
	require.Equal(t, wantHost, n.RPCHost)
	require.Equal(t, DefaultSetuEpochInterval, n.EpochInterval)
	require.Equal(t, uint32(DefaultSetuNumConfs), n.NumConfs)
	require.Equal(t, uint16(DefaultSetuCSVDelay), n.CSVDelay)
}

// TestSetuNodeValidateValidConfig verifies that a properly filled SetuNode
// passes validation.
func TestSetuNodeValidateValidConfig(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	require.NoError(t, n.Validate())
}

// TestSetuNodeValidateEmptyHost verifies that an empty RPCHost fails.
func TestSetuNodeValidateEmptyHost(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.RPCHost = ""
	require.Error(t, n.Validate())
}

// TestSetuNodeValidateHostWithEmptyComponent verifies that ":9000" (empty host
// part) also fails validation.
func TestSetuNodeValidateHostWithEmptyComponent(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.RPCHost = ":9000"
	require.Error(t, n.Validate())
}

// TestSetuNodeValidateZeroEpochInterval verifies that a zero EpochInterval
// fails validation.
func TestSetuNodeValidateZeroEpochInterval(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.EpochInterval = 0
	require.Error(t, n.Validate())
}

// TestSetuNodeValidateNegativeEpochInterval verifies that a negative
// EpochInterval fails validation.
func TestSetuNodeValidateNegativeEpochInterval(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.EpochInterval = -1
	require.Error(t, n.Validate())
}

// TestSetuNodeValidateZeroNumConfs verifies that NumConfs=0 fails validation.
func TestSetuNodeValidateZeroNumConfs(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.NumConfs = 0
	require.Error(t, n.Validate())
}

// TestSetuNodeRPCAddrWithPort verifies that RPCAddr returns the address
// verbatim when it already contains a port.
func TestSetuNodeRPCAddrWithPort(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.RPCHost = "setu.example.com:19000"
	require.Equal(t, "setu.example.com:19000", n.RPCAddr())
}

// TestSetuNodeRPCAddrWithoutPort verifies that RPCAddr appends the default
// port when only a hostname is given (no port).
func TestSetuNodeRPCAddrWithoutPort(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	n.RPCHost = "setu.example.com"
	want := fmt.Sprintf("setu.example.com:%d", DefaultSetuRPCPort)
	require.Equal(t, want, n.RPCAddr())
}

// TestSetuNodeRPCAddrDefaultHost verifies the RPCAddr for the default
// configuration is the canonical localhost:port string.
func TestSetuNodeRPCAddrDefaultHost(t *testing.T) {
	t.Parallel()

	n := DefaultSetuNode()
	want := fmt.Sprintf("%s:%d", DefaultSetuRPCHost, DefaultSetuRPCPort)
	require.Equal(t, want, n.RPCAddr())
}
