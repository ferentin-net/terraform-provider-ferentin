package provider

import (
	"os"
	"runtime"
	"strings"
)

// trustOverrideWarning is the warning Configure adds when this run's
// environment lets SSL_CERT_FILE or SSL_CERT_DIR replace the macOS or Windows
// certificate store after all, or "" when it does not.
//
// go.mod sets `godebug x509sslcertoverrideplatform=0`, but that is only the
// default: the GODEBUG variable outranks it. Whoever controls the environment
// terraform runs in controls this either way, so it is a warning and not a
// refusal; what it buys is that the override is never silent, because a CA
// file and a proxy together can read the connection to the auth server and
// the admin API. On Linux these variables are the trust store whatever
// GODEBUG says, as they always were.
func trustOverrideWarning(goos string, getenv func(string) string) string {
	if goos != "darwin" && goos != "windows" {
		return ""
	}
	if !godebugOverridesTrust(getenv("GODEBUG")) {
		return ""
	}
	var set []string
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if getenv(name) != "" {
			set = append(set, name)
		}
	}
	if len(set) == 0 {
		return ""
	}
	return "GODEBUG sets x509sslcertoverrideplatform, so " + strings.Join(set, " and ") +
		" replace the system certificate store for this run, including for the connection to " +
		"Ferentin's auth server and admin API. Unset GODEBUG's x509sslcertoverrideplatform, or " +
		"install the CA in the system store instead."
}

// godebugOverridesTrust reports whether a GODEBUG value may set
// x509sslcertoverrideplatform to anything but 0, as the Go runtime reads it:
// entries split on ',', the last one naming the key wins, the name ends at
// the first '=', and nothing is trimmed. A '#' starts a bisect pattern, and
// for a call stack the pattern does not match the runtime reads the value as
// "", which is not 0, so any pattern counts as an override: whether it matches
// depends on the stack. Adapted from ferentin-cli-app's check of the same
// name, which lives in its command package and cannot be imported.
func godebugOverridesTrust(godebug string) bool {
	entries := strings.Split(godebug, ",")
	for i := len(entries) - 1; i >= 0; i-- {
		name, value, ok := strings.Cut(entries[i], "=")
		if !ok || name != "x509sslcertoverrideplatform" {
			continue
		}
		return value != "0"
	}
	return false
}

// trustOverrideWarningForThisRun is trustOverrideWarning for this process.
func trustOverrideWarningForThisRun() string {
	return trustOverrideWarning(runtime.GOOS, os.Getenv)
}
