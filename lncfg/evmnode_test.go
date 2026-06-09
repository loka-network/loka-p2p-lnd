package lncfg

import "testing"

const validAddr = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"

func TestEvmNodeValidate(t *testing.T) {
	t.Parallel()

	base := func() *EvmNode {
		e := DefaultEvmNode()
		e.Active = true
		e.ChainID = 8453
		e.TokenAddress = validAddr
		e.ContractAddress = validAddr
		return e
	}

	tests := []struct {
		name    string
		mutate  func(*EvmNode)
		wantErr bool
	}{
		{"valid", func(*EvmNode) {}, false},
		{"empty rpchost", func(e *EvmNode) { e.RPCHost = "" }, true},
		{"zero chainid", func(e *EvmNode) { e.ChainID = 0 }, true},
		{"bad token", func(e *EvmNode) { e.TokenAddress = "0x123" }, true},
		{"bad contract", func(e *EvmNode) {
			e.ContractAddress = "nope"
		}, true},
		{"zero numconfs", func(e *EvmNode) { e.NumConfs = 0 }, true},
		{"zero gaslimit", func(e *EvmNode) { e.GasLimit = 0 }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := base()
			tc.mutate(e)
			err := e.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestEvmNodeRPCAddr(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"http://127.0.0.1:8545": "http://127.0.0.1:8545",
		"ws://node:8546":        "ws://node:8546",
		"example.com":           "http://example.com:8545",
		"example.com:9999":      "http://example.com:9999",
	}
	for in, want := range tests {
		e := &EvmNode{RPCHost: in}
		if got := e.RPCAddr(); got != want {
			t.Errorf("RPCAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
