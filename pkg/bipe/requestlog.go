package bipe

import (
	"context"
	"net/http"

	"maunium.net/go/mautrix"
)

// NoRequestBodiesInLog stops mautrix-go from writing request bodies into its
// request log (bipe-chat/bipe#1377). mautrix-go v0.31.0 adds the body as
// req_body at every level, from the request's context logger, so a level on
// the client logger is not enough. The hook runs on each attempt before the
// send and clears the body value on the same request that the log line reads
// after it. The log keeps the method, the URL, the status and the duration.
// An existing hook still runs.
func NoRequestBodiesInLog(cli *mautrix.Client) {
	next := cli.RequestHook
	cli.RequestHook = func(req *http.Request) {
		*req = *req.WithContext(context.WithValue(req.Context(), mautrix.LogBodyContextKey, nil))
		if next != nil {
			next(req)
		}
	}
}
