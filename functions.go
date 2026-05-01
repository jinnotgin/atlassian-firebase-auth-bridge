// Package p contains the Google Cloud Function entry point.
package p

import (
	"net/http"

	"github.com/jinnotgin/atlassian-firebase-auth-bridge/internal/authbridge"
)

// EntryPoint is the Google Cloud Function entry point.
func EntryPoint(w http.ResponseWriter, r *http.Request) {
	authbridge.Handler(w, r)
}
