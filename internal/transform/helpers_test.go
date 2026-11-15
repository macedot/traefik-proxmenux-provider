package transform

import (
	"net"
)

func listenLocal() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

type tcpAddr = net.TCPAddr

func itoa(n int) string {
	b := [20]byte{}
	i := len(b)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
