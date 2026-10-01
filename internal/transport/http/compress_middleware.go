package http

import (
	"compress/gzip"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")

		out := w
		if r.Method != http.MethodHead && acceptsGzip(r.Header.Values("Accept-Encoding")) {
			gw := &gzipResponseWriter{ResponseWriter: w}
			defer gw.finish()
			out = gw
		}

		encoding := singleContentEncoding(r.Header.Values("Content-Encoding"))
		switch {
		case encoding == "" || strings.EqualFold(encoding, "identity"):
		case strings.EqualFold(encoding, "gzip"):
			r.Body = &lazyGzipReader{src: r.Body}
			r.Header.Del("Content-Encoding")
			r.Header.Del("Content-Length")
			r.ContentLength = -1
		default:
			http.Error(out, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
			return
		}

		next.ServeHTTP(out, r)
	})
}

func singleContentEncoding(values []string) string {
	var joined []string
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			joined = append(joined, trimmed)
		}
	}
	return strings.Join(joined, ", ")
}

type lazyGzipReader struct {
	src io.ReadCloser
	gz  *gzip.Reader
	err error
}

func (l *lazyGzipReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if l.gz == nil {
		gz, err := gzip.NewReader(l.src)
		if err != nil {
			l.err = err
			return 0, err
		}
		l.gz = gz
	}
	return l.gz.Read(p)
}

func (l *lazyGzipReader) Close() error {
	var gzErr error
	if l.gz != nil {
		gzErr = l.gz.Close()
	}
	if err := l.src.Close(); err != nil {
		return err
	}
	return gzErr
}

type gzipResponseWriter struct {
	http.ResponseWriter
	code int
	sent bool
	gz   *gzip.Writer
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.sent || g.code != 0 {
		return
	}
	if code >= 100 && code < 200 {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.code = code
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.sent {
		if len(p) == 0 {
			return 0, nil
		}
		g.start()
	}
	if g.gz != nil {
		return g.gz.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipResponseWriter) start() {
	if g.code == 0 {
		g.code = http.StatusOK
	}
	header := g.ResponseWriter.Header()
	if g.code != http.StatusNoContent && g.code != http.StatusNotModified && header.Get("Content-Encoding") == "" && compressibleType(header.Get("Content-Type")) {
		header.Set("Content-Encoding", "gzip")
		header.Del("Content-Length")
		g.ResponseWriter.WriteHeader(g.code)
		g.gz = gzip.NewWriter(g.ResponseWriter)
	} else {
		g.ResponseWriter.WriteHeader(g.code)
	}
	g.sent = true
}

func (g *gzipResponseWriter) finish() {
	if !g.sent && g.code != 0 {
		g.ResponseWriter.WriteHeader(g.code)
		g.sent = true
	}
	if g.gz != nil {
		_ = g.gz.Close()
	}
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

func compressibleType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasPrefix(mediaType, "text/")
}

func acceptsGzip(values []string) bool {
	gzipQ, starQ := -1.0, -1.0
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			coding, params, _ := strings.Cut(item, ";")
			coding = strings.TrimSpace(coding)
			q := parseQuality(params)
			switch {
			case strings.EqualFold(coding, "gzip"):
				gzipQ = q
			case coding == "*":
				starQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

func parseQuality(params string) float64 {
	for _, param := range strings.Split(params, ";") {
		name, value, found := strings.Cut(param, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(q) || q < 0 {
			return 0
		}
		return q
	}
	return 1
}
