package main

// The store forwarder (platformd store-forward): with the platform's files on
// a company's S3 store (storage.external), the Service buckets:9000 forwards
// every request to the store's endpoint (STORE_ENDPOINT) as it came, its Host
// included, so the signatures the platform's services made for buckets:9000
// (and for s3.<domain>, through the gateway) hold at the store. It trusts the
// store's certificate as the platform does (SSL_CERT_FILE: global.ca).
//
// One request it adds to: a multi-object delete (POST /<bucket>?delete,
// S3's DeleteObjects) without a Content-MD5. S3 asks such a request to carry
// a checksum; AWS's SDKs since early 2025 (Java 2.30, botocore 1.36) send a
// CRC32 one (x-amz-checksum-crc32) in place of Content-MD5, and a MinIO from
// before 2025 refuses them: "Missing required header for this request:
// Content-Md5" (Nessie's GC deleted nothing on such a store). The forwarder
// adds the body's MD5. Content-MD5 isn't among the headers the request's
// signature covers (the client sent none), so the signature still holds;
// a body sent in aws-chunked encoding, whose bytes aren't the payload's, is
// left as it is.

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// deleteBodyMost is the largest multi-object delete body the forwarder reads
// to sum: S3 takes at most 1,000 keys, each at most 1,024 bytes, in a request.
const deleteBodyMost = 8 << 20

func storeForward() int {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	endpoint := os.Getenv("STORE_ENDPOINT")
	h, err := newStoreForwarder(endpoint, log)
	if err != nil {
		log.Error("store-forward", "error", err)
		return 2
	}
	listen := os.Getenv("STORE_FORWARD_LISTEN")
	if listen == "" {
		listen = ":9000"
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hs := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()
	log.Info("store-forward", "listen", listen, "store", endpoint)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("store-forward", "error", err)
		return 1
	}
	return 0
}

// newStoreForwarder is the forwarder to the store at endpoint (http(s)://host[:port]).
func newStoreForwarder(endpoint string, log *slog.Logger) (http.Handler, error) {
	store, err := url.Parse(endpoint)
	if err != nil || (store.Scheme != "http" && store.Scheme != "https") || store.Host == "" {
		return nil, fmt.Errorf("STORE_ENDPOINT %q: want http(s)://host[:port]", endpoint)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 64
	if store.Scheme == "https" {
		// The store's own name, for its certificate: the request's Host is the platform's.
		transport.TLSClientConfig = &tls.Config{ServerName: store.Hostname(), MinVersion: tls.VersionTLS12}
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = store.Scheme, store.Host
			pr.Out.Host = pr.In.Host // the Host the signature covers
			// As it came: no X-Forwarded-* added (Rewrite adds none unless asked).
		},
		Transport:     transport,
		FlushInterval: -1, // stream: objects, and S3's listings as they come
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("store-forward", "method", r.Method, "path", r.URL.Path, "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := withDeleteMD5(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

// withDeleteMD5 gives a multi-object delete without a Content-MD5 its body's
// (see the file's comment); every other request is left as it is.
func withDeleteMD5(r *http.Request) error {
	if r.Method != http.MethodPost || !hasQueryKey(r.URL.RawQuery, "delete") || r.Header.Get("Content-MD5") != "" {
		return nil
	}
	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
		strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, deleteBodyMost+1))
	r.Body.Close()
	if err != nil {
		return fmt.Errorf("reading the delete's body: %v", err)
	}
	if len(body) > deleteBodyMost {
		return fmt.Errorf("a multi-object delete's body over %d bytes", deleteBodyMost)
	}
	sum := md5.Sum(body)
	r.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	return nil
}

// hasQueryKey says whether a raw query names key (?delete, ?delete=, &delete).
func hasQueryKey(raw, key string) bool {
	for _, kv := range strings.Split(raw, "&") {
		if k, _, _ := strings.Cut(kv, "="); k == key {
			return true
		}
	}
	return false
}
