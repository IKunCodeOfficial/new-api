package controller

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/setting/system_setting"
)

func paymentReturnPath(suffix string) string {
	base := strings.TrimRight(system_setting.ServerAddress, "/")
	return base + suffix
}

// absolutePaymentReturnURL is paymentReturnPath for gateways that need an
// absolute return URL. It fails while the server address is not an absolute
// http(s) URL, instead of sending the customer to a relative or empty page.
func absolutePaymentReturnURL(suffix string) (string, error) {
	base := strings.TrimRight(system_setting.ServerAddress, "/")
	if !system_setting.IsAbsoluteHTTPAddress(base) {
		return "", fmt.Errorf("server address %q is not an absolute http(s) URL", base)
	}
	return base + suffix, nil
}
