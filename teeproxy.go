package main

import (
	"bytes"
	"encoding/json"
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
			fmt.Println("Recovered in clientCall", r, debug.Stack())
		}
	}()

	resp, err := client.Do(req2)

	if err != nil {
		logMessage("[%v][%v][<B Error>][<%v>]\n", id, err)
	} else {
		r, e := httputil.DumpResponse(resp, true)

		if e != nil {
			logMessage("[%v][%v][<B Error Dump>][<%v>]\n", id, e)
		} else {
			logMessage("[%v][%v][<B Resp>][<%v>]\n", id, string(r))
		}

		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}
}

func teeDirector(req *http.Request) {
	id := uuid.NewUUID().String()
	fmt.Printf("[%v][%v][<Request>][<%+v>]\n", time.Now().Format(time.RFC3339Nano), id, req)

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

func logMessage(message string, id string, logObj interface{}) {
	fmt.Printf(message, time.Now().Format(time.RFC3339Nano), id, logObj)
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

	return request2
}

func handler(w http.ResponseWriter, r *http.Request) {
	dump, e := httputil.DumpRequest(r, true)
	if e != nil {
		logMessage("[%v][%v][<In Error Dump>][<%v>]\n", "", e)
	} else {
		logMessage("[%v][%v][<In Req>][<%v>]\n", "", string(dump))
	}
	proxy.ServeHTTP(w, r)
}

func prettyPrintRequest(req *http.Request) string {
	result, _ := json.MarshalIndent(req, "", "\t")
	return string(result)
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
