package main

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// OnHijacked callback that will be called every time a request has been
// hijacked
type OnHijacked func(r *http.Request, in, out io.Reader)

// TeeConn will forward any reads or writes to a pair of io.Writer
func TeeConn(conn net.Conn, in, out io.Writer) net.Conn {
	return &teeConn{
		Conn:   conn,
		reader: io.TeeReader(conn, in),
		writer: io.MultiWriter(conn, out),
	}
}

type teeConn struct {
	net.Conn
	reader io.Reader
	writer io.Writer
}

func (c *teeConn) Read(p []byte) (n int, err error) {
	return c.reader.Read(p)
}

func (c *teeConn) Write(p []byte) (n int, err error) {
	return c.writer.Write(p)
}

// CallbackHijacker is a wrapper around an http.ResponseWriter that will invoke
// our OnHijacked callback whenever a Hijack() is succesfully done
func CallbackHijacker(w http.ResponseWriter, r *http.Request,
	cb OnHijacked) http.ResponseWriter {
	if h, ok := w.(http.Hijacker); ok {
		w = &callbackHijacker{
			ResponseWriter: w,
			hijacker:       h,
			request:        r,
			callback:       cb,
		}
	}
	return w
}

type callbackHijacker struct {
	http.ResponseWriter
	hijacker http.Hijacker
	request  *http.Request
	callback OnHijacked
}

func (h *callbackHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buf, err := h.hijacker.Hijack()
	if err != nil {
		return conn, buf, err
	}
	// use `io.Pipe` to connect code expecting an `io.Reader` with code
	// expecting an `io.Writer`
	rIn, wIn := io.Pipe()
	rOut, wOut := io.Pipe()

	// invoke callback
	h.callback(h.request, rIn, rOut)

	// return wrapped conn
	return TeeConn(conn, wIn, wOut), buf, nil
}

// Sniffer is a wrapper around http.Handler that will invoke CallbackHijacker
// every time ServeHTTP() is called.
func Sniffer(h http.Handler, callback OnHijacked) http.Handler {
	return &sniffer{
		handler:  h,
		callback: callback,
	}
}

type sniffer struct {
	handler  http.Handler
	callback OnHijacked
}

func (s *sniffer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = CallbackHijacker(w, r, s.callback)
	s.handler.ServeHTTP(w, r)
}

func main() {
	u, err := url.Parse("http://echo.websocket.org")
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = u.Scheme
		r.URL.Host = u.Host
		r.Host = u.Host
	}
	handler := Sniffer(proxy, func(r *http.Request, in, out io.Reader) {
		go readLoop(r, in, "<")
		go readLoop(r, out, ">")
	})
	log.Println("listening on :8080")
	http.ListenAndServe("localhost:8080", handler)
}

func readLoop(req *http.Request, r io.Reader, dir string) {
	data := make([]byte, 1024)
	for {
		n, err := r.Read(data)
		if err != nil {
			log.Println(err)
			return
		}
		log.Printf("%s %s: %x\n", dir, req.RemoteAddr, data[:n])
	}
}
