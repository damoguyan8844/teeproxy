package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

var (
	listen           = flag.String("l", ":8888", "port to accept requests")
	targetProduction = flag.String("a", "localhost:8080", "where production traffic goes. http://localhost:8080/production")
	altTarget        = flag.String("b", "localhost:8081", "where testing traffic goes. response are skipped. http://localhost:8081/test")
	debug            = flag.Bool("debug", false, "more logging, showing ignored output")
)

type Hosts struct {
	Target      url.URL
	Alternative url.URL
}

type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error { return nil }

type myTransport struct {
}

var hosts Hosts

func (t *myTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)

	if response != nil {
		r, err := httputil.DumpResponse(response, true)
		if err != nil {
			// copying the response body did not work
			return nil, err
		}

		fmt.Printf("[<A Response>][<%v>]\n", string(r))
	}

	return response, err
}

func teeDirector(req *http.Request) {
	fmt.Printf("[<Request>][<%v>]\n", req)
	req2 := duplicateRequest(req)

	go func() {
		defer func() {
			if r := recover(); r != nil && *debug {
				fmt.Println("Recovered in f", r)
			}
		}()
		client_tcp_conn, _ := net.DialTimeout("tcp", hosts.Alternative.Host, time.Duration(1*time.Second))
		client_http_conn := httputil.NewClientConn(client_tcp_conn, nil)
		client_http_conn.Write(req2)
		resp, _ := client_http_conn.Read(req2)
		r, _ := httputil.DumpResponse(resp, true)
		fmt.Printf("[<B Response>][<%v>]\n", string(r))

		client_http_conn.Close()
	}()

	targetQuery := hosts.Target.RawQuery
	req.URL.Scheme = hosts.Target.Scheme
	req.URL.Host = hosts.Target.Host
	req.URL.Path = singleJoiningSlash(hosts.Target.Path, req.URL.Path)
	if targetQuery == "" || req.URL.RawQuery == "" {
		req.URL.RawQuery = targetQuery + req.URL.RawQuery
	} else {
		req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
	}
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

func duplicateRequest(request *http.Request) (request1 *http.Request) {
	b1 := new(bytes.Buffer)
	io.Copy(b1, request.Body)
	defer request.Body.Close()
	request1 = &http.Request{
		Method:        request.Method,
		URL:           request.URL,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        request.Header,
		Body:          nopCloser{b1},
		Host:          request.Host,
		ContentLength: request.ContentLength,
	}
	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	u, _ := url.Parse(*targetProduction)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &myTransport{}
	proxy.Director = teeDirector

	proxy.ServeHTTP(w, r)
}

func main() {
	flag.Parse()

	target, _ := url.Parse(*targetProduction)
	alt, _ := url.Parse(*altTarget)

	hosts = Hosts{
		Target:      *target,
		Alternative: *alt,
	}

	http.HandleFunc("/", handler)
	http.ListenAndServe(*listen, nil)
}
