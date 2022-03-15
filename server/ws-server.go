package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

var closeCodes map[int]string = map[int]string{
	1000: "NormalError",
	1001: "GoingAwayError",
	1002: "ProtocolError",
	1003: "UnknownType",
	1007: "TypeError",
	1008: "PolicyError",
	1009: "MessageTooLargeError",
	1010: "ExtensionError",
	1011: "UnexpectedError",
}

const bufferSize = 4096

var sockets []Websocket

type Websocket struct {
	conn   net.Conn
	bufrw  *bufio.ReadWriter
	header http.Header
	status uint16
	name   string
}

func main() {
	http.HandleFunc("/", IndexHandler)
	log.Fatal(http.ListenAndServe(*address, nil))
}

var address = flag.String("address", "localhost:8080", "http address")

func IndexHandler(w http.ResponseWriter, req *http.Request) {
	websocket, err := InitWebsocket(w, req)
	check(err)
	sockets = append(sockets, *websocket)

	err = websocket.Handshake()
	check(err)

	for {
		frame := Frame{}
		frame, err = websocket.Recv()
		if string(frame.Payload) == "EXIT" {
			err = websocket.Close()
			check(err)
		}
		SendToAll(frame, websocket)
	}
}

func SendToAll(frame Frame, sender *Websocket) {
	fmt.Println(sockets)
	for i := 0; i < len(sockets); i++ {
		if sockets[i].conn != sender.conn {
			err := sockets[i].Send(frame)
			check(err)
		}
	}
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

	return &Websocket{conn, bufrw, req.Header, 1000, "Socket " + strconv.Itoa(len(sockets)+1) + ": "}, nil
}

// Handshake Does initial handshake without closing.
func (ws *Websocket) Handshake() error {
	hash := getAcceptHash(ws.header.Get("Sec-WebSocket-Key"))
	lines := []string{
		"HTTP/1.1 101 Switching Protocols",
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

func (ws *Websocket) Recv() (Frame, error) {
	frame := Frame{}
	head, err := ws.read(2)
	if err != nil {
		return frame, err
	}

	frame.IsFragment = (head[0] & 0x80) == 0x00
	frame.Opcode = head[0] & 0x0F
	frame.Reserved = (head[0] & 0x70)

	frame.IsMasked = (head[1] & 0x80) == 0x80

	var length uint64
	length = uint64(head[1] & 0x7F)

	if length == 126 {
		data, err := ws.read(2)
		if err != nil {
			return frame, err
		}
		length = uint64(binary.BigEndian.Uint16(data))
	} else if length == 127 {
		data, err := ws.read(8)
		if err != nil {
			return frame, err
		}
		length = uint64(binary.BigEndian.Uint64(data))
	}
	mask, err := ws.read(4)
	if err != nil {
		return frame, err
	}
	frame.Length = length

	payload, err := ws.read(int(length)) // possible data loss
	if err != nil {
		return frame, err
	}

	for i := uint64(0); i < length; i++ {
		payload[i] ^= mask[i%4]
	}
	frame.Payload = payload
	err = ws.validate(&frame)
	return frame, err
}

func (ws *Websocket) read(size int) ([]byte, error) {
	data := make([]byte, 0)
	for {
		if len(data) == size {
			break
		}
		// Temporary slice to read chunk
		sz := bufferSize
		remaining := size - len(data)
		if sz > remaining {
			sz = remaining
		}
		temp := make([]byte, sz)

		n, err := ws.bufrw.Read(temp)
		if err != nil && err != io.EOF {
			return data, err
		}

		data = append(data, temp[:n]...)
	}
	return data, nil
}

func (ws *Websocket) validate(fr *Frame) error {
	if !fr.IsMasked {
		ws.status = 1002
		return errors.New("protocol error: unmasked client frame")
	}
	if fr.IsControl() && (fr.Length > 125 || fr.IsFragment) {
		ws.status = 1002
		return errors.New("protocol error: all control frames MUST have a payload length of 125 bytes or less and MUST NOT be fragmented")
	}
	if fr.HasReservedOpcode() {
		ws.status = 1002
		return errors.New("protocol error: opcode " + fmt.Sprintf("%x", fr.Opcode) + " is reserved")
	}
	if fr.Reserved > 0 {
		ws.status = 1002
		return errors.New("protocol error: RSV " + fmt.Sprintf("%x", fr.Reserved) + " is reserved")
	}
	if fr.Opcode == 1 && !fr.IsFragment && !utf8.Valid(fr.Payload) {
		ws.status = 1007
		return errors.New("wrong code: invalid UTF-8 text message ")
	}
	if fr.Opcode == 8 {
		if fr.Length >= 2 {
			code := binary.BigEndian.Uint16(fr.Payload[:2])
			reason := utf8.Valid(fr.Payload[2:])
			if code >= 5000 || (code < 3000 && closeCodes[int(code)] == "") {
				ws.status = 1002
				return errors.New(closeCodes[1002] + " Wrong Code")
			}
			if fr.Length > 2 && !reason {
				ws.status = 1007
				return errors.New(closeCodes[1007] + " invalid UTF-8 reason message")
			}
		} else if fr.Length != 0 {
			ws.status = 1002
			return errors.New(closeCodes[1002] + " Wrong Code")
		}
	}
	return nil
}

func (ws *Websocket) Send(fr Frame) error {
	data := make([]byte, 2)
	data[0] = 0x80 | fr.Opcode
	if fr.IsFragment {
		data[0] &= 0x7F
	}

	byteName := []byte(ws.name)
	fr.Payload = append(byteName, fr.Payload...)
	fr.Length += uint64(len(byteName))

	if fr.Length <= 125 {
		data[1] = byte(fr.Length)
		data = append(data, fr.Payload...)
	} else if fr.Length > 125 && float64(fr.Length) < math.Pow(2, 16) {
		data[1] = byte(126)
		size := make([]byte, 2)
		binary.BigEndian.PutUint16(size, uint16(fr.Length))
		data = append(data, size...)
		data = append(data, fr.Payload...)
	} else if float64(fr.Length) >= math.Pow(2, 16) {
		data[1] = byte(127)
		size := make([]byte, 8)
		binary.BigEndian.PutUint64(size, fr.Length)
		data = append(data, size...)
		data = append(data, fr.Payload...)
	}
	return ws.write(data)
}

func (ws *Websocket) write(data []byte) error {
	if _, err := ws.bufrw.Write(data); err != nil {
		return err
	}
	return ws.bufrw.Flush()
}

// Close closes tcp connection and ends handshake
func (ws *Websocket) Close() error {

	// Fill this with closing code.
	return ws.conn.Close()
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
