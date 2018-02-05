package main

// standard libs
import (
	"net/http"
	"net/url"
)

func init() {
	// create a reverse proxy to our couch server
	proxyURL, _ := url.Parse("https://sites.google.com/")
	proxy := NewSingleHostReverseProxy(proxyURL)
	http.Handle("/", proxy)
}
