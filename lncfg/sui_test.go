package lncfg

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultSuiNode verifies that DefaultSuiNode returns a configuration
// struct populated with the expected constant defaults.
func TestDefaultSuiNode(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	require.NotNil(t, n)

	wantHost := fmt.Sprintf("%s:%d", DefaultSuiRPCHost, DefaultSuiRPCPort)
	require.Equal(t, wantHost, n.RPCHost)
	require.Equal(t, DefaultSuiEpochInterval, n.EpochInterval)
	require.Equal(t, uint32(DefaultSuiNumConfs), n.NumConfs)
	require.Equal(t, uint16(DefaultSuiCSVDelay), n.CSVDelay)
}

// TestSuiNodeValidateValidConfig verifies that a properly filled SuiNode
// passes validation.
func TestSuiNodeValidateValidConfig(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	require.NoError(t, n.Validate())
}

// TestSuiNodeValidateEmptyHost verifies that an empty RPCHost fails.
func TestSuiNodeValidateEmptyHost(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.RPCHost = ""
	require.Error(t, n.Validate())
}

// TestSuiNodeValidateHostWithEmptyComponent verifies that ":9000" (empty host
// part) also fails validation.
func TestSuiNodeValidateHostWithEmptyComponent(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.RPCHost = ":9000"
	require.Error(t, n.Validate())
}

// TestSuiNodeValidateZeroEpochInterval verifies that a zero EpochInterval
// fails validation.
func TestSuiNodeValidateZeroEpochInterval(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.EpochInterval = 0
	require.Error(t, n.Validate())
}

// TestSuiNodeValidateNegativeEpochInterval verifies that a negative
// EpochInterval fails validation.
func TestSuiNodeValidateNegativeEpochInterval(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.EpochInterval = -1
	require.Error(t, n.Validate())
}

// TestSuiNodeValidateZeroNumConfs verifies that NumConfs=0 fails validation.
func TestSuiNodeValidateZeroNumConfs(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.NumConfs = 0
	require.Error(t, n.Validate())
}

// TestSuiNodeRPCAddrWithPort verifies that RPCAddr returns the address
// verbatim when it already contains a port.
func TestSuiNodeRPCAddrWithPort(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.RPCHost = "sui.example.com:19000"
	require.Equal(t, "sui.example.com:19000", n.RPCAddr())
}

// TestSuiNodeRPCAddrWithoutPort verifies that RPCAddr appends the default
// port when only a hostname is given (no port).
func TestSuiNodeRPCAddrWithoutPort(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	n.RPCHost = "sui.example.com"
	want := fmt.Sprintf("sui.example.com:%d", DefaultSuiRPCPort)
	require.Equal(t, want, n.RPCAddr())
}

// TestSuiNodeRPCAddrDefaultHost verifies the RPCAddr for the default
// configuration is the canonical localhost:port string.
func TestSuiNodeRPCAddrDefaultHost(t *testing.T) {
	t.Parallel()

	n := DefaultSuiNode()
	want := fmt.Sprintf("%s:%d", DefaultSuiRPCHost, DefaultSuiRPCPort)
	require.Equal(t, want, n.RPCAddr())
}
