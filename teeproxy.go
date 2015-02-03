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

var hosts Hosts

type TimeoutTransport struct {
	http.Transport
	RoundTripTimeout time.Duration
}

type respAndErr struct {
	resp *http.Response
	err  error
}

type netTimeoutError struct {
	error
}

func (ne netTimeoutError) Timeout() bool { return true }

// If you don't set RoundTrip on TimeoutTransport, this will always timeout at 0
func (t *TimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	timeout := time.After(t.RoundTripTimeout)
	resp := make(chan respAndErr, 1)

	go func() {
		r, e := t.Transport.RoundTrip(req)

		resp <- respAndErr{
			resp: r,
			err:  e,
		}
	}()

	select {
	case <-timeout: // A round trip timeout has occurred.
		t.Transport.CancelRequest(req)
		return nil, netTimeoutError{
			error: fmt.Errorf("timed out after %s", t.RoundTripTimeout),
		}
	case r := <-resp: // Success!
		return r.resp, r.err
	}
}

func teeDirector(req *http.Request) {
	fmt.Printf("[<Request Protocol>][<%v>]\n", req.Proto)
	fmt.Printf("[<Request>][<%v>]\n", req)
	req2 := duplicateRequest(req)

	go func() {
		defer func() {
			if r := recover(); r != nil && *debug {
				fmt.Println("Recovered in f", r)
			}
		}()

		client := &http.Client{}
		client.Timeout = time.Millisecond * 2000
		resp, err := client.Do(req2)

		if err != nil {
			fmt.Printf("[<B Error>][<%v>]\n", err)
		} else {
			r, e := httputil.DumpResponse(resp, true)
			if e != nil {
				fmt.Printf("[<B Error Dump>][<%v>]", e)
			} else {
				fmt.Printf("[<B Resp>][<%v>]", string(r))
			}
		}

		resp.Body.Close()
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
	b2 := new(bytes.Buffer)
	w := io.MultiWriter(b1, b2)
	io.Copy(w, request.Body)
	request.Body = ioutil.NopCloser(bytes.NewReader(b2.Bytes()))
	request2 := &http.Request{
		Method: request.Method,
		URL: &url.URL{
			Scheme: hosts.Alternative.Scheme,
			Host:   hosts.Alternative.Host,
			Path:   singleJoiningSlash(hosts.Alternative.Path, request.URL.Path),
		},
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        request.Header,
		Body:          ioutil.NopCloser(bytes.NewReader(b1.Bytes())),
		ContentLength: request.ContentLength,
	}

	return request2
}

func handler(w http.ResponseWriter, r *http.Request) {
	u, _ := url.Parse(*targetProduction)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &TimeoutTransport{
		RoundTripTimeout: time.Second * 60,
	}
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
