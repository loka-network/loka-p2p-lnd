package evmtower

import "github.com/btcsuite/btclog/v2"

// Subsystem defines the logging code for this subsystem.
const Subsystem = "EVMT"

// log is a logger that is initialized with no output filters. This means the
// package will not perform any logging by default until the caller requests it
// via UseLogger.
var log = btclog.Disabled

// UseLogger uses a specified Logger to output package logging info. This should
// be used in preference to SetLogWriter if the caller is also using btclog.
func UseLogger(logger btclog.Logger) {
	log = logger
}
