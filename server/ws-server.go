package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"strings"
)

type Websocket struct {
	conn   net.Conn
	bufrw  *bufio.ReadWriter
	header http.Header
	status uint16
}

func main() {
	http.HandleFunc("/", IndexHandler)
}

func IndexHandler(w http.ResponseWriter, req *http.Request) {
	websocket, err := InitWebsocket(w, req)
	check(err)

	// Sending message
	err = websocket.write("Heisann!")
	check(err)

	// Clossing connection
	err = websocket.Close()
	check(err)
}

// InitWebsocket inititates an open websocket connection.
func InitWebsocket(w http.ResponseWriter, req *http.Request) (*Websocket, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "webserver doesn't support hijacking", http.StatusInternalServerError)
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	return &Websocket{conn, bufrw, req.Header, 1000}, nil
}

// Handshake Does initial handshake without closing.
func (ws *Websocket) Handshake() error {
	hash := getAcceptHash(ws.header.Get("Sec-WebSocket-Key"))
	lines := []string{
		"HTTP/1.1 101 Web Socket Protocol Handshake",
		"Server: go/echoserver",
		"Upgrade: WebSocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: " + hash,
		"", //  Two spaces to signalize that header is finished.
		"",
	}

	// Send Header
	if _, err := ws.bufrw.Write([]byte(strings.Join(lines, "\r\n"))); err != nil {
		return err
	}
	return ws.bufrw.Flush()
}

// Close closes tcp connection and ends handshake
func (ws *Websocket) Close() error {

	// Fill this with closing code.
	return ws.conn.Close()
}

func (ws *Websocket) write(message string) error {
	_, err := ws.bufrw.WriteString(message)
	check(err)
	return ws.bufrw.Flush()
}

// Check documentation on this
func getAcceptHash(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func check(err error) {
	if err != nil {
		log.Fatalln(err)
	}
}
