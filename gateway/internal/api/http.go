package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

type errorResponse struct {
	Error string `json:"error"`
}

var errExtraJSON = errors.New("request body must contain only one JSON object")

// decodeJSON проверяет весь документ до изменения состояния Ledger.
func decodeJSON[T any](body io.Reader, request *T) error {
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(request); err != nil {
		return err
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errExtraJSON
	}
	return nil
}

func writeJSON[T any](w http.ResponseWriter, r *http.Request, status int, value T) {
	body, err := json.Marshal(value)
	if err != nil {
		log.Printf("encode %s response: %v", r.URL.Path, err)
		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		log.Printf("write %s response: %v", r.URL.Path, err)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, r, status, errorResponse{Error: message})
}

func writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, errExtraJSON) || errors.As(err, &syntaxError) || errors.As(err, &typeError) {
		writeError(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	writeInternalError(w, r, err)
}

func writeInternalError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("handle %s %s: %v", r.Method, r.URL.Path, err)
	writeError(w, r, http.StatusInternalServerError, "internal server error")
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			log.Printf("method=%s path=%s duration=%s", r.Method, r.URL.Path, time.Since(start))
		}()
		next.ServeHTTP(w, r)
	})
}
