/*
Parts of this file contain code from github.com/gin-contrib/zap, released under
the below license:

MIT License

Copyright (c) 2017 gin-contrib

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

// Package chizap provides a logging and recovery middleware for chi using zap.
package chizap

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type ctxKey struct{}

// Logger returns a middleware handler that logs all requests using the passed
// [zap.Logger].
//
// Additionally, it saves a logger instance in the request context, to be
// retrieved using [Get].
// Besides the fields already added to the logger, that instance also holds
// the following fields:
//   - request_id: the request ID, if set by
//     [github.com/go-chi/chi/v5/middleware.RequestID]
//   - proto: the request protocol
//   - method: the HTTP method of the request
//   - path: the path of the request
//   - query: the query string of the request
//   - remote: the remote address of the client
//   - user_agent: the user agent of the client
//   - referer: the referer of the client
//
// If you don't want a certain path prefix to be logged, you may specify it as
// one of the excludedPaths.
// Even if a path prefix is echoed, the logger will still be saved in the
// request context.
func Logger(l *zap.Logger, excludedPaths ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var excluded bool
			for _, path := range excludedPaths {
				if strings.HasPrefix(r.URL.Path, path) {
					excluded = true
					break
				}
			}

			var start time.Time
			if !excluded {
				start = time.Now()
			}

			rl := l.With(
				zap.String("request_id", middleware.GetReqID(r.Context())),
				zap.String("proto", r.Proto),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("query", r.URL.RawQuery),
				zap.String("remote", r.RemoteAddr),
				zap.String("user_agent", r.UserAgent()),
				zap.String("referer", r.Referer()),
			)
			set(r, rl)

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			if !excluded {
				lat := time.Since(start)
				rl.Info(r.Method+" "+r.URL.Path,
					zap.Int("status", ww.Status()),
					zap.Int("bytes_written", ww.BytesWritten()),
					zap.Duration("latency", lat),
				)
			}
		})
	}
}

// Get returns the [*zap.Logger] instance saved in the request context by the
// [Logger] middleware.
//
// Must be called after the [Logger] middleware.
func Get(r *http.Request) *zap.Logger {
	return r.Context().Value(ctxKey{}).(*zap.Logger)
}

// GetSugared is shorthand for:
//
//	Get(r).Sugar()
func GetSugared(r *http.Request) *zap.SugaredLogger {
	return Get(r).Sugar()
}

func set(r *http.Request, l *zap.Logger) {
	*r = *r.WithContext(context.WithValue(r.Context(), ctxKey{}, l))
}

// Recoverer recovers from panics and logs the stack trace using the logger
// added by [Logger].
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// Check for a broken connection, as it is not really a
			// condition that warrants a panic stack trace.
			var brokenPipe bool
			if opErr, ok := rec.(*net.OpError); ok {
				if se, ok := opErr.Err.(*os.SyscallError); ok {
					if strings.Contains(strings.ToLower(se.Error()),
						"broken pipe") || strings.Contains(strings.ToLower(se.Error()),
						"connection reset by peer") {
						brokenPipe = true
					}
				}
			}

			l := Get(r)

			httpRequest, _ := httputil.DumpRequest(r, false)
			if brokenPipe {
				l.Error(r.Method+" "+r.URL.Path,
					zap.Any("error", rec),
					zap.String("request", string(httpRequest)),
				)
				return
			}

			l.Error(r.Method+" "+r.URL.Path+" Recovered from panic",
				zap.Any("error", rec),
				zap.String("request", string(httpRequest)),
				zap.String("stack", string(debug.Stack())),
			)

			w.WriteHeader(http.StatusInternalServerError)
		}()

		next.ServeHTTP(w, r)
	})
}
