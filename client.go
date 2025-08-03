package myrpc

import (
	"MyRpc/codec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

// 客户端发送的请求以及接收响应的载体
type Call struct {
	Seq           uint64      //序列号
	ServiceMethod string      //服务名和方法
	Args          interface{} //客户端传入参数
	Reply         interface{} //服务端返回的参数 服务端返回时填充进去
	Error         error       //错误
	Done          chan *Call  //为了支持异步调用，当调用结束时，通知调用方
}

// 给无缓冲通道传入call
func (call *Call) done() {
	call.Done <- call
}

// 客户端
type Client struct {
	cc      codec.Codec  //编解码接口
	opt     *Option      //协议头
	sending sync.Mutex   //保持发送互斥
	header  codec.Header //请求头
	mu      sync.Mutex
	seq     uint64           //请求序列号
	pending map[uint64]*Call //存未完成的请求
	//两个任意一个为true就为关闭
	closing  bool //用户主动关闭
	shutdown bool //有错误发生改为true
}

var _ io.Closer = (*Client)(nil)
var ErrShutdown = errors.New("connection is shut down")

// 实现close
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// 确认是否可用
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 注册请求
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 移除请求
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 服务端和客户端发生错误时调用
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		//请求结束了 通知调用方
		call.done()
	}
}

// 接收响应
func (client *Client) receive() {
	var err error
	for err == nil {
		//读取服务端返回的头，拿到请求的序列号
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		//拿到服务器处理完的请求
		call := client.removeCall(h.Seq)
		//分情况，call没有：可能发送出错或者出错了但是服务端正常处理，
		//服务端处理错误
		//一切正常
		switch {
		case call == nil:
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	//出现错误
	client.terminateCalls(err)
}
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	//得先拿到初始化编解码接口的函数
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	//然后和服务端确认编解码方法
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		cc:      cc,
		opt:     opt,
		seq:     1,
		pending: make(map[uint64]*Call),
	}
	//开启协程接收响应
	go client.receive()
	return client
}

// 实现可选编解码协议
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// 实现一个dial函数（创建一个连接，连接到服务端地址）
// 正常是返回一个连接实例 我们实现了用连接实例初始化客户端
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	defer func() {
		if client == nil {
			_ = conn.Close()
		}
	}()
	return NewClient(conn, opt)
}

// 完成发送请求
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()

	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// 初始化call然后进行发送操作
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	//阻塞call.Done，等待响应返回
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
}
