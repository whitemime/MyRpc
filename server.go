package myrpc

import (
	"MyRpc/codec"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
)

const MagicNumber = 0x3bef5c

// 协商信息，让双方知道怎么读写数据
type Option struct {
	MagicNumber int
	CodecType   codec.Type //head和body所用的编解码格式
}

var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

type Server struct{}

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

// net.Listener表示一个网络监听器，用于接收网络连接
func (server *Server) Accept(lis net.Listener) {
	for {
		//等待并且接收第一个网络连接
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		//开启子协程，服务端处理接收到的连接，收到一个就开启一个协程
		go server.ServerConn(conn)
	}
}

// 暴露到外部
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// 处理客户端发来的连接 这里主要为读取这个连接的编解码接口
func (server *Server) ServerConn(conn io.ReadWriteCloser) {
	defer func() {
		_ = conn.Close()
	}()
	//先用json解析option得到请求头和body的编解码方式
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	//拿到了编解码方式，通过编解码方式得到对应的初始化编解码接口的方法
	//这个方法传入一个io读写，返回一个编解码接口
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serverCodec(f(conn))
}

var invalidRequest = struct{}{}

// 传入连接的编解码接口进行读取请求，处理请求和回复请求
func (server *Server) serverCodec(cc codec.Codec) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		//先进行读取操作
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break
			}
			req.h.Error = err.Error()
			//这个请求读取的时候有错误 直接回复客户端
			//因为等待连接是for死循环的，然后并发的读取编解码接口，得到接口后并发的处理请求
			//之后的处理操作也是并发的，回复客户端时要一条一条回复
			server.sendRequest(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		//进行处理操作 在处理函数中直接回复了
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	_ = cc.Close()
}

type request struct {
	//头信息 包含服务名称及方法 序列号 错误
	h *codec.Header
	//a是客户端调用服务的方法时传入的参数，r是服务端返回的参数
	argv, replyv reflect.Value
}

// 读取请求头
func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}
func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	//先读取请求头
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	//读取请求体，也就是客户端传来的参数
	req.argv = reflect.New(reflect.TypeOf(""))
	if err := cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	return req, nil
}
func (server *Server) sendRequest(cc codec.Codec, h *codec.Header, body interface{}, mu *sync.Mutex) {
	//加锁
	mu.Lock()
	defer mu.Unlock()
	//写入缓冲区
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}
func (server *Server) handleRequest(cc codec.Codec, req *request, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq))
	server.sendRequest(cc, req.h, req.replyv.Interface(), mu)
}
