package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"time"
)

// DefaultRequestTimeout ограничивает время обработки одного HTTP-запроса.
const DefaultRequestTimeout = 2 * time.Second

const timeoutMessage = "request timed out or canceled"

func timeoutMiddleware(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		// A body read must also unblock at the deadline. Real net/http server
		// writers support this; recorders may return http.ErrNotSupported.
		controller := http.NewResponseController(w)
		deadline, _ := ctx.Deadline()
		if err := controller.SetReadDeadline(deadline); err == nil {
			finished := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				defer close(finished)
				_ = controller.SetReadDeadline(time.Now())
			})
			defer func() {
				if !stop() {
					<-finished
				}
				// net/http may still drain an unread body while flushing the
				// response. Keep its deadline; the server resets it before reuse.
			}()
		}
		if requestCanceled(ctx) {
			writeTimeoutError(w, r)
			return
		}

		// Run cooperatively in this goroutine and commit only when processing
		// finishes. Cancellation cannot race with writes or leave a handler behind.
		response := &bufferedResponse{header: make(http.Header)}
		next.ServeHTTP(response, r)
		if requestCanceled(ctx) {
			writeTimeoutError(w, r)
			return
		}
		for key, values := range response.header {
			w.Header()[key] = values
		}
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if _, err := w.Write(response.body.Bytes()); err != nil {
			log.Printf("write %s response: %v", r.URL.Path, err)
		}
	})
}

func requestCanceled(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

func writeTimeoutError(w http.ResponseWriter, r *http.Request) {
	// Skip net/http's pre-response drain of an incomplete HTTP/1 request body.
	w.Header().Set("Connection", "close")
	writeError(w, r, http.StatusGatewayTimeout, timeoutMessage)
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *bufferedResponse) Header() http.Header { return w.header }

func (w *bufferedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponse) Write(body []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(body)
}
