// Package libp2pkcp provides a KCP-based transport for libp2p.
//
// 注意：KCP传输层不再直接实现stream，而是使用yamux进行流的多路复用。
// 这个文件保留为历史记录。
package libp2pkcp

// KCP传输层现在通过yamux提供多路复用功能。
// 不再需要直接在KCP会话上实现stream。

import (
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	kcp "github.com/xtaci/kcp-go"
)

type stream struct {
	conn *kcp.UDPSession
}

var _ network.MuxedStream = &stream{}

// newStream creates a new stream based on a KCP connection.
func newStream(conn *kcp.UDPSession) *stream {
	return &stream{
		conn: conn,
	}
}

func (s *stream) Read(b []byte) (n int, err error) {
	return s.conn.Read(b)
}

func (s *stream) Write(b []byte) (n int, err error) {
	return s.conn.Write(b)
}

func (s *stream) CloseWrite() error {
	// KCP不支持单方向关闭，我们这里只是设置一个超小的写入超时
	return s.conn.SetWriteDeadline(time.Now())
}

func (s *stream) CloseRead() error {
	// KCP不支持单方向关闭，我们这里只是设置一个超小的读取超时
	return s.conn.SetReadDeadline(time.Now())
}

func (s *stream) Close() error {
	// 流关闭不关闭底层连接，因为连接是复用的
	// 但是可以设置这个流的截止日期为现在，使其不可读写
	err1 := s.conn.SetReadDeadline(time.Now())
	err2 := s.conn.SetWriteDeadline(time.Now())
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *stream) Reset() error {
	// 重置操作相当于关闭操作
	return s.Close()
}

func (s *stream) ResetWithError(code network.StreamErrorCode) error {
	return s.Reset()
}

func (s *stream) SetDeadline(t time.Time) error {
	return s.conn.SetDeadline(t)
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return s.conn.SetWriteDeadline(t)
}
