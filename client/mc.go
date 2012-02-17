// A memcached binary protocol client.
package memcached

import (
	"bufio"
	"encoding/binary"
	"github.com/dustin/gomemcached"
	"io"
	"log"
	"net"
	"runtime"
)

const bufsize = 1024

// The Client itself.
type Client struct {
	conn   net.Conn
	writer *bufio.Writer

	hdrBuf []byte
}

// Connect to a memcached server.
func Connect(prot, dest string) (rv *Client, err error) {
	conn, err := net.Dial(prot, dest)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:   conn,
		writer: bufio.NewWriterSize(conn, bufsize),
		hdrBuf: make([]byte, gomemcached.HDR_LEN),
	}, nil
}

// Close the connection when you're done.
func (c *Client) Close() {
	c.conn.Close()
}

// Send a custom request and get the response.
func (client *Client) Send(req *gomemcached.MCRequest) (rv gomemcached.MCResponse) {
	transmitRequest(client.writer, req)
	rv = client.getResponse()
	return
}

// Send a request, but do not wait for a response.
func (client *Client) Transmit(req *gomemcached.MCRequest) {
	transmitRequest(client.writer, req)
}

// Receive a response
func (client *Client) Receive() gomemcached.MCResponse {
	return client.getResponse()
}

// Get the value for a key.
func (client *Client) Get(vb uint16, key string) gomemcached.MCResponse {
	var req gomemcached.MCRequest
	req.Opcode = gomemcached.GET
	req.VBucket = vb
	req.Key = []byte(key)
	req.Cas = 0
	req.Opaque = 0
	req.Extras = []byte{}
	req.Body = []byte{}
	return client.Send(&req)
}

// Delete a key.
func (client *Client) Del(vb uint16, key string) gomemcached.MCResponse {
	var req gomemcached.MCRequest
	req.Opcode = gomemcached.DELETE
	req.VBucket = vb
	req.Key = []byte(key)
	req.Cas = 0
	req.Opaque = 0
	req.Extras = []byte{}
	req.Body = []byte{}
	return client.Send(&req)
}

func (client *Client) store(opcode gomemcached.CommandCode, vb uint16,
	key string, flags int, exp int, body []byte) gomemcached.MCResponse {

	var req gomemcached.MCRequest
	req.Opcode = opcode
	req.VBucket = vb
	req.Cas = 0
	req.Opaque = 0
	req.Key = []byte(key)
	req.Extras = []byte{0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(req.Extras, uint64(flags)<<32|uint64(exp))
	req.Body = body
	return client.Send(&req)
}

// Add a value for a key (store if not exists).
func (client *Client) Add(vb uint16, key string, flags int, exp int,
	body []byte) gomemcached.MCResponse {
	return client.store(gomemcached.ADD, vb, key, flags, exp, body)
}

// Set the value for a key.
func (client *Client) Set(vb uint16, key string, flags int, exp int,
	body []byte) gomemcached.MCResponse {
	return client.store(gomemcached.SET, vb, key, flags, exp, body)
}

// Stats returns a slice of these.
type StatValue struct {
	// The stat key
	Key string
	// The stat value
	Val string
}

// Get stats from the server
// use "" as the stat key for toplevel stats.
func (client *Client) Stats(key string) []StatValue {
	rv := []StatValue{}

	var req gomemcached.MCRequest
	req.Opcode = gomemcached.STAT
	req.VBucket = 0
	req.Cas = 0
	req.Opaque = 918494
	req.Key = []byte(key)
	req.Extras = []byte{}
	req.Body = []byte{}

	transmitRequest(client.writer, &req)

	for {
		res := client.getResponse()
		k := string(res.Key)
		if k == "" {
			break
		}
		rv = append(rv, StatValue{
			Key: k,
			Val: string(res.Body),
		})
	}

	return rv
}

func (client *Client) getResponse() gomemcached.MCResponse {
	bytesRead, err := io.ReadFull(client.conn, client.hdrBuf)
	if err != nil || bytesRead != gomemcached.HDR_LEN {
		log.Printf("Error reading message: %s (%d bytes)", err, bytesRead)
		runtime.Goexit()
	}
	res := grokHeader(client.hdrBuf)
	readContents(client.conn, res)
	return res
}

func readContents(s net.Conn, res gomemcached.MCResponse) {
	readOb(s, res.Extras)
	readOb(s, res.Key)
	readOb(s, res.Body)
}

func grokHeader(hdrBytes []byte) (rv gomemcached.MCResponse) {
	if hdrBytes[0] != gomemcached.RES_MAGIC {
		log.Printf("Bad magic: %x", hdrBytes[0])
		runtime.Goexit()
	}
	// rv.Opcode = hdrBytes[1]
	rv.Key = make([]byte, binary.BigEndian.Uint16(hdrBytes[2:]))
	rv.Extras = make([]byte, hdrBytes[4])
	rv.Status = uint16(hdrBytes[7])
	bodyLen := binary.BigEndian.Uint32(hdrBytes[8:]) - uint32(len(rv.Key)) - uint32(len(rv.Extras))
	rv.Body = make([]byte, bodyLen)
	// rv.Opaque = binary.BigEndian.Uint32(hdrBytes[12:])
	rv.Cas = binary.BigEndian.Uint64(hdrBytes[16:])
	return
}

func transmitRequest(o *bufio.Writer, req *gomemcached.MCRequest) {
	// 0
	writeByte(o, gomemcached.REQ_MAGIC)
	writeByte(o, byte(req.Opcode))
	writeUint16(o, uint16(len(req.Key)))
	// 4
	writeByte(o, uint8(len(req.Extras)))
	writeByte(o, 0)
	writeUint16(o, req.VBucket)
	// 8
	writeUint32(o, uint32(len(req.Body))+
		uint32(len(req.Key))+
		uint32(len(req.Extras)))
	// 12
	writeUint32(o, req.Opaque)
	// 16
	writeUint64(o, req.Cas)
	// The rest
	writeBytes(o, req.Extras)
	writeBytes(o, req.Key)
	writeBytes(o, req.Body)
	o.Flush()
}

func writeBytes(s *bufio.Writer, data []byte) {
	if len(data) > 0 {
		written, err := s.Write(data)
		if err != nil || written != len(data) {
			log.Printf("Error writing bytes:  %s", err)
			runtime.Goexit()
		}
	}
	return

}

func writeByte(s *bufio.Writer, b byte) {
	data := make([]byte, 1)
	data[0] = b
	writeBytes(s, data)
}

func writeUint16(s *bufio.Writer, n uint16) {
	data := []byte{0, 0}
	binary.BigEndian.PutUint16(data, n)
	writeBytes(s, data)
}

func writeUint32(s *bufio.Writer, n uint32) {
	data := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint32(data, n)
	writeBytes(s, data)
}

func writeUint64(s *bufio.Writer, n uint64) {
	data := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(data, n)
	writeBytes(s, data)
}

func readOb(s net.Conn, buf []byte) {
	x, err := io.ReadFull(s, buf)
	if err != nil || x != len(buf) {
		log.Printf("Error reading part: %s", err)
		runtime.Goexit()
	}
}
