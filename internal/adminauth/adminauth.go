// Package adminauth defines the small B01 -> management handler boundary.
// It deliberately has no Controller-internal imports, cookie or login implementation.
package adminauth

import "net/http"

// Guard is implemented by B01 and injected by the Controller composition root.
// Read requires a valid administrator session. Write ALSO checks allowed Origin
// and CSRF before calling next, including for sensitive read-like POST actions.
// Failure writes the domain error envelope (401/403), never calls next, never
// redirects. These wrappers must not be installed on 9443 enrollment or mTLS.
type Guard interface {
	Read(next http.Handler) http.Handler
	Write(next http.Handler) http.Handler
}
