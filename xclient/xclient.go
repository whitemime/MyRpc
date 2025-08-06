package xclient

import (
	myrpc "MyRpc"
	"context"
	"io"
	"reflect"
	"sync"
)

//暴露一个支持负载均衡的客户端

type XClient struct {
	d       Discovery
	mode    SelectMode
	opt     *myrpc.Option
	mu      sync.Mutex
	clients map[string]*myrpc.Client
}

var _ io.Closer = (*XClient)(nil)

func NewXClient(d Discovery, mode SelectMode, opt *myrpc.Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*myrpc.Client)}
}

func (xc *XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for k, c := range xc.clients {
		_ = c.Close()
		delete(xc.clients, k)
	}
	return nil
}

func (xc *XClient) dial(rpcaddr string) (*myrpc.Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	client, ok := xc.clients[rpcaddr]
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(xc.clients, rpcaddr)
		client = nil
	}
	if client == nil {
		var err error
		client, err = myrpc.XDial(rpcaddr, xc.opt)
		if err != nil {
			return nil, err
		}
		xc.clients[rpcaddr] = client
	}
	return client, nil
}

func (xc *XClient) call(rpcaddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	client, err := xc.dial(rpcaddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply)
}
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcaddr, err := xc.d.Get(xc.mode)
	if err != nil {
		return err
	}
	return xc.call(rpcaddr, ctx, serviceMethod, args, reply)
}

// 广播到所有服务
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex // protect e and replyDone
	var e error
	replyDone := reply == nil // if reply is nil, don't need to set value
	ctx, cancel := context.WithCancel(ctx)
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel() // if any call failed, cancel unfinished calls
			}
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
