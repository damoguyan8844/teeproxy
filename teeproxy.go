package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"code.google.com/p/go-uuid/uuid"
)

var (
	listen           = flag.String("l", ":8888", "port to accept requests")
	targetProduction = flag.String("a", "http://localhost:8080", "where production traffic goes. http://localhost:8080/production")
	altTarget        = flag.String("b", "http://localhost:8081", "where testing traffic goes. response are skipped. http://localhost:8081/test")

	// Hop-by-hop headers. These are removed when sent to the backend.
	// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
	hopHeaders = []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te", // canonicalized version of "TE"
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}
)

type Hosts struct {
	Target      url.URL
	Alternative url.URL
}

var hosts Hosts
var client *http.Client
var proxy *httputil.ReverseProxy

type TimeoutTransport struct {
	http.Transport
}

func (t *TimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.Transport.RoundTrip(req)
}

func clientCall(id string, req, req2 *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			logMessage(id, "ERROR", fmt.Sprintf("Recovered in clientCall: <%v> <%s>", r, removeEndsOfLines(string(debug.Stack()))))
		}
	}()

	resp, err := http.DefaultTransport.RoundTrip(req2)
	if err != nil {
		logMessage(id, "ERROR", fmt.Sprintf("Invoking client failed: <%v>. Request: <%s>.", err, prettyPrint(req2)))
		return
	}

	r, e := httputil.DumpResponse(resp, true)
	if e != nil {
		logMessage(id, "ERROR", fmt.Sprintf("Could not create response dump: <%v>", e))
	} else {
		logMessage(id, "INFO", fmt.Sprintf("Response: <%s>", removeEndsOfLines(string(r))))
	}

	io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()
}

func teeDirector(req *http.Request) {
	id := uuid.NewUUID().String()
	logMessage(id, "INFO", fmt.Sprintf("Request: <%s>", prettyPrint(req)))

	go clientCall(id, req, duplicateRequest(req))

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

func duplicateRequest(request *http.Request) (request1 *http.Request) {
	b1 := new(bytes.Buffer)
	b2 := new(bytes.Buffer)
	w := io.MultiWriter(b1, b2)
	io.Copy(w, request.Body)
	request.Body = ioutil.NopCloser(bytes.NewReader(b2.Bytes()))
	bodyReader := ioutil.NopCloser(bytes.NewReader(b1.Bytes()))

	request2 := &http.Request{
		Method: request.Method,
		URL: &url.URL{
			Scheme:   hosts.Alternative.Scheme,
			Host:     hosts.Alternative.Host,
			Path:     singleJoiningSlash(hosts.Alternative.Path, request.URL.Path),
			RawQuery: request.URL.RawQuery,
		},
		Proto:         request.Proto,
		ProtoMajor:    request.ProtoMajor,
		ProtoMinor:    request.ProtoMinor,
		Header:        request.Header,
		Body:          bodyReader,
		ContentLength: request.ContentLength,
		Close:         false,
	}

	// Remove hop-by-hop headers to the backend.  Especially
	// important is "Connection" because we want a persistent
	// connection, regardless of what the client sent to us.  This
	// is modifying the same underlying map from req (shallow
	// copied above) so we only copy it if necessary.
	copiedHeaders := false
	for _, h := range hopHeaders {
		if request2.Header.Get(h) != "" {
			if !copiedHeaders {
				request2.Header = make(http.Header)
				copyHeader(request2.Header, request.Header)
				copiedHeaders = true
			}
			request2.Header.Del(h)
		}
	}

	return request2
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	proxy.ServeHTTP(w, r)
}

// want to keep log messages on a single line, one line is one log entry
func removeEndsOfLines(s string) string {
	return strings.Replace(strings.Replace(s, "\n", "\\n", -1), "\r", "\\r", -1)
}

func prettyPrint(obj interface{}) string {
	return removeEndsOfLines(fmt.Sprintf("%+v", obj))
}

func logMessage(id, messageType, message string) {
	fmt.Printf("[%s][%s][%s][%s]\n", time.Now().Format(time.RFC3339Nano), id, messageType, message)
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

func main() {
	flag.Parse()

	target, _ := url.Parse(*targetProduction)
	alt, _ := url.Parse(*altTarget)
	client = &http.Client{
		Timeout: time.Millisecond * 2000,
	}

	hosts = Hosts{
		Target:      *target,
		Alternative: *alt,
	}

	u, _ := url.Parse(*targetProduction)
	proxy = httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &TimeoutTransport{}
	proxy.Director = teeDirector

	http.HandleFunc("/", handler)
	http.ListenAndServe(*listen, nil)
}
