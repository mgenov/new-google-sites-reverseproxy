// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// HTTP reverse proxy handler

package main

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"
)

const footer = `<footer class="BCtCv" jscontroller="BYGbod" jsaction="rcuQ6b:WYd;" data-create-site-url="/new/?usp=viewer_footer"><div class="uByue"><div class="yYF4Mc"><div><div class="qmpcfe">Made with the new Google&nbsp;Sites<span class="uabEyd">, an effortless way to create beautiful&nbsp;sites.</span></div><div class="qIzFn"><div class="tT6kpe"><button class="f8sasc" jsaction="click:ksR11e;">Create a site</button></div><div class="CvWMXb" role="button" tabindex="0" jscontroller="Nd7Bx" jsaction="rcuQ6b:npT2md;click:GDd5Hf"><span class="DLHR6">Report abuse</span></div></div></div></div></div></footer>`

// app engine libs

// onExitFlushLoop is a callback set by tests to detect the state of the
// flushLoop() goroutine.
var onExitFlushLoop func()

// ReverseProxy is an HTTP Handler that takes an incoming request and
// sends it to another server, proxying the response back to the
// client.
type ReverseProxy struct {
	// Director must be a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	Director func(*http.Request)

	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// FlushInterval specifies the flush interval
	// to flush to the client while copying the
	// response body.
	// If zero, no periodic flushing is done.
	FlushInterval time.Duration
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// NewSingleHostReverseProxy returns a new ReverseProxy that rewrites
// URLs to the scheme, host, and base path provided in target. If the
// target's path is "/base" and the incoming request was for "/dir",
// the target request will be for /base/dir.
func NewSingleHostReverseProxy(target *url.URL) *ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		if !strings.Contains(req.URL.Path, "_/") && !strings.Contains(req.URL.Path, "/fiber.bg/www") {
			req.URL.Path = singleJoiningSlash(target.Path, "/fiber.bg/www/"+req.URL.Path)
		} else {
			req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		}
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
	}
	return &ReverseProxy{Director: director}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ctx := appengine.NewContext(req)
	// The primary difference with App Engine is that we must use the urlfetch library to obtain the Transport
	transport := &urlfetch.Transport{
		Context: ctx,
	}

	outreq := new(http.Request)
	*outreq = *req // includes shallow copies of maps, but okay

	p.Director(outreq)
	outreq.Proto = "HTTP/1.1"
	outreq.ProtoMajor = 1
	outreq.ProtoMinor = 1
	outreq.Close = false

	// Remove hop-by-hop headers to the backend.  Especially
	// important is "Connection" because we want a persistent
	// connection, regardless of what the client sent to us.  This
	// is modifying the same underlying map from req (shallow
	// copied above) so we only copy it if necessary.
	copiedHeaders := false
	for _, h := range hopHeaders {
		if outreq.Header.Get(h) != "" {
			if !copiedHeaders {
				outreq.Header = make(http.Header)
				copyHeader(outreq.Header, req.Header)
				copiedHeaders = true
			}
			outreq.Header.Del(h)
		}
	}

	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		// If we aren't the first proxy retain prior
		// X-Forwarded-For information as a comma+space
		// separated list and fold multiple headers into one.
		if prior, ok := outreq.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		outreq.Header.Set("X-Forwarded-For", clientIP)
	}

	res, err := transport.RoundTrip(outreq)
	if err != nil {
		p.logf(ctx, "http: proxy error: %v", err)
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()
	b, err := ioutil.ReadAll(res.Body)
	result := strings.Replace(string(b), "/fiber.bg/www", "", -1)
	result = strings.Replace(result, "https://sites.google.com", req.Header.Get("Origin"), -1)
	result = strings.Replace(result, footer, "", -1)

	if strings.Contains(req.URL.Path, "_/") {
		log.Debugf(ctx, "Host: %s, Content: %s", req.URL.Host, result)
		log.Debugf(ctx, "Headers: %v", req.Header)
	}

	for _, h := range hopHeaders {
		res.Header.Del(h)
	}

	copyHeader(rw.Header(), res.Header)

	rw.WriteHeader(res.StatusCode)
	p.copyResponse(rw, bytes.NewBufferString(result))
}

func (p *ReverseProxy) copyResponse(dst io.Writer, src io.Reader) {
	if p.FlushInterval != 0 {
		if wf, ok := dst.(writeFlusher); ok {
			mlw := &maxLatencyWriter{
				dst:     wf,
				latency: p.FlushInterval,
				done:    make(chan bool),
			}
			go mlw.flushLoop()
			defer mlw.stop()
			dst = mlw
		}
	}

	io.Copy(dst, src)
}

func (p *ReverseProxy) logf(ctx context.Context, format string, args ...interface{}) {
	// if p.ErrorLog != nil {
	// p.ErrorLog.Printf(format, args...)
	// } else {
	log.Debugf(ctx, format, args...)
	// }
}

type writeFlusher interface {
	io.Writer
	http.Flusher
}

type maxLatencyWriter struct {
	dst     writeFlusher
	latency time.Duration

	lk   sync.Mutex // protects Write + Flush
	done chan bool
}

func (m *maxLatencyWriter) Write(p []byte) (int, error) {
	m.lk.Lock()
	defer m.lk.Unlock()
	return m.dst.Write(p)
}

func (m *maxLatencyWriter) flushLoop() {
	t := time.NewTicker(m.latency)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			if onExitFlushLoop != nil {
				onExitFlushLoop()
			}
			return
		case <-t.C:
			m.lk.Lock()
			m.dst.Flush()
			m.lk.Unlock()
		}
	}
}

func (m *maxLatencyWriter) stop() { m.done <- true }
