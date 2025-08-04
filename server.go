package myrpc

import (
	"MyRpc/codec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3bef5c

// 协商信息，让双方知道怎么读写数据
type Option struct {
	MagicNumber    int
	CodecType      codec.Type    //head和body所用的编解码格式
	ConnectTimeout time.Duration //连接默认时间限制
	HandleTimeout  time.Duration //处理默认时间限制
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

type Server struct {
	serviceMap sync.Map
}

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

// 注册服务
func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	//存入map，服务名字和服务的映射 如果dup为true则说明有相同的键
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

// 将注册逻辑委托给默认的 Server 实例
func Register(rcvr interface{}) error {
	return DefaultServer.Register(rcvr)
}
func (server *Server) findService(serviceMethod string) (svc *service, mType *methodType, err error) {
	//找到.的位置，服务方法一般为t.T
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	//找到服务实例
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	svc = svci.(*service)
	mType = svc.method[methodName]
	if mType == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

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
		go server.handleRequest(cc, req, sending, wg, 10*time.Second)
	}
	wg.Wait()
	_ = cc.Close()
}

// 请求
type request struct {
	//头信息 包含服务名称及方法 序列号 错误
	h *codec.Header
	//a是客户端调用服务的方法时传入的参数，r是服务端返回的参数
	argv, replyv reflect.Value
	mtype        *methodType //服务的一个方法结构体
	svc          *service    //服务实例结构体
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
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReply()
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	//读取请求体，也就是客户端传来的参数
	//req.argv = reflect.New(reflect.TypeOf(""))
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argv err:", err)
		return req, err
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
func (server *Server) handleRequest(cc codec.Codec, req *request, mu *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	//服务调用成功发送的通道
	called := make(chan struct{})
	//回复完客户端发送的通道
	sent := make(chan struct{})
	go func() {
		//直接用服务调用方法
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendRequest(cc, req.h, invalidRequest, mu)
			sent <- struct{}{}
			return
		}
		server.sendRequest(cc, req.h, req.replyv.Interface(), mu)
		sent <- struct{}{}
	}()
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendRequest(cc, req.h, invalidRequest, mu)
	case <-called:
		<-sent
	}
}

const (
	connected        = "200 Connected to Gee RPC"
	defaultRPCPath   = "/_geeprc_"
	defaultDebugPath = "/debug/geerpc"
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//w是响应写入器，r是客户端请求
	//必须为connect请求
	if r.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	//获取底层的网络连接
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", r.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	s.ServerConn(conn)
}
func (s *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, s)
}
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
