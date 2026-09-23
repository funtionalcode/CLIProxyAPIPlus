package proxyutil

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"testing"
)

func TestHTTPConnectDialerForwardsGatewayTrace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			done <- errAccept
			return
		}
		defer conn.Close()
		req, errRead := http.ReadRequest(bufio.NewReader(conn))
		if errRead != nil {
			done <- errRead
			return
		}
		if got := req.Header.Get(GatewayTraceHeader); got != "trace-123" {
			done <- fmt.Errorf("trace header = %q", got)
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		done <- nil
	}()

	dialer, _, err := BuildDialer("http://" + listener.Addr().String() + "?" + GatewayTraceQueryParameter + "=trace-123")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = dialer.Dial("tcp", "chatgpt.com:443")
	if errServer := <-done; errServer != nil {
		t.Fatal(errServer)
	}
}
