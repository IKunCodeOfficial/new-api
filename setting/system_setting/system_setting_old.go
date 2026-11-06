package system_setting

import (
	"net/url"
	"strings"
)

var ServerAddress = "http://localhost:3000"
var TaskPublicAddress = ""
var WorkerUrl = ""
var WorkerValidKey = ""
var WorkerAllowHttpImageRequestEnabled = false

func EnableWorker() bool {
	return WorkerUrl != ""
}

// IsAbsoluteHTTPAddress reports whether address, ignoring trailing slashes,
// is an absolute http or https URL with a host, as payment gateways need for
// the pages they return customers to.
func IsAbsoluteHTTPAddress(address string) bool {
	parsed, err := url.Parse(strings.TrimRight(address, "/"))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}
