package privacy

import (
	"sync"

	"github.com/QuantumNous/new-api/common"

	pb "github.com/QuantumNous/new-api/service/privacy/filterpb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// EnvPrivacyFilterAddr configures the privacy-filter gRPC service address.
	EnvPrivacyFilterAddr = "PRIVACY_FILTER_ADDR"
	defaultAddr          = "localhost:8089"
)

var (
	clientOnce   sync.Once
	cachedClient pb.PrivacyFilterClient

	// Test seam: when testOverride is true, getClient returns testClient
	// (which may be nil to exercise the fail-open "no client" path).
	testMu       sync.RWMutex
	testOverride bool
	testClient   pb.PrivacyFilterClient
)

// SetClientForTest injects a fake client for tests. Pass nil to exercise the
// "client unavailable" fail-open path. Call ResetClientForTest to restore
// normal behaviour.
func SetClientForTest(cl pb.PrivacyFilterClient) {
	testMu.Lock()
	testOverride = true
	testClient = cl
	testMu.Unlock()
}

// ResetClientForTest restores the real client lookup.
func ResetClientForTest() {
	testMu.Lock()
	testOverride = false
	testClient = nil
	testMu.Unlock()
}

// getClient lazily constructs the singleton gRPC client. grpc.NewClient is
// non-blocking (no dial happens here); connection errors surface on the RPC
// call instead, where they are handled fail-open. Returns nil only if the
// client could not even be constructed.
func getClient() pb.PrivacyFilterClient {
	testMu.RLock()
	if testOverride {
		cl := testClient
		testMu.RUnlock()
		return cl
	}
	testMu.RUnlock()

	clientOnce.Do(func() {
		addr := common.GetEnvOrDefaultString(EnvPrivacyFilterAddr, defaultAddr)
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			common.SysError("privacy-filter: failed to create gRPC client: " + err.Error())
			return
		}
		cachedClient = pb.NewPrivacyFilterClient(conn)
	})
	return cachedClient
}
