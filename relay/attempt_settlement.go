package relay

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// settleCompletedAttempt is the final billing gate for text-generation
// handlers. It keeps a timed-out or no-output attempt from creating a consume
// log before the controller turns it into a retryable channel error.
func settleCompletedAttempt(info *relaycommon.RelayInfo, settle func()) *types.NewAPIError {
	if err := info.AttemptSettlementError(); err != nil {
		return err
	}
	settle()
	return nil
}
